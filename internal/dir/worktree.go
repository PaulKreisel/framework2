package dir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"framework2/internal/projectconfig"
	"framework2/internal/repoclone"
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
	cfg, err := projectconfig.Load(beadsDir)
	if err != nil {
		return err
	}
	if err := repoclone.EnsureBareFromPath(bare, cfg.SetupScript); err != nil {
		return err
	}
	if !gitIsRepo(bare) {
		return fmt.Errorf("not a git repository: %s", bare)
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
	e, ok, err := findEntryForBare(entries, bare)
	if err != nil {
		return "", err
	}
	if ok {
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
// branch (no template-derived branch name, no parent cascade). Used by
// `jira --discover-branches` to attach worktrees for the exact branches GitHub
// reports for a Jira key, and by `dir worktree --branch` to attach an explicit
// branch.
//
// When createMissing is true and the branch exists neither locally nor on
// origin, a fresh branch is created off the repo's default base (this is the
// `dir worktree --branch` behavior). When false, a missing branch is an error
// (the `jira --discover-branches` branches are reported by GitHub, so a missing
// one is a genuine anomaly).
//
// Idempotent on (bare, branch): if the ticket already has an entry pointing
// at bareRepo with this branch, returns without error.
func WorktreeForBranch(db, beadsDir, ticketID, bareRepo, branch string, createMissing bool) error {
	if branch == "" {
		return fmt.Errorf("WorktreeForBranch: empty branch")
	}
	bare, err := absBare(bareRepo)
	if err != nil {
		return err
	}
	cfg, err := projectconfig.Load(beadsDir)
	if err != nil {
		return err
	}
	if err := repoclone.EnsureBareFromPath(bare, cfg.SetupScript); err != nil {
		return err
	}
	if !gitIsRepo(bare) {
		return fmt.Errorf("not a git repository: %s", bare)
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
		if err := gitWorktreeAddExisting(bare, wtPath, branch, createMissing); err != nil {
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

// FFResult records the outcome of one fast-forward attempt for a worktree.
type FFResult struct {
	Path         string // absolute worktree path
	ParentBranch string // parent bead's branch we tried to merge
	Advanced     bool   // branch was moved to parent's tip
	Skipped      bool   // FF not clean; caller should suggest manual rebase
}

// FastForwardWorktrees attempts a fast-forward-only merge of each of the
// bead's worktrees onto the parent bead's branch tip in the same bare repo.
// It is a no-op when the bead has no parent, when the parent has no worktree
// for a given bare, or when the ambiguity rule (findEntryForBare) applies.
// Only FFResults with Advanced=true or Skipped=true are returned; up-to-date
// worktrees are omitted. The caller is responsible for logging.
func FastForwardWorktrees(db, beadsDir, ticketID string) ([]FFResult, error) {
	row, err := showTicket(db, beadsDir, ticketID)
	if err != nil {
		return nil, err
	}
	if row.Parent == "" {
		return nil, nil
	}
	childEntries, err := load(db, beadsDir, ticketID)
	if err != nil {
		return nil, err
	}
	parentEntries, err := load(db, beadsDir, row.Parent)
	if err != nil {
		return nil, err
	}
	var results []FFResult
	for _, ce := range childEntries {
		if ce.BareRepo == "" {
			continue
		}
		pe, ok, err := findEntryForBare(parentEntries, ce.BareRepo)
		if err != nil || !ok {
			continue
		}
		advanced, skipped := gitFastForwardOnto(ce.Path, pe.Branch)
		if advanced || skipped {
			results = append(results, FFResult{
				Path:         ce.Path,
				ParentBranch: pe.Branch,
				Advanced:     advanced,
				Skipped:      skipped,
			})
		}
	}
	return results, nil
}

// findEntryForBare returns the single worktree entry created off bare. It
// reports ok=false when there is no such worktree. If more than one worktree
// is linked to the same bare (e.g. several branches attached via
// `jira --discover-branches`), the base branch is ambiguous, so it returns an
// error rather than silently picking one — mirrors the "two branches" rule in
// finish.
func findEntryForBare(entries []Entry, bare string) (Entry, bool, error) {
	var matches []Entry
	for _, e := range entries {
		if e.BareRepo == bare {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 0:
		return Entry{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		branches := make([]string, len(matches))
		for i, m := range matches {
			branches[i] = m.Branch
		}
		return Entry{}, false, fmt.Errorf(
			"%d worktrees linked to %s (branches: %s); cannot determine which to base on",
			len(matches), bare, strings.Join(branches, ", "))
	}
}
