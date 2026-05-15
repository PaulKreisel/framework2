// Package ticketcli implements the `ticket` command-line dispatcher.
//
// It is extracted from cmd/ticket so tests can invoke subcommands in-process
// via [Run] without forking a subprocess. The `this` binary shares this
// dispatcher: it resolves a ticket id from a .ticket-id marker and forwards
// the call via the global `--ticket` flag, so there is no `this`-side
// subcommand knowledge to drift against `ticket`.
package ticketcli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"framework2/internal/agent"
	"framework2/internal/create"
	"framework2/internal/dir"
	"framework2/internal/env"
	"framework2/internal/finish"
	"framework2/internal/next"
)

const usage = `usage:
  ticket [global-flags] <subcommand> [args...]

global flags:
  --db <path>      bd database path
  --ticket <id>    ticket id (alternative to per-subcommand positional)

subcommands:
  dir link [<id>] <path>             link a path to a ticket
  dir remove [<id>] <path>           unlink a path from a ticket
  dir list [<id>]                    list paths linked to a ticket
  dir worktree [<id>] <bare-repo>    create a worktree off <bare-repo> for the
                                     ticket and link it (cascades through
                                     parents so ancestors gain the worktree too)
  env [<id>]                              create on-disk env folder for a ticket
  env syncback [<id>]                     push <env>/description.md back to the
                                          bead description (one-way file → bead)
  agent [<id>]                            launch claude from a ticket's env folder
                                          in coding mode
  agent list [<id>] [--json]              list agents that have been launched on a
                                          ticket (newest first)
  agent relaunch [<id>] [extra-args...]   from inside a session: ask the running
                                          wrapper to re-exec claude with --continue
                                          (use after a dir link/worktree)
  spec [<id>]                             launch claude from a ticket's env folder
                                          in spec mode (edits description.md and
                                          spawns related beads; does not code)
  create child <title>                    new bead as a child of the current ticket
  create followup [--not-blocked] <title>
                                          sibling of the current ticket (top-level if
                                          current has no parent). By default current
                                          blocks the new bead; --not-blocked skips
                                          that dep.
  next [<id>]                             show ready descendants of a ticket
  finish [<id>]                           wrap up: rebase the ticket's worktrees
                                          onto their parent branch (FF when no
                                          divergence, real rebase otherwise),
                                          close the bead, remove the worktrees,
                                          and delete the env folder. One-way;
                                          never invoked automatically.
  finish [<id>] --continue                resume a finish whose rebase paused on
                                          conflicts: runs ` + "`git rebase --continue`" + ` in
                                          each paused worktree, then completes
                                          the rest of the flow.`

// Options configures a Run invocation. The zero value uses
// os.Getenv("BEADS_DIR") for BeadsDir, matching the production behaviour of
// the `ticket` binary.
type Options struct {
	// BeadsDir, when non-empty, overrides BEADS_DIR for the duration of this
	// invocation: env folder lookups and child bd subprocess invocations use
	// this value instead of os.Getenv("BEADS_DIR"). Tests use it to drive a
	// per-test .beads/ directory in parallel.
	BeadsDir string
}

// Run executes the ticket command-line with the given args, writing normal
// output to stdout and errors to stderr. Returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunWithOptions(Options{}, args, stdout, stderr)
}

// RunWithOptions is like [Run] but lets callers pin BEADS_DIR explicitly via
// [Options].
func RunWithOptions(opts Options, args []string, stdout, stderr io.Writer) int {
	beadsDir := opts.BeadsDir
	if beadsDir == "" {
		beadsDir = os.Getenv("BEADS_DIR")
	}
	if err := dispatch(beadsDir, args, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "ticket:", err)
		return 1
	}
	return 0
}

func dispatch(beadsDir string, args []string, stdout, stderr io.Writer) error {
	db, ticket, rest, err := extractGlobal(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errors.New(usage)
	}
	switch rest[0] {
	case "dir":
		return runDir(db, beadsDir, ticket, rest[1:], stdout)
	case "env":
		return runEnv(db, beadsDir, ticket, rest[1:], stdout)
	case "agent":
		return runAgent(db, beadsDir, ticket, rest[1:], agent.ModeAgent, stdout)
	case "spec":
		return runAgent(db, beadsDir, ticket, rest[1:], agent.ModeSpec, stdout)
	case "create":
		return runCreate(db, beadsDir, ticket, rest[1:], stdout)
	case "next":
		return runNext(db, beadsDir, ticket, rest[1:], stdout, stderr)
	case "finish":
		return runFinish(db, beadsDir, ticket, rest[1:], stdout)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", rest[0], usage)
	}
}

func runDir(db, beadsDir, ticket string, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: ticket dir <link|remove|list> ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "link":
		id, tail, err := resolveID(ticket, rest, 1, "usage: ticket dir link [<id>] <path>")
		if err != nil {
			return err
		}
		if err := dir.Link(db, beadsDir, id, tail[0]); err != nil {
			return err
		}
		// Only the leaf ticket — the one we just mutated — may have a live
		// wrapper to refresh. dir.Worktree cascades into ancestors, but
		// their sessions aren't running, so we never try to signal them.
		return agent.RelaunchIfActive(beadsDir, id)
	case "remove":
		id, tail, err := resolveID(ticket, rest, 1, "usage: ticket dir remove [<id>] <path>")
		if err != nil {
			return err
		}
		return dir.Remove(db, beadsDir, id, tail[0])
	case "list":
		id, _, err := resolveID(ticket, rest, 0, "usage: ticket dir list [<id>]")
		if err != nil {
			return err
		}
		return dir.List(db, beadsDir, id, stdout)
	case "worktree":
		id, tail, err := resolveID(ticket, rest, 1, "usage: ticket dir worktree [<id>] <bare-repo>")
		if err != nil {
			return err
		}
		if err := dir.Worktree(db, beadsDir, id, tail[0]); err != nil {
			return err
		}
		// See the comment under `link` above: cascade-created ancestor
		// worktrees belong to tickets whose agents aren't running, so we
		// only relaunch the explicitly addressed ticket.
		return agent.RelaunchIfActive(beadsDir, id)
	default:
		return fmt.Errorf("unknown dir subcommand %q", sub)
	}
}

func runEnv(db, beadsDir, ticket string, args []string, stdout io.Writer) error {
	if len(args) > 0 && args[0] == "syncback" {
		id, _, err := resolveID(ticket, args[1:], 0, "usage: ticket env syncback [<id>]")
		if err != nil {
			return err
		}
		return agent.Syncback(db, beadsDir, id)
	}
	id, _, err := resolveID(ticket, args, 0, "usage: ticket env [<id>]")
	if err != nil {
		return err
	}
	envPath, err := env.Create(db, beadsDir, id)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, envPath)
	return enterEnvShell(stdout, envPath)
}

// enterEnvShell drops the caller into an interactive $SHELL rooted at envPath
// so they end up inside the env folder after `ticket env`. A binary cannot cd
// its parent shell, so we exec a child shell instead — `exit` returns them.
//
// Gated on stdout being an *os.File terminal so tests and pipelines (which
// pass a bytes.Buffer or redirect) keep the plain print-and-return behavior.
func enterEnvShell(stdout io.Writer, envPath string) error {
	if !isTerminalWriter(stdout) {
		return nil
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		return nil
	}
	if err := os.Chdir(envPath); err != nil {
		return fmt.Errorf("chdir to env: %w", err)
	}
	return syscall.Exec(shell, []string{shell}, os.Environ())
}

func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func runAgent(db, beadsDir, ticket string, args []string, mode agent.Mode, stdout io.Writer) error {
	if mode == agent.ModeAgent && len(args) > 0 && args[0] == "relaunch" {
		// `ticket agent relaunch [<id>] [extra-args...]`
		id, extra, err := resolveIDVariadic(ticket, args[1:], "usage: ticket agent relaunch [<id>] [extra-args...]")
		if err != nil {
			return err
		}
		return agent.Relaunch(beadsDir, id, extra)
	}
	if mode == agent.ModeAgent && len(args) > 0 && args[0] == "list" {
		// `ticket agent list [<id>] [--json]`
		var jsonOut bool
		positional := make([]string, 0, len(args)-1)
		for _, a := range args[1:] {
			if a == "--json" {
				jsonOut = true
				continue
			}
			positional = append(positional, a)
		}
		id, _, err := resolveID(ticket, positional, 0, "usage: ticket agent list [<id>] [--json]")
		if err != nil {
			return err
		}
		return agent.ListAgents(db, beadsDir, id, jsonOut, stdout)
	}
	usage := "usage: ticket agent [<id>]"
	if mode == agent.ModeSpec {
		usage = "usage: ticket spec [<id>]"
	}
	id, _, err := resolveID(ticket, args, 0, usage)
	if err != nil {
		return err
	}
	return agent.Run(db, beadsDir, id, mode)
}

func runCreate(db, beadsDir, ticket string, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: ticket create <child|followup> [<id>] [--not-blocked] <title>")
	}
	sub, rest := args[0], args[1:]

	// Extract --not-blocked flag from anywhere in rest; surrounding tokens
	// are positionals (id, title) handled by resolveID below.
	var notBlocked bool
	positional := make([]string, 0, len(rest))
	for _, a := range rest {
		if a == "--not-blocked" {
			notBlocked = true
			continue
		}
		positional = append(positional, a)
	}

	var rel create.Relation
	switch sub {
	case "child":
		if notBlocked {
			return errors.New("--not-blocked is only valid for `create followup`")
		}
		rel = create.Child
	case "followup":
		rel = create.Followup
	default:
		return fmt.Errorf("unknown create relation %q (want child|followup)", sub)
	}
	id, tail, err := resolveID(ticket, positional, 1, fmt.Sprintf("usage: ticket create %s [<id>] [--not-blocked] <title>", sub))
	if err != nil {
		return err
	}
	return create.Run(db, beadsDir, id, tail[0], rel, !notBlocked, stdout)
}

func runFinish(db, beadsDir, ticket string, args []string, stdout io.Writer) error {
	var continueMode bool
	positional := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--continue" {
			continueMode = true
			continue
		}
		positional = append(positional, a)
	}
	id, _, err := resolveID(ticket, positional, 0, "usage: ticket finish [<id>] [--continue]")
	if err != nil {
		return err
	}
	return finish.Run(db, beadsDir, id, continueMode, stdout)
}

func runNext(db, beadsDir, ticket string, args []string, stdout, stderr io.Writer) error {
	id, _, err := resolveOptionalID(ticket, args, 0, "usage: ticket next [<id>]")
	if err != nil {
		return err
	}
	return next.Run(db, beadsDir, id, stdout, stderr)
}

// resolveID pulls the ticket id from either the global --ticket flag or the
// first positional. extra is the number of remaining positionals expected
// after the id is consumed. Returns the resolved id and the tail of
// positionals (length == extra). Errors if both forms are supplied or if no
// id is available.
func resolveID(ticket string, args []string, extra int, usage string) (string, []string, error) {
	if ticket != "" {
		if len(args) != extra {
			return "", nil, errors.New(usage)
		}
		return ticket, args, nil
	}
	if len(args) != extra+1 {
		return "", nil, errors.New(usage)
	}
	return args[0], args[1:], nil
}

// resolveIDVariadic is like resolveID but accepts a variable number of
// trailing positionals (zero or more). Used by `relaunch`.
func resolveIDVariadic(ticket string, args []string, usage string) (string, []string, error) {
	if ticket != "" {
		return ticket, args, nil
	}
	if len(args) == 0 {
		return "", nil, errors.New(usage)
	}
	return args[0], args[1:], nil
}

// resolveOptionalID is like resolveID but tolerates a missing id (returns
// the empty string in that case). Used by `next`.
func resolveOptionalID(ticket string, args []string, extra int, usage string) (string, []string, error) {
	if ticket != "" {
		if len(args) != extra {
			return "", nil, errors.New(usage)
		}
		return ticket, args, nil
	}
	switch len(args) {
	case extra:
		return "", args, nil
	case extra + 1:
		return args[0], args[1:], nil
	default:
		return "", nil, errors.New(usage)
	}
}

func extractGlobal(args []string) (db, ticket string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--db":
			if i+1 >= len(args) {
				return "", "", nil, errors.New("--db requires a value")
			}
			db = args[i+1]
			i++
		case strings.HasPrefix(a, "--db="):
			db = strings.TrimPrefix(a, "--db=")
		case a == "--ticket":
			if i+1 >= len(args) {
				return "", "", nil, errors.New("--ticket requires a value")
			}
			ticket = args[i+1]
			i++
		case strings.HasPrefix(a, "--ticket="):
			ticket = strings.TrimPrefix(a, "--ticket=")
		default:
			rest = append(rest, a)
		}
	}
	return db, ticket, rest, nil
}
