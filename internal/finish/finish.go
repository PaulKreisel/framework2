// Package finish implements the `ticket finish` subcommand: the explicit,
// one-way wrap-up at the end of a ticket session.
//
// finish is NOT invoked automatically by anything else: it is destructive
// (closes the bead, removes worktrees, deletes the env folder) and must
// only fire on explicit user action.
//
// The flow is:
//
//  1. Pre-flight every worktree this ticket has linked. Refuse if any has
//     uncommitted changes (the user should commit first with plain git)
//     or is on a detached HEAD.
//  2. For each worktree, fast-forward-rebase the bead's branch onto its
//     parent ticket's branch in the same bare repo. If a clean FF rebase
//     is not possible (conflicts, divergent history), abort with a clear
//     error rather than dropping the user into an interactive rebase.
//  3. `bd close <id>`.
//  4. Remove each worktree from its bare repo (`git worktree remove`).
//  5. Delete the on-disk env folder at <beadsDir>/envs/<id>.
package finish

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"framework2/internal/dir"
)

// Run executes the finish flow for ticketID. beadsDir must be set so the env
// folder can be located; db is the optional bd database path forwarded to
// `bd` invocations.
func Run(db, beadsDir, ticketID string, stdout io.Writer) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	if beadsDir == "" {
		return errors.New("BEADS_DIR not set: cannot derive env path")
	}

	entries, err := dir.Load(db, ticketID)
	if err != nil {
		return err
	}
	worktrees := worktreeEntries(entries)

	if err := preflight(worktrees); err != nil {
		return err
	}

	parent, err := dir.ParentID(db, ticketID)
	if err != nil {
		return err
	}
	if err := rebaseWorktrees(db, ticketID, parent, worktrees); err != nil {
		return err
	}

	if err := bdClose(db, ticketID); err != nil {
		return err
	}
	for _, e := range worktrees {
		if err := gitWorktreeRemove(e.BareRepo, e.Path); err != nil {
			return err
		}
	}
	envPath := filepath.Join(beadsDir, "envs", ticketID)
	if err := os.RemoveAll(envPath); err != nil {
		return fmt.Errorf("remove env folder %s: %w", envPath, err)
	}
	fmt.Fprintf(stdout, "finished %s\n", ticketID)
	return nil
}

// worktreeEntries returns just the entries that were created by
// `ticket dir worktree` (those have a BareRepo set). Plain `dir link`
// entries don't participate in finish.
func worktreeEntries(entries []dir.Entry) []dir.Entry {
	var out []dir.Entry
	for _, e := range entries {
		if e.BareRepo != "" {
			out = append(out, e)
		}
	}
	return out
}

func preflight(worktrees []dir.Entry) error {
	for _, e := range worktrees {
		branch, ok := runGit(e.Path, "rev-parse", "--abbrev-ref", "HEAD")
		if !ok || branch == "HEAD" {
			return fmt.Errorf("worktree %s is on a detached HEAD: commit and check out a branch before finishing", e.Path)
		}
		status, ok := runGit(e.Path, "status", "--porcelain")
		if !ok {
			return fmt.Errorf("worktree %s: git status failed", e.Path)
		}
		if status != "" {
			return fmt.Errorf("worktree %s has uncommitted changes:\n%s\ncommit them first, then re-run ticket finish", e.Path, status)
		}
	}
	return nil
}

// rebaseWorktrees fast-forward-rebases each worktree's branch onto its
// parent ticket's branch in the same bare repo. A root bead (no parent)
// or a bead whose parent has no worktree in the same bare repo is left
// alone.
func rebaseWorktrees(db, ticketID, parent string, worktrees []dir.Entry) error {
	if parent == "" {
		return nil
	}
	parentEntries, err := dir.Load(db, parent)
	if err != nil {
		return err
	}
	for _, e := range worktrees {
		pe, ok := findEntryForBare(parentEntries, e.BareRepo)
		if !ok {
			// Parent doesn't have a worktree off this bare — nothing to
			// rebase onto. Treat as a no-op for this bare.
			continue
		}
		if err := ffRebase(e.Path, pe.Branch); err != nil {
			return err
		}
	}
	return nil
}

// ffRebase rebases the current branch in wt onto upstream, refusing
// anything that isn't a clean fast-forward. The intent is the same as
// `git pull --ff-only` against an upstream branch: catch up the bead's
// branch to its parent's tip when nothing has diverged, and bail out
// loudly when it has.
func ffRebase(wt, upstream string) error {
	headSHA, ok := runGit(wt, "rev-parse", "HEAD")
	if !ok {
		return fmt.Errorf("rev-parse HEAD failed in %s", wt)
	}
	upstreamSHA, ok := runGit(wt, "rev-parse", upstream)
	if !ok {
		return fmt.Errorf("rev-parse %s failed in %s", upstream, wt)
	}
	if headSHA == upstreamSHA {
		return nil
	}
	// merge-base --is-ancestor exits 0 iff <commit> is an ancestor of <other>.
	if err := exec.Command("git", "-C", wt, "merge-base", "--is-ancestor", headSHA, upstreamSHA).Run(); err == nil {
		// HEAD is behind upstream: a plain fast-forward catches it up.
		return runGitChecked(wt, "merge", "--ff-only", upstream)
	}
	if err := exec.Command("git", "-C", wt, "merge-base", "--is-ancestor", upstreamSHA, headSHA).Run(); err == nil {
		// HEAD is already ahead of upstream — nothing to do.
		return nil
	}
	return fmt.Errorf("worktree %s: branch has diverged from parent %s — cannot fast-forward; rebase or merge manually before ticket finish",
		wt, upstream)
}

func findEntryForBare(entries []dir.Entry, bare string) (dir.Entry, bool) {
	for _, e := range entries {
		if e.BareRepo == bare {
			return e, true
		}
	}
	return dir.Entry{}, false
}

func bdClose(db, ticketID string) error {
	args := []string{}
	if db != "" {
		args = append(args, "--db", db)
	}
	args = append(args, "close", ticketID)
	cmd := exec.Command("bd", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd close %s: %w: %s", ticketID, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitWorktreeRemove(bare, wt string) error {
	cmd := exec.Command("git", "-C", bare, "worktree", "remove", wt)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree remove %s (bare %s): %w: %s",
			wt, bare, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runGit(path string, args ...string) (string, bool) {
	full := append([]string{"-C", path}, args...)
	out, err := exec.Command("git", full...).Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

func runGitChecked(path string, args ...string) error {
	full := append([]string{"-C", path}, args...)
	cmd := exec.Command("git", full...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s in %s: %w: %s",
			strings.Join(args, " "), path, err, strings.TrimSpace(string(out)))
	}
	return nil
}
