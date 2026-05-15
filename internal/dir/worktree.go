package dir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"framework2/internal/slug"
)

// Worktree creates a git worktree in bareRepo for ticketID and registers it
// via Link. The new branch is named "<prefix>/<id>-<slug>" where prefix is
// derived from the bead's issue type (enh/fix/todo) and slug from the bead
// metadata; legacy beads without a slug fall back to "<prefix>/<id>". The
// base commit-ish cascades through the parent chain: if an ancestor already
// has a worktree of this bare repo, the new branch is based on that
// ancestor's branch; otherwise it is based on the bare's default branch
// (origin/HEAD, or local main/master).
//
// Cascading is recursive: if a parent does not yet have this bare linked,
// Worktree creates one for the parent first, then bases this bead's worktree
// on the parent's freshly created branch.
//
// Idempotent: if the ticket already has this bare repo linked, Worktree
// returns without error.
func Worktree(db, beadsDir, ticketID, bareRepo string) error {
	bare, err := filepath.Abs(bareRepo)
	if err != nil {
		return fmt.Errorf("resolve bare repo path: %w", err)
	}
	if !gitIsRepo(bare) {
		return fmt.Errorf("not a git repository: %s", bare)
	}
	_, err = ensureWorktree(db, beadsDir, ticketID, bare)
	return err
}

// ensureWorktree returns the branch name to use as the base for descendants.
func ensureWorktree(db, beadsDir, ticketID, bare string) (string, error) {
	entries, err := load(db, beadsDir, ticketID)
	if err != nil {
		return "", err
	}
	if e, ok := findEntryForBare(entries, bare); ok {
		return e.Branch, nil
	}

	row, err := showTicket(db, beadsDir, ticketID)
	if err != nil {
		return "", err
	}
	parent := row.Parent
	var base string
	if parent == "" {
		base, err = gitDefaultBase(bare)
		if err != nil {
			return "", err
		}
	} else {
		base, err = ensureWorktree(db, beadsDir, parent, bare)
		if err != nil {
			return "", err
		}
	}

	info, err := slug.Get(db, beadsDir, ticketID)
	if err != nil {
		return "", err
	}
	prefix := slug.BranchPrefix(row.IssueType)
	branch := slug.Branch(prefix, ticketID, info.Slug)
	wtPath := worktreePath(bare, ticketID, info.Slug)
	if _, err := os.Stat(wtPath); err == nil {
		return "", fmt.Errorf("worktree path already exists: %s", wtPath)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat worktree path: %w", err)
	}
	if err := gitWorktreeAdd(bare, wtPath, branch, base); err != nil {
		return "", err
	}
	if err := link(db, beadsDir, ticketID, wtPath, bare); err != nil {
		return "", err
	}
	if err := slug.MarkUsed(db, beadsDir, ticketID); err != nil {
		return "", err
	}
	return branch, nil
}

func findEntryForBare(entries []Entry, bare string) (Entry, bool) {
	for _, e := range entries {
		if e.BareRepo == bare {
			return e, true
		}
	}
	return Entry{}, false
}

// worktreePath derives a stable worktree location: a sibling of the bare repo
// named "<bare-basename-without-.git>-<bead-id>" with the slug appended when
// present ("…-<id>-<slug>"). Legacy beads without a slug get the historical
// id-only form.
func worktreePath(bare, ticketID, slugStr string) string {
	base := filepath.Base(bare)
	base = strings.TrimSuffix(base, ".git")
	name := base + "-" + ticketID
	if slugStr != "" {
		name = name + "-" + slugStr
	}
	return filepath.Join(filepath.Dir(bare), name)
}
