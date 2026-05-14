// Package env implements the `ticket env` subcommand: creating an on-disk
// environment directory for a ticket, with a .ticket-id marker that the
// companion `this` command discovers via cwd walk-up.
package env

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MarkerFile is the basename of the per-env file containing the ticket id.
const MarkerFile = ".ticket-id"

// Create makes (or refreshes) an env folder for ticketID and writes a
// .ticket-id marker inside it. The destination is rootOverride/<id>/ when
// rootOverride is non-empty, otherwise beadsDir/envs/<id>/.
//
// Idempotent: re-running with the same id is a no-op success. The ticket is
// looked up via `bd show` first to fail fast on typos.
func Create(db, beadsDir, ticketID, rootOverride string) (string, error) {
	if ticketID == "" {
		return "", errors.New("ticket id required")
	}
	if err := assertTicketExists(db, ticketID); err != nil {
		return "", err
	}
	root := rootOverride
	if root == "" {
		if beadsDir == "" {
			return "", errors.New("BEADS_DIR not set: cannot derive env root (pass --root, or set BEADS_DIR)")
		}
		root = filepath.Join(beadsDir, "envs")
	}
	envPath := filepath.Join(root, ticketID)
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

func assertTicketExists(db, ticketID string) error {
	args := []string{}
	if db != "" {
		args = append(args, "--db", db)
	}
	args = append(args, "show", ticketID)
	out, err := exec.Command("bd", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ticket lookup failed for %s: %v: %s", ticketID, err, out)
	}
	return nil
}
