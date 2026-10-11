package pde

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"framework2/internal/dir"
)

// Plugin is one detected OSGi bundle: a `<worktree>/<name>` directory that
// `pde` should treat as a workspace bundle rather than take from the target
// platform.
type Plugin struct {
	// Name is the bundle directory name, which for KNIME repos equals the
	// bundle symbolic name (`org.knime.base`).
	Name string
	// Worktree is the absolute path of the linked dir the plugin lives in.
	Worktree string
	// Bare is the bare repo the worktree belongs to, "" for plain linked dirs.
	Bare string
}

// Path is the absolute bundle directory.
func (p Plugin) Path() string { return filepath.Join(p.Worktree, p.Name) }

// Detect returns the plugins touched by the ticket across all its linked dirs.
//
// The detection is deliberately identical to ticket-eclipse's: for each linked
// dir, union the merge-base diff against the bare repo's default branch with
// the unstaged, staged and untracked changes, then keep the first path segment
// of every result that looks like a bundle directory (`org.*` / `com.*`). A
// path with only one segment is ignored — a change to a repo-root file is not a
// change to a bundle.
//
// Non-worktree and vanished linked dirs are reported to progress and skipped,
// so a ticket that links a scratch directory alongside its worktrees still
// works. The result is sorted and de-duplicated; the same plugin found in two
// worktrees yields two entries, since those are genuinely different bundles.
func Detect(entries []dir.Entry, progress io.Writer) ([]Plugin, error) {
	var found []Plugin
	for _, e := range entries {
		if e.Path == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(e.Path, ".git")); err != nil {
			fmt.Fprintf(progress, "  skip (not a worktree): %s\n", e.Path)
			continue
		}
		names, err := changedPlugins(e)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			found = append(found, Plugin{Name: n, Worktree: e.Path, Bare: e.BareRepo})
		}
	}
	slices.SortFunc(found, func(a, b Plugin) int {
		if c := strings.Compare(a.Worktree, b.Worktree); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return slices.Compact(found), nil
}

// changedPlugins returns the bundle directory names touched in one linked dir.
func changedPlugins(e dir.Entry) ([]string, error) {
	// The default branch is read from the bare repo when the entry records one
	// (the beads worktree layout), else from the worktree itself.
	repo := e.BareRepo
	if repo == "" {
		repo = e.Path
	}

	var files []string
	if base, ok := defaultBase(repo); ok {
		// A missing merge-base (unfetched origin, unrelated history) is not
		// fatal: the working-tree passes below still detect uncommitted work.
		if mb, ok := git(e.Path, "merge-base", "HEAD", "origin/"+base); ok {
			files = append(files, gitLines(e.Path, "diff", "--name-only", mb, "HEAD")...)
		}
	}
	files = append(files, gitLines(e.Path, "diff", "--name-only", "HEAD")...)
	files = append(files, gitLines(e.Path, "diff", "--cached", "--name-only")...)
	files = append(files, gitLines(e.Path, "ls-files", "--others", "--exclude-standard")...)

	var names []string
	for _, f := range files {
		segs := strings.Split(f, "/")
		if len(segs) < 2 {
			continue
		}
		if !strings.HasPrefix(segs[0], "org.") && !strings.HasPrefix(segs[0], "com.") {
			continue
		}
		names = append(names, segs[0])
	}
	slices.Sort(names)
	return slices.Compact(names), nil
}

// defaultBase returns the repo's default branch name (without the `origin/`
// prefix), trying origin/HEAD first and falling back to main/master.
func defaultBase(repo string) (string, bool) {
	if ref, ok := git(repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); ok {
		return strings.TrimPrefix(ref, "origin/"), true
	}
	for _, b := range []string{"main", "master"} {
		if _, ok := git(repo, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+b); ok {
			return b, true
		}
	}
	return "", false
}

func git(path string, args ...string) (string, bool) {
	out, err := exec.Command("git", append([]string{"-C", path}, args...)...).Output()
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(out))
	return s, s != ""
}

func gitLines(path string, args ...string) []string {
	out, ok := git(path, args...)
	if !ok {
		return nil
	}
	return strings.Split(out, "\n")
}
