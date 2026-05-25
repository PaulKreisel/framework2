package dir

import (
	"fmt"
	"os"
	"path/filepath"

	"framework2/internal/projectconfig"
	"framework2/internal/slug"
)

func absBare(bareRepo string) (string, error) {
	bare, err := filepath.Abs(bareRepo)
	if err != nil {
		return "", fmt.Errorf("resolve bare repo path: %w", err)
	}
	return bare, nil
}

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
	bare, err := absBare(bareRepo)
	if err != nil {
		return err
	}
	if !gitIsRepo(bare) {
		return fmt.Errorf("not a git repository: %s", bare)
	}
	cfg, err := projectconfig.Load(beadsDir)
	if err != nil {
		return err
	}
	return withWorktreeLock(beadsDir, func() error {
		_, err := ensureWorktree(db, beadsDir, ticketID, bare, cfg)
		return err
	})
}

// ensureWorktree returns the branch name to use as the base for descendants.
func ensureWorktree(db, beadsDir, ticketID, bare string, cfg projectconfig.Config) (string, error) {
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
		base, err = ensureWorktree(db, beadsDir, parent, bare, cfg)
		if err != nil {
			return "", err
		}
	}

	info, err := slug.Get(db, beadsDir, ticketID)
	if err != nil {
		return "", err
	}
	jiraID, err := AncestorJiraID(db, beadsDir, ticketID)
	if err != nil {
		return "", err
	}
	vars := projectconfig.Vars{
		BeadID:       ticketID,
		JiraID:       jiraID,
		Slug:         info.Slug,
		Type:         row.IssueType,
		BareRepoPath: bare,
	}
	branch, err := cfg.RenderBranch(vars)
	if err != nil {
		return "", fmt.Errorf("render branch template: %w", err)
	}
	vars.Branch = branch
	wtPath, err := cfg.RenderPath(vars)
	if err != nil {
		return "", fmt.Errorf("render worktree path template: %w", err)
	}
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

// WorktreeForBranch creates a worktree on bareRepo that checks out the given
// pre-existing branch (no template-derived branch name, no new branch off
// base). Used by `jira --discover-branches` to attach worktrees for the exact
// branches GitHub reports for a Jira key.
//
// Idempotent on (bare, branch): if the ticket already has an entry pointing
// at bareRepo with this branch, returns without error.
func WorktreeForBranch(db, beadsDir, ticketID, bareRepo, branch string) error {
	if branch == "" {
		return fmt.Errorf("WorktreeForBranch: empty branch")
	}
	bare, err := absBare(bareRepo)
	if err != nil {
		return err
	}
	if !gitIsRepo(bare) {
		return fmt.Errorf("not a git repository: %s", bare)
	}
	cfg, err := projectconfig.Load(beadsDir)
	if err != nil {
		return err
	}
	return withWorktreeLock(beadsDir, func() error {
		entries, err := load(db, beadsDir, ticketID)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.BareRepo == bare && e.Branch == branch {
				return nil
			}
		}
		row, err := showTicket(db, beadsDir, ticketID)
		if err != nil {
			return err
		}
		info, err := slug.Get(db, beadsDir, ticketID)
		if err != nil {
			return err
		}
		jiraID, err := AncestorJiraID(db, beadsDir, ticketID)
		if err != nil {
			return err
		}
		vars := projectconfig.Vars{
			BeadID:       ticketID,
			JiraID:       jiraID,
			Slug:         info.Slug,
			Type:         row.IssueType,
			BareRepoPath: bare,
			Branch:       branch,
		}
		wtPath, err := cfg.RenderPath(vars)
		if err != nil {
			return fmt.Errorf("render worktree path template: %w", err)
		}
		if _, err := os.Stat(wtPath); err == nil {
			return fmt.Errorf("worktree path already exists: %s", wtPath)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat worktree path: %w", err)
		}
		if err := gitWorktreeAddExisting(bare, wtPath, branch); err != nil {
			return err
		}
		if err := link(db, beadsDir, ticketID, wtPath, bare); err != nil {
			return err
		}
		if err := slug.MarkUsed(db, beadsDir, ticketID); err != nil {
			return err
		}
		return nil
	})
}

func findEntryForBare(entries []Entry, bare string) (Entry, bool) {
	for _, e := range entries {
		if e.BareRepo == bare {
			return e, true
		}
	}
	return Entry{}, false
}
