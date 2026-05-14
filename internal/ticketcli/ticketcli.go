// Package ticketcli implements the `ticket` command-line dispatcher.
//
// It is extracted from cmd/ticket so tests can invoke subcommands in-process
// via [Run] without forking a subprocess.
package ticketcli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"framework2/internal/dir"
	"framework2/internal/next"
)

const usage = `usage:
  ticket [--db <path>] dir link <id> <path>
  ticket [--db <path>] dir remove <id> <path>
  ticket [--db <path>] dir list <id>
  ticket [--db <path>] next [<id>]`

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
	db, rest, err := extractDB(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errors.New(usage)
	}
	switch rest[0] {
	case "dir":
		return runDir(db, rest[1:], stdout)
	case "next":
		return runNext(db, rest[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", rest[0], usage)
	}
}

func runDir(db string, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: ticket dir <link|remove|list> ...")
	}
	switch args[0] {
	case "link":
		if len(args) != 3 {
			return errors.New("usage: ticket dir link <id> <path>")
		}
		return dir.Link(db, args[1], args[2])
	case "remove":
		if len(args) != 3 {
			return errors.New("usage: ticket dir remove <id> <path>")
		}
		return dir.Remove(db, args[1], args[2])
	case "list":
		if len(args) != 2 {
			return errors.New("usage: ticket dir list <id>")
		}
		return dir.List(db, args[1], stdout)
	default:
		return fmt.Errorf("unknown dir subcommand %q", args[0])
	}
}

func runNext(db string, args []string, stdout, stderr io.Writer) error {
	id := ""
	switch len(args) {
	case 0:
	case 1:
		id = args[0]
	default:
		return errors.New("usage: ticket next [<id>]")
	}
	return next.Run(db, id, stdout, stderr)
}

func extractDB(args []string) (string, []string, error) {
	db := ""
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--db":
			if i+1 >= len(args) {
				return "", nil, errors.New("--db requires a value")
			}
			db = args[i+1]
			i++
		case strings.HasPrefix(a, "--db="):
			db = strings.TrimPrefix(a, "--db=")
		default:
			rest = append(rest, a)
		}
	}
	return db, rest, nil
}
