// Package env implements the `ticket env` subcommand: creating an on-disk
// environment directory for a ticket, with a .ticket-id marker that the
// companion `this` command discovers via cwd walk-up.
package env

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"framework2/internal/bdcli"
	"framework2/internal/project"
)

// MarkerFile is the basename of the per-env file containing the ticket id.
const MarkerFile = ".ticket-id"

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
	if err := assertTicketExists(db, beadsDir, ticketID); err != nil {
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

func assertTicketExists(db, beadsDir, ticketID string) error {
	out, err := bdcli.Cmd(beadsDir, db, "show", ticketID).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ticket lookup failed for %s: %v: %s", ticketID, err, out)
	}
	return nil
}
