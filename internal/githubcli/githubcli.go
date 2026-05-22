// Package githubcli implements the `github` command: a small CLI that
// searches across every GitHub org the authenticated user belongs to via the
// `gh` CLI. Today only the `branches` subcommand is wired up; future
// subcommands (e.g. `code`) will reuse [ListOrgs].
package githubcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const usage = `usage: github <subcommand> [args...]

subcommands:
  branches <search-term> [--json]                        find branches whose name contains <search-term>
  code <search-term>... [--org <org>]... [--limit N] [--threshold N] [--json] [-- <gh flags>...]
                                                         search code in the given GitHub org(s) (default: knime)

Requires the gh CLI to be authenticated. See 'github <subcommand> --help'.`

const branchesUsage = `usage: github branches <search-term> [--json]

Case-insensitive substring search for branch names across every repo
(non-archived, pushed in the past year) in every GitHub org the
authenticated user belongs to.

Output:
  default: one tab-separated <owner>/<repo>\t<branch> line per match,
           or "No branches found." when nothing matches.
  --json:  a single JSON array of {owner, repo, branch} objects.`

const codeUsage = `usage: github code <search-term>... [--org <org>]... [--limit <n>] [--threshold <n>] [--json] [-- <gh flags>...]

Runs ` + "`gh search code`" + ` against one or more GitHub orgs (one --owner per
org, concurrently) and merges the results.

Arguments:
  <search-term>...    one or more positional terms forwarded to gh search code.
  --org <org>         org to search; repeat to fan out across multiple orgs
                      (default: knime).
  --limit <n>         per-org cap forwarded as --limit to gh search code
                      (default: 100; gh caps at 1000).
  --threshold <n>     when total results exceed n, content-only matches are
                      collapsed to a count per repo (default: 100). Does not
                      affect --json output.
  --json              emit the grouped results as JSON.
  -- <gh flags>...    everything after -- is forwarded verbatim to gh search
                      code (e.g. --language python, --filename package.json).`

// Options pins values that production reads from the environment. Tests use
// it to inject a fake [GHRunner] in place of the real gh CLI.
type Options struct {
	// GHRunner overrides the default `gh` shell-out. When nil, a runner
	// that exec's the real gh binary is built using the [Run] stderr.
	GHRunner GHRunner
}

// Run is the binary entrypoint. Returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunWithOptions(Options{}, args, stdout, stderr)
}

// RunWithOptions is like [Run] but lets callers inject a fake [GHRunner].
// Used by tests.
func RunWithOptions(opts Options, args []string, stdout, stderr io.Writer) int {
	runner := opts.GHRunner
	if runner == nil {
		runner = newDefaultRunner(stderr)
	}
	if err := dispatch(context.Background(), runner, args, stdout); err != nil {
		fmt.Fprintln(stderr, "github:", err)
		return 1
	}
	return 0
}

func dispatch(ctx context.Context, runner GHRunner, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage)
		return nil
	case "branches":
		return branchesCommand(ctx, runner, args[1:], stdout)
	case "code":
		return codeCommand(ctx, runner, args[1:], stdout)
	default:
		return fmt.Errorf("unknown subcommand %q\n\n%s", args[0], usage)
	}
}

func branchesCommand(ctx context.Context, runner GHRunner, args []string, stdout io.Writer) error {
	var (
		searchTerm string
		asJSON     bool
		sawTerm    bool
	)
	for _, a := range args {
		switch {
		case a == "-h" || a == "--help":
			fmt.Fprintln(stdout, branchesUsage)
			return nil
		case a == "--json":
			asJSON = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q\n\n%s", a, branchesUsage)
		case !sawTerm:
			searchTerm = a
			sawTerm = true
		default:
			return fmt.Errorf("unexpected argument %q\n\n%s", a, branchesUsage)
		}
	}
	if !sawTerm {
		return errors.New(branchesUsage)
	}

	matches, err := ListBranches(ctx, runner, searchTerm)
	if err != nil {
		return err
	}
	return writeBranches(stdout, matches, asJSON)
}

// writeBranches formats matches for stdout. JSON form is one object per line
// (indented by two spaces) inside a top-level array, matching the example in
// the ticket description.
func writeBranches(w io.Writer, matches []Branch, asJSON bool) error {
	if asJSON {
		if len(matches) == 0 {
			_, err := fmt.Fprintln(w, "[]")
			return err
		}
		if _, err := fmt.Fprintln(w, "["); err != nil {
			return err
		}
		for i, m := range matches {
			line, err := json.Marshal(m)
			if err != nil {
				return err
			}
			sep := ","
			if i == len(matches)-1 {
				sep = ""
			}
			if _, err := fmt.Fprintf(w, "  %s%s\n", line, sep); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintln(w, "]")
		return err
	}
	if len(matches) == 0 {
		_, err := fmt.Fprintln(w, "No branches found.")
		return err
	}
	for _, m := range matches {
		if _, err := fmt.Fprintf(w, "%s/%s\t%s\n", m.Owner, m.Repo, m.Branch); err != nil {
			return err
		}
	}
	return nil
}
