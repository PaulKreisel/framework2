package handover

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"framework2/internal/dir"
	"framework2/internal/finish"
)

// loadDirEntries returns the bead's metadata.dirs[] entries as
// dir.Entry values. Thin wrapper around dir.Load so the handover
// package shares the project's canonical Entry shape.
func loadDirEntries(db, beadsDir, ticketID string) ([]dir.Entry, error) {
	return dir.Load(db, beadsDir, ticketID)
}

// parentBranchesFor returns one parent-branch-on-same-bare per worktree
// entry. The lookup walks the bd parent chain until it finds an ancestor
// that links the same bare repo; falls back to git's default branch on
// the bare (origin/HEAD or main/master) when no ancestor matches.
//
// The returned map is keyed by the worktree's BareRepo so commits.md can
// pick the right base for each linked dir.
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
	// No ancestor links this bare. Fall back to whatever the bare's
	// default branch is — the worktree path resolves it.
	return defaultBranch(bare), nil
}

func defaultBranch(bare string) string {
	out, err := exec.Command("git", "-C", bare, "symbolic-ref", "--short", "HEAD").Output()
	if err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	// HEAD on a bare normally points to the default branch even without a
	// remote; fall through to a sane guess if not.
	return "main"
}

// writeCommitsMD renders <dirPath>/tickets/<id>/commits.md with one section
// per linked worktree. Per-commit session ids are pulled from the
// refs/notes/agent-session note attached to that SHA, when present.
func writeCommitsMD(dirPath string, b *Bead, entries []dir.Entry, parentBranches map[string]string, beads map[string]*Bead) error {
	out := filepath.Join(dirPath, "tickets", b.ID, "commits.md")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	fmt.Fprintf(w, "# %s · commits\n\n", b.ID)

	wrote := false
	for _, e := range entries {
		if e.BareRepo == "" || e.Branch == "" {
			continue
		}
		base := parentBranches[e.BareRepo]
		commits, err := listCommits(e.Path, base, e.Branch)
		if err != nil {
			fmt.Fprintf(w, "## %s — branch `%s`\n\n_error listing commits: %v_\n\n",
				repoLabel(e.BareRepo), e.Branch, err)
			wrote = true
			continue
		}
		fmt.Fprintf(w, "## %s — branch `%s`\n\n", repoLabel(e.BareRepo), e.Branch)
		fmt.Fprintf(w, "Base: `%s`\n\n", nz(base))
		if len(commits) == 0 {
			fmt.Fprintln(w, "_No commits in range._")
			fmt.Fprintln(w)
			wrote = true
			continue
		}
		fmt.Fprintln(w, "| SHA | Date | Subject | Session |")
		fmt.Fprintln(w, "| --- | --- | --- | --- |")
		for _, c := range commits {
			fmt.Fprintf(w, "| `%s` | %s | %s | %s |\n",
				c.ShortSHA, c.Date, mdEscape(linkBeadIDs(c.Subject, beads)), c.Session)
		}
		fmt.Fprintln(w)
		wrote = true
	}
	if !wrote {
		fmt.Fprintln(w, "_No worktrees linked._")
	}
	return nil
}

// gitCommit is one row in the commits.md table.
type gitCommit struct {
	ShortSHA string
	Date     string
	Subject  string
	Session  string
}

// listCommits returns the commits in base..branch evaluated from the
// worktree at wtPath, plus the session-id parsed from the
// refs/notes/agent-session note on each commit (empty when no note exists).
//
// handover is intended for unfinished tickets — once `this finish` has
// removed the worktree, wtPath will be missing and `git log` will fail with
// a path error that surfaces in commits.md.
func listCommits(wtPath, base, branch string) ([]gitCommit, error) {
	if wtPath == "" {
		return nil, fmt.Errorf("worktree path empty")
	}
	rng := branch
	if base != "" {
		rng = base + ".." + branch
	}
	// Format: SHA \t shortSHA \t date \t subject (each commit one line, NUL-terminated).
	cmd := exec.Command("git", "-C", wtPath, "log",
		"--reverse",
		"-z",
		"--date=short",
		"--pretty=format:%H%x09%h%x09%ad%x09%s",
		rng,
	)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("git log %s: %v: %s", rng, err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("git log %s: %w", rng, err)
	}
	var commits []gitCommit
	for _, rec := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\t", 4)
		if len(parts) < 4 {
			continue
		}
		commits = append(commits, gitCommit{
			ShortSHA: parts[1],
			Date:     parts[2],
			Subject:  parts[3],
			Session:  sessionIDForCommit(wtPath, parts[0]),
		})
	}
	return commits, nil
}

func sessionIDForCommit(wtPath, sha string) string {
	out, err := exec.Command("git", "-C", wtPath,
		"notes", "--ref="+finish.NotesRef, "show", sha,
	).Output()
	if err != nil {
		return ""
	}
	var payload struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return ""
	}
	return payload.SessionID
}

// repoLabel returns a portable name for the bare repo: basename minus the
// trailing `.git`. Matches the naming convention spec'd in ai-vrc.57.2 even
// though we don't emit repos.json today — the reviewer can still grep for
// the label across commits.md.
func repoLabel(bare string) string {
	base := filepath.Base(bare)
	return strings.TrimSuffix(base, ".git")
}

// mdEscape escapes characters that would break a markdown table row: `|`
// would close the column early, newlines would break the row layout. We
// keep the escape minimal so commit subjects stay readable.
func mdEscape(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}
