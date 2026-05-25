package dir

import (
	"fmt"
	"os/exec"
	"strings"
)

func gitToplevel(path string) (string, bool) {
	return runGit(path, "rev-parse", "--show-toplevel")
}

func gitBranch(path string) (string, bool) {
	return runGit(path, "rev-parse", "--abbrev-ref", "HEAD")
}

func gitRemote(path string) (string, bool) {
	return runGit(path, "config", "remote.origin.url")
}

func runGit(path string, args ...string) (string, bool) {
	full := append([]string{"-C", path}, args...)
	out, err := exec.Command("git", full...).Output()
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "", false
	}
	return s, true
}

// gitIsRepo reports whether the path is recognised by git as a repository
// (bare or otherwise).
func gitIsRepo(path string) bool {
	_, ok := runGit(path, "rev-parse", "--git-dir")
	return ok
}

// gitDefaultBase returns the branch ref a fresh worktree on this repo should
// be based on when there is no parent worktree to inherit from. Tries
// origin/HEAD first; falls back to local main/master.
func gitDefaultBase(repo string) (string, error) {
	if ref, ok := runGit(repo, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); ok {
		return ref, nil
	}
	for _, b := range []string{"main", "master"} {
		if _, ok := runGit(repo, "rev-parse", "--verify", "refs/heads/"+b); ok {
			return b, nil
		}
	}
	return "", fmt.Errorf("cannot determine base branch in %s: no origin/HEAD and no local main/master", repo)
}

// gitWorktreeAdd creates a new worktree at wtPath with a new branch named
// branch. If origin/<branch> exists (after a best-effort fetch from origin),
// the new branch tracks it; otherwise the new branch is created based on
// base. Returns the combined git output on failure.
func gitWorktreeAdd(repo, wtPath, branch, base string) error {
	// Best-effort fetch of the target branch (into refs/remotes/origin/) and
	// the base ref from origin so we can detect a pre-existing remote
	// branch. Ignore failures (offline, no origin configured, branch
	// doesn't exist on remote, ...). An explicit refspec is used for the
	// branch so this works in bare repos (where the default fetch refspec
	// targets refs/heads/* rather than refs/remotes/origin/*).
	_ = exec.Command("git", "-C", repo, "fetch", "origin",
		"+refs/heads/"+branch+":refs/remotes/origin/"+branch).Run()
	baseRef := strings.TrimPrefix(base, "origin/")
	_ = exec.Command("git", "-C", repo, "fetch", "origin", baseRef).Run()

	_, hasLocal := runGit(repo, "rev-parse", "--verify", "refs/heads/"+branch)
	_, hasRemote := runGit(repo, "rev-parse", "--verify", "refs/remotes/origin/"+branch)

	addBase := base
	if !hasLocal && hasRemote {
		addBase = "refs/remotes/origin/" + branch
	}
	cmd := exec.Command("git", "-C", repo, "worktree", "add", "-b", branch, wtPath, addBase)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if !hasLocal && hasRemote {
		// Set upstream manually. `git worktree add --track` requires that
		// refs/remotes/origin/* be recognised as remote-tracking via the
		// remote's fetch refspec, which is not the case in a bare clone
		// (default refspec is +refs/heads/*:refs/heads/*). Writing the
		// config keys directly works regardless of refspec config.
		_ = exec.Command("git", "-C", repo, "config", "branch."+branch+".remote", "origin").Run()
		_ = exec.Command("git", "-C", repo, "config", "branch."+branch+".merge", "refs/heads/"+branch).Run()
	}
	return nil
}

// gitWorktreeAddExisting creates a worktree at wtPath that checks out the
// pre-existing branch. Fetches it from origin best-effort, then uses the
// local branch if present, otherwise creates one tracking the remote ref.
// Errors if the branch is found in neither place.
func gitWorktreeAddExisting(repo, wtPath, branch string) error {
	_ = exec.Command("git", "-C", repo, "fetch", "origin",
		"+refs/heads/"+branch+":refs/remotes/origin/"+branch).Run()

	_, hasLocal := runGit(repo, "rev-parse", "--verify", "refs/heads/"+branch)
	_, hasRemote := runGit(repo, "rev-parse", "--verify", "refs/remotes/origin/"+branch)

	var cmd *exec.Cmd
	switch {
	case hasLocal:
		cmd = exec.Command("git", "-C", repo, "worktree", "add", wtPath, branch)
	case hasRemote:
		cmd = exec.Command("git", "-C", repo, "worktree", "add", "-b", branch, wtPath, "refs/remotes/origin/"+branch)
	default:
		return fmt.Errorf("branch %q not found locally or on origin in %s", branch, repo)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if !hasLocal && hasRemote {
		_ = exec.Command("git", "-C", repo, "config", "branch."+branch+".remote", "origin").Run()
		_ = exec.Command("git", "-C", repo, "config", "branch."+branch+".merge", "refs/heads/"+branch).Run()
	}
	return nil
}

