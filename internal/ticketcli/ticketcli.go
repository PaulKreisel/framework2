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

	"framework2/internal/agent"
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
  agent [<id>]                            launch claude from a ticket's env folder
  agent relaunch [<id>] [extra-args...]   from inside a session: ask the running
                                          wrapper to re-exec claude with --continue
                                          (use after a dir link/worktree)
  next [<id>]                             show ready descendants of a ticket
  finish [<id>]                           wrap up: FF-rebase the ticket's worktrees
                                          onto their parent branch, close the bead,
                                          remove the worktrees, and delete the env
                                          folder. One-way; never invoked automatically.`

// Run executes the ticket command-line with the given args, writing normal
// output to stdout and errors to stderr. Returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if err := dispatch(args, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "ticket:", err)
		return 1
	}
	return 0
}

func dispatch(args []string, stdout, stderr io.Writer) error {
	db, ticket, rest, err := extractGlobal(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errors.New(usage)
	}
	switch rest[0] {
	case "dir":
		return runDir(db, ticket, rest[1:], stdout)
	case "env":
		return runEnv(db, ticket, rest[1:], stdout)
	case "agent":
		return runAgent(db, ticket, rest[1:])
	case "next":
		return runNext(db, ticket, rest[1:], stdout, stderr)
	case "finish":
		return runFinish(db, ticket, rest[1:], stdout)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", rest[0], usage)
	}
}

func runDir(db, ticket string, args []string, stdout io.Writer) error {
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
		return dir.Link(db, id, tail[0])
	case "remove":
		id, tail, err := resolveID(ticket, rest, 1, "usage: ticket dir remove [<id>] <path>")
		if err != nil {
			return err
		}
		return dir.Remove(db, id, tail[0])
	case "list":
		id, _, err := resolveID(ticket, rest, 0, "usage: ticket dir list [<id>]")
		if err != nil {
			return err
		}
		return dir.List(db, id, stdout)
	case "worktree":
		id, tail, err := resolveID(ticket, rest, 1, "usage: ticket dir worktree [<id>] <bare-repo>")
		if err != nil {
			return err
		}
		return dir.Worktree(db, id, tail[0])
	default:
		return fmt.Errorf("unknown dir subcommand %q", sub)
	}
}

func runEnv(db, ticket string, args []string, stdout io.Writer) error {
	id, _, err := resolveID(ticket, args, 0, "usage: ticket env [<id>]")
	if err != nil {
		return err
	}
	envPath, err := env.Create(db, os.Getenv("BEADS_DIR"), id)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, envPath)
	return nil
}

func runAgent(db, ticket string, args []string) error {
	if len(args) > 0 && args[0] == "relaunch" {
		// `ticket agent relaunch [<id>] [extra-args...]`
		id, extra, err := resolveIDVariadic(ticket, args[1:], "usage: ticket agent relaunch [<id>] [extra-args...]")
		if err != nil {
			return err
		}
		return agent.Relaunch(os.Getenv("BEADS_DIR"), id, extra)
	}
	id, _, err := resolveID(ticket, args, 0, "usage: ticket agent [<id>]")
	if err != nil {
		return err
	}
	return agent.Run(db, os.Getenv("BEADS_DIR"), id)
}

func runFinish(db, ticket string, args []string, stdout io.Writer) error {
	id, _, err := resolveID(ticket, args, 0, "usage: ticket finish [<id>]")
	if err != nil {
		return err
	}
	return finish.Run(db, os.Getenv("BEADS_DIR"), id, stdout)
}

func runNext(db, ticket string, args []string, stdout, stderr io.Writer) error {
	id, _, err := resolveOptionalID(ticket, args, 0, "usage: ticket next [<id>]")
	if err != nil {
		return err
	}
	return next.Run(db, id, stdout, stderr)
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
