// Package env implements the `ticket env` subcommand: creating an on-disk
// environment directory for a ticket, with a .ticket-id marker that the
// companion `this` command discovers via cwd walk-up.
package env

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"framework2/internal/bdcli"
	"framework2/internal/project"
)

// MarkerFile is the basename of the per-env file containing the ticket id.
const MarkerFile = ".ticket-id"

// ErrNoTicketEnv is returned by [Discover] when no [MarkerFile] exists in the
// starting directory or any of its ancestors. Callers that have a meaningful
// non-env behaviour (e.g. `ticket agent continue`, which falls back to a plain
// claude launch) branch on this rather than treating it as a hard failure.
var ErrNoTicketEnv = errors.New("not inside a ticket env")

// Discover walks up from start looking for a [MarkerFile]. Returns the ticket
// id (trimmed) and the directory containing the marker. Wraps
// [ErrNoTicketEnv] when the walk reaches the filesystem root without a hit.
func Discover(start string) (id, envDir string, err error) {
	dir := start
	for {
		marker := filepath.Join(dir, MarkerFile)
		data, rerr := os.ReadFile(marker)
		if rerr == nil {
			id = strings.TrimSpace(string(data))
			if id == "" {
				return "", "", fmt.Errorf("empty %s at %s", MarkerFile, marker)
			}
			return id, dir, nil
		}
		if !os.IsNotExist(rerr) {
			return "", "", rerr
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", "", fmt.Errorf("%w (no %s found from %s)", ErrNoTicketEnv, MarkerFile, start)
		}
		dir = parent
	}
}

// stepLabel mirrors finish.StepLabel — the label stamped onto step-beads by the
// step-driven finish path. It is duplicated as a literal rather than imported
// because internal/finish's test package imports internal/env, so an
// env→finish import would form a cycle in the finish test build.
const stepLabel = "finish-step"

// Create makes (or refreshes) an env folder for ticketID at
// beadsDir/envs/<id>/ and writes a .ticket-id marker inside it.
//
// Idempotent: re-running with the same id is a no-op success. The ticket is
// looked up via `bd show` first to fail fast on typos.
func Create(db, beadsDir, ticketID string) (string, error) {
	if ticketID == "" {
		return "", errors.New("ticket id required")
	}
	if beadsDir == "" {
		return "", errors.New("BEADS_DIR not set: cannot derive env root")
	}
	if err := assertOpenable(db, beadsDir, ticketID); err != nil {
		return "", err
	}
	envPath := project.Path(beadsDir, filepath.Join("envs", ticketID))
	if err := os.MkdirAll(envPath, 0o755); err != nil {
		return "", fmt.Errorf("create env dir: %w", err)
	}
	marker := filepath.Join(envPath, MarkerFile)
	if existing, err := os.ReadFile(marker); err == nil {
		if strings.TrimSpace(string(existing)) == ticketID {
			return envPath, nil
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("read marker: %w", err)
	}
	if err := os.WriteFile(marker, []byte(ticketID+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("write marker: %w", err)
	}
	return envPath, nil
}

// EnterShell drops the caller into an interactive $SHELL rooted at envPath so
// they end up inside the env folder. A binary cannot cd its parent shell, so
// we exec a child shell instead — `exit` returns them.
//
// Gated on stdout being an *os.File terminal so tests and pipelines (which
// pass a bytes.Buffer or redirect) keep the plain print-and-return behavior.
func EnterShell(stdout io.Writer, envPath string) error {
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

// assertOpenable verifies ticketID exists and is a bead that may own an
// environment. Step-beads (those carrying the finish-step label) are internal
// bookkeeping for the step-driven finish path and have no env folder, so
// opening one is always a mistake — it is refused here, before any directory is
// created, so a stray `this env <step-id>` cannot litter envs/ with an orphan.
func assertOpenable(db, beadsDir, ticketID string) error {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "show", ticketID).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return fmt.Errorf("ticket lookup failed for %s: %v: %s", ticketID, err, ee.Stderr)
		}
		return fmt.Errorf("ticket lookup failed for %s: %w", ticketID, err)
	}
	var rows []struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return fmt.Errorf("parse bd show %s: %w", ticketID, err)
	}
	if len(rows) == 0 {
		return fmt.Errorf("ticket not found: %s", ticketID)
	}
	if slices.Contains(rows[0].Labels, stepLabel) {
		return fmt.Errorf("%s is a finish-step bead (label %q); step-beads have no environment", ticketID, stepLabel)
	}
	return nil
}
