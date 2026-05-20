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

// DefaultBranch returns the local short branch name (e.g. "main") that this
// repo treats as its default. Mirrors gitDefaultBase but strips any
// "origin/" prefix so the result is directly comparable against branch
// names recorded in dir.Entry.
func DefaultBranch(repo string) (string, error) {
	base, err := gitDefaultBase(repo)
	if err != nil {
		return "", err
	}
	if i := strings.IndexByte(base, '/'); i >= 0 {
		return base[i+1:], nil
	}
	return base, nil
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

// AgentSessionNotesRef is the notes ref used by `this finish` to record
// per-commit session/ticket attribution. Held here in the `dir` package so
// the worktree config helper can wire fetch up without importing `finish`
// (which depends on this package).
const AgentSessionNotesRef = "refs/notes/agent-session"

// ensureNotesFetchRefspec adds
//
//	+refs/notes/agent-session:refs/notes/agent-session
//
// to remote.<origin>.fetch in repo (a worktree or bare path) when origin
// exists and the refspec is not already configured. Idempotent: re-running
// is a no-op. Returns nil silently when the repo has no remotes — a
// notes-fetch refspec on a remote that doesn't exist would just be dead
// config.
func ensureNotesFetchRefspec(repo string) error {
	remote := defaultRemote(repo)
	if remote == "" {
		return nil
	}
	want := "+" + AgentSessionNotesRef + ":" + AgentSessionNotesRef
	existing, _ := exec.Command("git", "-C", repo, "config", "--get-all",
		"remote."+remote+".fetch").Output()
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == want {
			return nil
		}
	}
	cmd := exec.Command("git", "-C", repo, "config", "--add",
		"remote."+remote+".fetch", want)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git config --add remote.%s.fetch: %w: %s",
			remote, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// defaultRemote returns the remote name to attach the notes refspec to.
// Prefers "origin" when present, else falls back to the first remote listed
// by `git remote`. Returns "" when the repo has no remotes — typical for a
// local-only bare that exists only as a workspace.
func defaultRemote(repo string) string {
	out, err := exec.Command("git", "-C", repo, "remote").Output()
	if err != nil {
		return ""
	}
	remotes := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, r := range remotes {
		if strings.TrimSpace(r) == "origin" {
			return "origin"
		}
	}
	if len(remotes) > 0 {
		r := strings.TrimSpace(remotes[0])
		if r != "" {
			return r
		}
	}
	return ""
}
