package finish

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// subtaskTrailerRE matches a stand-alone "Subtask N" or "Subtask N.M.…" line:
// the exact shape this package writes and re-recognises across runs. Anything
// looser (extra punctuation, additional text on the line) is treated as
// regular body text — the trailer rule only acts on its own emissions.
var subtaskTrailerRE = regexp.MustCompile(`^Subtask \d+(\.\d+)*$`)

// rewriteSubtaskTrailer applies the Subtask-trailer rule from the ticket spec
// to msg with the allocated index N:
//
//   - If the very last non-empty line matches the Subtask trailer regex, the
//     leading "N." is prepended to the existing dotted sequence. Example:
//     "Subtask 4.2" with N=7 → "Subtask 7.4.2".
//   - Otherwise a blank line + "Subtask N" is appended.
//
// When idempotent is true and the trailer already starts with the same N
// (e.g. a partial previous run rewrote this commit), the message is returned
// unchanged so resumes don't double-prepend. On a fresh allocation this
// guard must NOT fire — a cascade where this finish's N coincidentally
// equals a descendant-finish's M would otherwise be misclassified as
// already-rewritten and skipped instead of becoming "Subtask N.M".
func rewriteSubtaskTrailer(msg string, index int, idempotent bool) string {
	// Find the last non-empty line.
	lines := strings.Split(msg, "\n")
	lastIdx := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			lastIdx = i
			break
		}
	}
	idxStr := strconv.Itoa(index)
	if lastIdx >= 0 && subtaskTrailerRE.MatchString(lines[lastIdx]) {
		existing := strings.TrimPrefix(lines[lastIdx], "Subtask ")
		if idempotent {
			// A previous (interrupted) run may have already prepended the
			// same N. The leading component before the first '.' identifies
			// the most-recently-prepended index.
			firstPart := existing
			if i := strings.IndexByte(existing, '.'); i >= 0 {
				firstPart = existing[:i]
			}
			if firstPart == idxStr {
				return msg
			}
		}
		lines[lastIdx] = "Subtask " + idxStr + "." + existing
		// Drop any trailing blank lines that were after the trailer, then
		// re-add a single newline to leave the message tidy.
		out := strings.Join(lines[:lastIdx+1], "\n")
		return out + "\n"
	}
	// No existing trailer: append a blank line + new trailer.
	trimmed := strings.TrimRight(msg, "\n")
	return trimmed + "\n\n" + "Subtask " + idxStr + "\n"
}

// commitInfo captures the bits of a commit we need to re-create it with a
// new message via `git commit-tree`. Authorship and the original timestamps
// are preserved end-to-end so the rewrite is invisible to `git log` aside
// from the message change.
type commitInfo struct {
	AuthorName     string
	AuthorEmail    string
	AuthorDate     string
	CommitterName  string
	CommitterEmail string
	CommitterDate  string
}

func (c commitInfo) env() []string {
	return []string{
		"GIT_AUTHOR_NAME=" + c.AuthorName,
		"GIT_AUTHOR_EMAIL=" + c.AuthorEmail,
		"GIT_AUTHOR_DATE=" + c.AuthorDate,
		"GIT_COMMITTER_NAME=" + c.CommitterName,
		"GIT_COMMITTER_EMAIL=" + c.CommitterEmail,
		"GIT_COMMITTER_DATE=" + c.CommitterDate,
	}
}

func readCommitInfo(wt, sha string) (commitInfo, error) {
	// %x00 emits a NUL byte; using a NUL separator instead of newlines means
	// author/committer names that legitimately contain newlines (unusual but
	// not forbidden) don't break the parse.
	out, err := exec.Command("git", "-C", wt, "log", "-1",
		"--format=%an%x00%ae%x00%aI%x00%cn%x00%ce%x00%cI", sha).Output()
	if err != nil {
		return commitInfo{}, fmt.Errorf("read commit info for %s in %s: %w", sha, wt, err)
	}
	parts := strings.Split(strings.TrimRight(string(out), "\n"), "\x00")
	if len(parts) != 6 {
		return commitInfo{}, fmt.Errorf("commit info for %s in %s: expected 6 fields, got %d", sha, wt, len(parts))
	}
	return commitInfo{
		AuthorName: parts[0], AuthorEmail: parts[1], AuthorDate: parts[2],
		CommitterName: parts[3], CommitterEmail: parts[4], CommitterDate: parts[5],
	}, nil
}

// listCommitsInRange returns the commit SHAs in (old..new] inside wt, oldest
// first (chronological / topological forward order).
func listCommitsInRange(wt, oldSHA, newSHA string) ([]string, error) {
	out, err := exec.Command("git", "-C", wt, "rev-list", "--reverse", oldSHA+".."+newSHA).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("rev-list %s..%s in %s: %v: %s",
				oldSHA, newSHA, wt, err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("rev-list %s..%s in %s: %w", oldSHA, newSHA, wt, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var commits []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			commits = append(commits, l)
		}
	}
	return commits, nil
}

func readCommitMessage(wt, sha string) (string, error) {
	out, err := exec.Command("git", "-C", wt, "log", "-1", "--format=%B", sha).Output()
	if err != nil {
		return "", fmt.Errorf("read message for %s in %s: %w", sha, wt, err)
	}
	return string(out), nil
}

// rewriteCommitsInRange replays every commit in (oldSHA..newSHA] on top of
// oldSHA in wt with a Subtask-trailer-rewritten message, then points the
// branch ref at the new tip. The working tree's HEAD is reset to the new tip
// — safe because the tree of each rewritten commit is identical to the
// original, so file contents on disk don't change.
//
// Returns the new tip SHA. When no commits fall in the range the call is a
// no-op and newSHA is returned verbatim.
//
// When idempotent is true the per-commit rewrite skips commits whose trailer
// already starts with index — used on the resume-after-partial-failure path,
// where some commits may already have been rewritten in a prior run. On a
// fresh allocation idempotent must be false: a coincidence between this
// finish's index and a descendant-finish's index (e.g. both = 1 in a stack
// with one contributor per layer) would otherwise be misread as
// already-rewritten and skipped, breaking the cascade rule.
func rewriteCommitsInRange(wt, branch, oldSHA, newSHA string, index int, idempotent bool) (string, error) {
	commits, err := listCommitsInRange(wt, oldSHA, newSHA)
	if err != nil {
		return "", err
	}
	if len(commits) == 0 {
		return newSHA, nil
	}
	parent := oldSHA
	for _, sha := range commits {
		tree, ok := runGit(wt, "rev-parse", sha+"^{tree}")
		if !ok {
			return "", fmt.Errorf("rev-parse tree of %s in %s", sha, wt)
		}
		info, err := readCommitInfo(wt, sha)
		if err != nil {
			return "", err
		}
		msg, err := readCommitMessage(wt, sha)
		if err != nil {
			return "", err
		}
		newMsg := rewriteSubtaskTrailer(msg, index, idempotent)
		cmd := exec.Command("git", "-C", wt, "commit-tree", tree, "-p", parent, "-F", "-")
		cmd.Env = append(os.Environ(), info.env()...)
		cmd.Stdin = strings.NewReader(newMsg)
		out, err := cmd.Output()
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
				return "", fmt.Errorf("commit-tree for %s in %s: %v: %s", sha, wt, err, ee.Stderr)
			}
			return "", fmt.Errorf("commit-tree for %s in %s: %w", sha, wt, err)
		}
		parent = strings.TrimSpace(string(out))
	}
	if parent == newSHA {
		// Idempotent path: every commit already had the trailer for this N,
		// so the rebuild produced identical SHAs. Nothing to update.
		return parent, nil
	}
	// Update the branch ref and reset the worktree HEAD. `reset --hard`
	// requires the worktree to be on `branch`; in practice it always is
	// (the child worktree is the only one ever on the child's branch), but
	// guard via update-ref-then-reset rather than a bare reset so a worktree
	// that's somehow off-branch fails loudly instead of silently swapping
	// the user's HEAD.
	if err := runGitChecked(wt, "update-ref", "refs/heads/"+branch, parent, newSHA); err != nil {
		return "", err
	}
	// Refresh the worktree's index/HEAD to the new tip. Trees are identical
	// to the old commits, so the working tree files don't change.
	curBranch, ok := runGit(wt, "rev-parse", "--abbrev-ref", "HEAD")
	if ok && curBranch == branch {
		if err := runGitChecked(wt, "reset", "--hard", parent); err != nil {
			return "", err
		}
	}
	return parent, nil
}
