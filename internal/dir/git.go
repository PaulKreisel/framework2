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
// branch, based on base. Returns the combined git output on failure.
func gitWorktreeAdd(repo, wtPath, branch, base string) error {
	cmd := exec.Command("git", "-C", repo, "worktree", "add", "-b", branch, wtPath, base)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git worktree add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

