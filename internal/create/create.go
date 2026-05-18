// Package create implements the `ticket create child|followup|sibling`
// subcommands: spawning a new bead related to the current one and printing
// its id.
package create

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	"framework2/internal/bdcli"
	"framework2/internal/slug"
)

// Relation selects how the new bead is wired relative to the current one.
type Relation int

const (
	// Child: new bead is a child of current (current parents new).
	Child Relation = iota
	// Followup: new bead shares current's parent (top-level if current has
	// none). By default current blocks new; pass blocked=false to skip the
	// dep (i.e. a plain sibling).
	Followup
)

// Run creates a new bead related to currentID by rel with the given title and
// prints the new bead's id to stdout. db may be empty to use bd's default.
// blocked is only consulted for Followup. description becomes the new bead's
// initial description (and thus seeds the env folder's description.md); it
// must be non-empty.
func Run(db, beadsDir, currentID, title, description string, rel Relation, blocked bool, stdout io.Writer) error {
	if currentID == "" {
		return errors.New("ticket id required")
	}
	if title == "" {
		return errors.New("title required")
	}
	if description == "" {
		return errors.New("description required")
	}

	var parent string
	switch rel {
	case Child:
		parent = currentID
	case Followup:
		p, err := loadParent(db, beadsDir, currentID)
		if err != nil {
			return err
		}
		parent = p
	default:
		return fmt.Errorf("unknown relation: %d", rel)
	}

	initialSlug := slug.Derive(title, slug.MaxLen)

	args := []string{"create", title, "--silent", "--description", description}
	if parent != "" {
		args = append(args, "--parent", parent)
	}
	if initialSlug != "" {
		meta, _ := json.Marshal(map[string]string{"slug": initialSlug})
		args = append(args, "--metadata", string(meta))
	}
	out, err := bdcli.Cmd(beadsDir, db, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return fmt.Errorf("bd create: %v: %s", err, ee.Stderr)
		}
		return fmt.Errorf("bd create: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return errors.New("bd create returned empty id")
	}

	if rel == Followup && blocked {
		if dout, err := bdcli.Cmd(beadsDir, db, "dep", "add", id, currentID).CombinedOutput(); err != nil {
			return fmt.Errorf("bd dep add %s %s: %v: %s", id, currentID, err, dout)
		}
	}

	spawnSlugRefine(beadsDir, db, id)

	_, werr := fmt.Fprintln(stdout, id)
	return werr
}

// spawnSlugRefine fires a detached `ticket dir slug --ai <id>` subprocess that
// asks a tiny Claude Haiku model for a better slug and writes it back if the
// slug has not yet been frozen. Best-effort: any spawn error is swallowed, the
// bead already has a fallback slug from [slug.Derive] either way.
//
// We prefer the `ticket` binary because it accepts the bead id positionally,
// independent of any .ticket-id marker the parent shell may sit inside (which
// points at a different bead than the one we just created).
func spawnSlugRefine(beadsDir, db, id string) {
	// `go test` shares a bd database across tests in a package; a refinement
	// process surviving the test that spawned it could mutate another test's
	// state. Skip the spawn under `testing.Testing()` — production paths are
	// unaffected.
	if testing.Testing() {
		return
	}
	exe, err := exec.LookPath("ticket")
	if err != nil {
		return
	}
	args := []string{}
	if db != "" {
		args = append(args, "--db", db)
	}
	args = append(args, "dir", "slug", "--ai", id)
	cmd := exec.Command(exe, args...)
	if beadsDir != "" {
		cmd.Env = append(os.Environ(), "BEADS_DIR="+beadsDir)
	}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Start()
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
}

// loadParent returns the parent id of ticketID, or "" if it has none.
// Surfaces the first parent if multiple are recorded (bd allows this).
func loadParent(db, beadsDir, ticketID string) (string, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "show", ticketID).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("bd show %s: %v: %s", ticketID, err, ee.Stderr)
		}
		return "", fmt.Errorf("bd show %s: %w", ticketID, err)
	}
	var rows []struct {
		Parent string `json:"parent"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return "", fmt.Errorf("parse bd show output for %s: %w", ticketID, err)
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].Parent, nil
}
