// Package thiscli implements the `this` command — a context-scoped facade
// over `ticket` that resolves the ticket id from a .ticket-id marker found
// by walking up from the caller's cwd, then forwards to the shared
// [ticketcli.Run] dispatcher via the global --ticket flag.
//
// This package deliberately contains zero per-subcommand knowledge: new
// ticket subcommands work under `this` automatically because the id arrives
// through the global flag, not via positional manipulation.
package thiscli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"framework2/internal/env"
	"framework2/internal/ticketcli"
)

// Run is the convenience entrypoint that uses the process cwd.
func Run(args []string, stdout, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, "this:", err)
		return 1
	}
	return RunFrom(cwd, args, stdout, stderr)
}

// RunFrom is like Run but uses an explicit starting directory. Exposed so
// tests can avoid os.Chdir.
func RunFrom(cwd string, args []string, stdout, stderr io.Writer) int {
	id, _, err := Discover(cwd)
	if err != nil {
		fmt.Fprintln(stderr, "this:", err)
		return 1
	}
	for _, a := range args {
		if a == "--ticket" || strings.HasPrefix(a, "--ticket=") {
			fmt.Fprintln(stderr, "this: do not pass --ticket; it is inferred from the .ticket-id marker")
			return 1
		}
	}
	forwarded := append([]string{"--ticket", id}, args...)
	return ticketcli.Run(forwarded, stdout, stderr)
}

// Discover walks up from start looking for a .ticket-id marker file. Returns
// the ticket id (trimmed) and the directory containing the marker.
func Discover(start string) (id, envDir string, err error) {
	dir := start
	for {
		marker := filepath.Join(dir, env.MarkerFile)
		data, rerr := os.ReadFile(marker)
		if rerr == nil {
			id = strings.TrimSpace(string(data))
			if id == "" {
				return "", "", fmt.Errorf("empty .ticket-id at %s", marker)
			}
			return id, dir, nil
		}
		if !os.IsNotExist(rerr) {
			return "", "", rerr
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", fmt.Errorf("not inside a ticket env (no .ticket-id found from %s)", start)
		}
		dir = parent
	}
}
