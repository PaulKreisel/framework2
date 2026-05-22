package handover

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"framework2/internal/dir"
)

// collectCode walks every (worktree, branch) source in the subtree, groups
// the resulting commits by bare-repo basename, and returns one RepoSection
// per repo. Commits that appear in more than one source (e.g. a finished
// child's commit FF-rebased onto the parent's branch) are deduplicated by
// SHA. Within a repo, commits are listed chronologically (git's
// --reverse order); repos are sorted by name for deterministic output.
func collectCode(db, beadsDir string, order []string) ([]RepoSection, error) {
	type repoState struct {
		Commits []Commit
		Seen    map[string]struct{}
	}
	repos := map[string]*repoState{}

	for _, id := range order {
		dirs, err := dir.Load(db, beadsDir, id)
		if err != nil {
			return nil, fmt.Errorf("load dirs for %s: %w", id, err)
		}
		if !hasWorktree(dirs) {
			continue
		}
		parentBranches, err := parentBranchesFor(db, beadsDir, id, dirs)
		if err != nil {
			return nil, fmt.Errorf("resolve parent branches for %s: %w", id, err)
		}
		for _, e := range dirs {
			if e.BareRepo == "" || e.Branch == "" {
				continue
			}
			commits, err := listCommits(e.Path, parentBranches[e.BareRepo], e.Branch)
			if err != nil {
				return nil, fmt.Errorf("git log for %s @ %s: %w", repoLabel(e.BareRepo), e.Branch, err)
			}
			label := repoLabel(e.BareRepo)
			st, ok := repos[label]
			if !ok {
				st = &repoState{Seen: map[string]struct{}{}}
				repos[label] = st
			}
			for _, c := range commits {
				if _, dup := st.Seen[c.SHA]; dup {
					continue
				}
				st.Seen[c.SHA] = struct{}{}
				st.Commits = append(st.Commits, c)
			}
		}
	}

	out := make([]RepoSection, 0, len(repos))
	for label, st := range repos {
		out = append(out, RepoSection{Repo: label, Commits: st.Commits})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo < out[j].Repo })
	return out, nil
}

// hasWorktree reports whether any entry was created via `dir worktree`
// (BareRepo set). Plain `dir link` entries don't participate in commits
// since they have no branch model.
func hasWorktree(entries []dir.Entry) bool {
	for _, e := range entries {
		if e.BareRepo != "" {
			return true
		}
	}
	return false
}

// parentBranchesFor returns one parent-branch-on-same-bare per worktree
// entry. The lookup walks the bd parent chain until it finds an ancestor
// that links the same bare repo; falls back to git's default branch on
// the bare when no ancestor matches.
func parentBranchesFor(db, beadsDir, ticketID string, entries []dir.Entry) (map[string]string, error) {
	out := map[string]string{}
	for _, e := range entries {
		if e.BareRepo == "" || e.Branch == "" {
			continue
		}
		branch, err := findParentBranch(db, beadsDir, ticketID, e.BareRepo)
		if err != nil {
			return nil, err
		}
		out[e.BareRepo] = branch
	}
	return out, nil
}

func findParentBranch(db, beadsDir, ticketID, bare string) (string, error) {
	cur := ticketID
	for {
		pid, err := dir.ParentID(db, beadsDir, cur)
		if err != nil {
			return "", err
		}
		if pid == "" {
			break
		}
		entries, err := dir.Load(db, beadsDir, pid)
		if err != nil {
			return "", err
		}
		for _, pe := range entries {
			if pe.BareRepo == bare && pe.Branch != "" {
				return pe.Branch, nil
			}
		}
		cur = pid
	}
	return defaultBranch(bare), nil
}

func defaultBranch(bare string) string {
	out, err := exec.Command("git", "-C", bare, "symbolic-ref", "--short", "HEAD").Output()
	if err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	return "main"
}

// listCommits returns the commits in base..branch evaluated from the
// worktree at wtPath. Each commit carries the full commit message; the
// bead_id field is left empty for now — the refs/notes/agent-session
// attribution mechanism was removed in ai-vrc.57.13, and ai-vrc.57.14
// (handover switches to the `Subtask N` commit trailer) will repopulate it.
func listCommits(wtPath, base, branch string) ([]Commit, error) {
	if wtPath == "" {
		return nil, fmt.Errorf("worktree path empty")
	}
	rng := branch
	if base != "" {
		rng = base + ".." + branch
	}
	// Record format: SHA \x1f message \x1e (one record per commit). %B
	// includes the full multi-line message; we use ASCII record/unit
	// separators so newlines inside the message are unambiguous.
	cmd := exec.Command("git", "-C", wtPath, "log",
		"--reverse",
		"--pretty=format:%H%x1f%B%x1e",
		rng,
	)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("git log %s: %v: %s", rng, err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git log %s: %w", rng, err)
	}
	var commits []Commit
	for _, rec := range strings.Split(string(out), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\x1f", 2)
		if len(parts) < 2 {
			continue
		}
		sha := parts[0]
		msg := strings.TrimRight(parts[1], "\n")
		commits = append(commits, Commit{
			SHA:     sha,
			Message: msg,
		})
	}
	return commits, nil
}

// repoLabel returns a portable name for the bare repo: basename minus the
// trailing `.git`.
func repoLabel(bare string) string {
	base := filepath.Base(bare)
	return strings.TrimSuffix(base, ".git")
}
