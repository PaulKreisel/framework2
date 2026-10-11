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
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"framework2/internal/agentbrowser"
	"framework2/internal/devserver"
	"framework2/internal/env"
	"framework2/internal/ticketcli"
	"framework2/internal/wfmcp"
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
	return RunFromWithOptions(ticketcli.Options{}, cwd, args, stdout, stderr)
}

// RunFromWithOptions is like RunFrom but lets callers pin BEADS_DIR via
// ticketcli.Options. Used by tests driving per-test .beads/ directories.
func RunFromWithOptions(opts ticketcli.Options, cwd string, args []string, stdout, stderr io.Writer) int {
	// The one subcommand that must run without a ticket: Claude Code starts
	// the proxy in whatever folder a session opens, and a folder outside any
	// ticket env must yield an empty tool list, not a failed server.
	if len(args) > 0 && args[0] == "wf-mcp-proxy" {
		if err := wfmcp.Main(cwd, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, "this: wf-mcp-proxy:", err)
			return 1
		}
		return 0
	}
	// The `backend` sidecar of the devserver preset: pde starts it next to the
	// backend JVM, it blocks until the given ports accept connections and
	// prints `ready: ...` (relayed by pde as `[backend] ready: ...`). No ticket
	// needed, the ports are in the command line.
	// `--open <url>` afterwards waits for that URL's port too and opens it in
	// the default browser.
	// `this view`: show the ticket's browser-editor tab in the KNIME Agent
	// Browser (the tab the ticket's Playwright sessions use as well).
	if len(args) > 0 && args[0] == "view" {
		if err := agentbrowser.View(cwd, stdout); err != nil {
			fmt.Fprintln(stderr, "this: view:", err)
			return 1
		}
		return 0
	}
	if len(args) > 0 && args[0] == "devserver-wait" {
		ports, err := devserver.ParsePorts(args[1:])
		if err == nil {
			err = devserver.WaitPorts(context.Background(), ports, stdout)
		}
		if err != nil {
			fmt.Fprintln(stderr, "this:", err)
			return 1
		}
		return 0
	}
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
	return ticketcli.RunWithOptions(opts, forwarded, stdout, stderr)
}

// Discover walks up from start looking for a .ticket-id marker file. Returns
// the ticket id (trimmed) and the directory containing the marker. Thin
// delegate to [env.Discover] — the walk lives next to the marker constant so
// `ticket` can reuse it (see `ticket agent continue`).
func Discover(start string) (id, envDir string, err error) {
	return env.Discover(start)
}
