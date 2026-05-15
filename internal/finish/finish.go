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
//
// If continueMode is true, Run expects at least one linked worktree to have
// a paused rebase (left over from a prior `Run` whose rebase hit conflicts);
// it calls `git rebase --continue` in each such worktree and then completes
// the rest of the flow.
func Run(db, beadsDir, ticketID string, continueMode bool, stdout io.Writer) error {
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

	if err := preflight(worktrees, continueMode); err != nil {
		return err
	}

	parent, err := dir.ParentID(db, ticketID)
	if err != nil {
		return err
	}
	if err := rebaseWorktrees(db, ticketID, parent, worktrees, continueMode); err != nil {
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

func preflight(worktrees []dir.Entry, continueMode bool) error {
	anyInRebase := false
	for _, e := range worktrees {
		// Check rebase state FIRST: during a paused rebase HEAD is naturally
		// detached, which would otherwise trip the detached-HEAD check below.
		if inProgressRebase(e.Path) {
			anyInRebase = true
			if !continueMode {
				return fmt.Errorf("worktree %s has a rebase in progress; resolve conflicts then run `ticket finish --continue` (or `git rebase --abort` to undo)", e.Path)
			}
			// Skip the detached-HEAD and clean-tree checks: an in-rebase
			// worktree is legitimately detached and may have staged conflict
			// resolutions that appear in `git status`.
			continue
		}
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
	if continueMode && !anyInRebase {
		return errors.New("no rebase in progress in any linked worktree; `ticket finish --continue` needs a paused rebase to resume")
	}
	return nil
}

// inProgressRebase reports whether wt is in the middle of a `git rebase`
// (presence of rebase-merge/rebase-apply state inside its git dir).
func inProgressRebase(wt string) bool {
	gitDir, ok := runGit(wt, "rev-parse", "--git-dir")
	if !ok {
		return false
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(wt, gitDir)
	}
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gitDir, name)); err == nil {
			return true
		}
	}
	return false
}

// rebaseWorktrees brings each worktree's branch onto its parent ticket's
// branch in the same bare repo, then fast-forwards the parent worktree's
// branch to match. A root bead (no parent) or a bead whose parent has no
// worktree in the same bare repo is left alone. In continueMode, in-rebase
// worktrees resume via `git rebase --continue`; non-in-rebase worktrees
// take the normal path.
func rebaseWorktrees(db, ticketID, parent string, worktrees []dir.Entry, continueMode bool) error {
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
		if err := ffRebase(e.Path, pe.Branch, continueMode); err != nil {
			return err
		}
		if err := ffAdvance(pe.Path, pe.Branch, e.Branch); err != nil {
			return err
		}
	}
	return nil
}

// ffAdvance fast-forwards the parent worktree's checked-out branch
// (expected to be parentBranch) to the tip of childBranch. After the
// ticket branch has been FF-rebased onto the parent, the parent branch
// is by construction an ancestor of the ticket branch, so a plain
// fast-forward catches it up. If the parent worktree is on a different
// branch, detached, or the FF would not be clean (e.g. the parent
// branch was advanced concurrently), bail out rather than fall back to
// a merge — same posture as ffRebase.
func ffAdvance(parentWT, parentBranch, childBranch string) error {
	childSHA, ok := runGit(parentWT, "rev-parse", childBranch)
	if !ok {
		return fmt.Errorf("rev-parse %s failed in %s", childBranch, parentWT)
	}
	parentSHA, ok := runGit(parentWT, "rev-parse", "HEAD")
	if !ok {
		return fmt.Errorf("rev-parse HEAD failed in %s", parentWT)
	}
	if parentSHA == childSHA {
		return nil
	}
	curBranch, ok := runGit(parentWT, "rev-parse", "--abbrev-ref", "HEAD")
	if !ok || curBranch == "HEAD" {
		return fmt.Errorf("parent worktree %s is on a detached HEAD: cannot fast-forward %s", parentWT, parentBranch)
	}
	if curBranch != parentBranch {
		return fmt.Errorf("parent worktree %s is on branch %s, expected %s — cannot fast-forward", parentWT, curBranch, parentBranch)
	}
	return runGitChecked(parentWT, "merge", "--ff-only", childBranch)
}

// ffRebase brings the current branch in wt up to upstream. In normal mode:
//
//   - identical → no-op
//   - HEAD behind upstream → fast-forward merge
//   - HEAD ahead of upstream → no-op (already includes upstream)
//   - diverged → real `git rebase upstream`; on conflict, leave the rebase
//     paused in the worktree so the user can resolve and resume with
//     `ticket finish --continue`.
//
// In continueMode, wt is expected to be in the middle of a paused rebase
// (preflight enforced this for at least one worktree). `git rebase
// --continue` is invoked; a worktree without an in-progress rebase is a
// no-op (one bare may have rebased cleanly while another still needs to
// resume).
func ffRebase(wt, upstream string, continueMode bool) error {
	if continueMode {
		if !inProgressRebase(wt) {
			return nil
		}
		cmd := exec.Command("git", "-C", wt, "rebase", "--continue")
		cmd.Env = append(os.Environ(), "GIT_EDITOR=true", "GIT_SEQUENCE_EDITOR=true")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("worktree %s: rebase --continue failed; resolve remaining conflicts (and `git add` them) before re-running:\n%s",
				wt, strings.TrimSpace(string(out)))
		}
		return nil
	}

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
	// Diverged. Try a real rebase; on conflict, leave the rebase paused so
	// the user can resolve and resume with `ticket finish --continue`.
	cmd := exec.Command("git", "-C", wt, "rebase", upstream)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("worktree %s: rebase onto %s hit conflicts; resolve them (and `git add` resolved files), then run `ticket finish --continue` (or `git rebase --abort` to undo):\n%s",
			wt, upstream, strings.TrimSpace(string(out)))
	}
	return nil
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
