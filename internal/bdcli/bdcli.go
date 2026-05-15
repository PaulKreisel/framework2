// Package bdcli centralizes how the framework spawns the `bd` binary.
//
// Production code paths that shell out to `bd` route through [Cmd] so each
// invocation can be pointed at a specific .beads/ directory via BEADS_DIR,
// independent of the process-wide environment. This is what lets multiple
// goroutines (e.g. parallel tests) drive separate bd databases through the
// same in-process dispatcher.
package bdcli

import (
	"os"
	"os/exec"
	"strings"
)

// Cmd builds an *exec.Cmd that invokes `bd` with optional --db plumbing and
// an optional BEADS_DIR override.
//
//   - db, when non-empty, is forwarded as `--db <db>` (legacy SQLite path).
//   - beadsDir, when non-empty, overrides BEADS_DIR in the subprocess env.
//     Empty means "inherit from os.Environ() as-is".
//
// The returned command has nil Stdout/Stderr/Stdin; callers attach as needed.
func Cmd(beadsDir, db string, args ...string) *exec.Cmd {
	full := make([]string, 0, 2+len(args))
	if db != "" {
		full = append(full, "--db", db)
	}
	full = append(full, args...)
	cmd := exec.Command("bd", full...)
	if beadsDir != "" {
		cmd.Env = envOverride(os.Environ(), "BEADS_DIR", beadsDir)
	}
	return cmd
}

func envOverride(env []string, key, val string) []string {
	out := make([]string, 0, len(env)+1)
	prefix := key + "="
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, prefix+val)
}
