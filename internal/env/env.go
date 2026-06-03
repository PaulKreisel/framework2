// Package env implements the `ticket env` subcommand: creating an on-disk
// environment directory for a ticket, with a .ticket-id marker that the
// companion `this` command discovers via cwd walk-up.
package env

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"framework2/internal/bdcli"
	"framework2/internal/project"
)

// MarkerFile is the basename of the per-env file containing the ticket id.
const MarkerFile = ".ticket-id"

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
