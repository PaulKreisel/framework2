// Package teardown implements `ticket teardown`: dismantle a ticket's
// on-disk footprint without landing anything. Every linked worktree is
// removed (uncommitted changes included, via --force), its dirs entry is
// unlinked, the bead is closed if still open, and the env folder is
// deleted. Branches are NOT deleted from the bare repo, so committed work
// stays recoverable.
//
// Unlike finish there is no rebase, no PR check, no pipeline run and no
// follow-up launch — teardown is for abandoning or archiving work. It is
// idempotent and also accepts already-closed beads, so it can sweep stale
// environments in bulk.
package teardown

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"framework2/internal/bdcli"
	"framework2/internal/dir"
	"framework2/internal/project"
)

// Run tears down ticketID. Per-worktree failures are reported on stdout and
// do not stop the sweep; the first hard failure is returned at the end so a
// bulk caller still sees a non-zero exit.
func Run(db, beadsDir, ticketID string, stdout io.Writer) error {
	status, err := beadStatus(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	entries, err := dir.Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	var firstErr error
	removed := 0
	for _, e := range entries {
		if e.BareRepo == "" {
			continue
		}
		if err := removeWorktree(e.BareRepo, e.Path); err != nil {
			fmt.Fprintf(stdout, "warn: %s: %v\n", ticketID, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		removed++
		if err := dir.Remove(db, beadsDir, ticketID, e.Path); err != nil {
			fmt.Fprintf(stdout, "warn: unlink %s from %s: %v\n", e.Path, ticketID, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if status != "closed" {
		if err := bdClose(db, beadsDir, ticketID); err != nil {
			return err
		}
	}
	envPath := project.Path(beadsDir, filepath.Join("envs", ticketID))
	if err := os.RemoveAll(envPath); err != nil {
		return fmt.Errorf("remove env folder %s: %w", envPath, err)
	}
	fmt.Fprintf(stdout, "teardown %s: %d worktree(s) removed\n", ticketID, removed)
	return firstErr
}

// removeWorktree force-removes wt from bare. A worktree whose directory is
// already gone is pruned from the bare's registry and treated as success.
func removeWorktree(bare, wt string) error {
	if _, err := os.Stat(wt); os.IsNotExist(err) {
		// Directory already gone; drop the stale registration if any.
		_ = exec.Command("git", "-C", bare, "worktree", "prune").Run()
		return nil
	}
	cmd := exec.Command("git", "-C", bare, "worktree", "remove", "--force", wt)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		// Not registered (e.g. removed manually, or linked to a different
		// bare after a repo move): nothing left to do for git.
		if strings.Contains(msg, "is not a working tree") {
			return nil
		}
		return fmt.Errorf("git worktree remove --force %s (bare %s): %w: %s", wt, bare, err, msg)
	}
	return nil
}

func beadStatus(db, beadsDir, ticketID string) (string, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "show", ticketID).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("look up ticket %s: %v: %s", ticketID, err, ee.Stderr)
		}
		return "", fmt.Errorf("look up ticket %s: %w", ticketID, err)
	}
	var rows []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return "", fmt.Errorf("parse ticket store output: %w", err)
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("ticket not found: %s", ticketID)
	}
	return rows[0].Status, nil
}

func bdClose(db, beadsDir, ticketID string) error {
	if out, err := bdcli.Cmd(beadsDir, db, "close", ticketID).CombinedOutput(); err != nil {
		return fmt.Errorf("bd close %s: %w: %s", ticketID, err, strings.TrimSpace(string(out)))
	}
	return nil
}
