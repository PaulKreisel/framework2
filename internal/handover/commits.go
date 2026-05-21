package handover

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

// commitSource is one (bead, worktree) pair to scan for commits.
type commitSource struct {
	Entry dir.Entry
	Base  string
}

// writeCommitsMD renders <dirPath>/commits.md as a single chronologically
// ordered table covering every linked worktree in the subtree. Commits
// that appear in more than one source (e.g. a finished child's commit
// that has been FF-rebased onto the parent) are deduplicated by SHA. The
// `Ticket` column is read from the refs/notes/agent-session JSON payload
// and is empty for commits made outside any agent session.
func writeCommitsMD(dirPath string, sources []commitSource, beads map[string]*Bead) error {
	out := filepath.Join(dirPath, "commits.md")
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	fmt.Fprintln(w, "# Commits")
	fmt.Fprintln(w)

	seen := map[string]struct{}{}
	var rows []gitCommit
	var problems []string
	for _, s := range sources {
		commits, err := listCommits(s.Entry.Path, s.Base, s.Entry.Branch)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s @ `%s`: %v",
				repoLabel(s.Entry.BareRepo), s.Entry.Branch, err))
			continue
		}
		for _, c := range commits {
			if _, dup := seen[c.SHA]; dup {
				continue
			}
			seen[c.SHA] = struct{}{}
			rows = append(rows, c)
		}
	}

	// Chronological order across the whole subtree. Git's `--date=short`
	// gives YYYY-MM-DD which sorts lexicographically; tie-break by SHA so
	// the output is stable.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Date != rows[j].Date {
			return rows[i].Date < rows[j].Date
		}
		return rows[i].SHA < rows[j].SHA
	})

	if len(rows) == 0 {
		fmt.Fprintln(w, "_No commits in subtree._")
	} else {
		fmt.Fprintln(w, "| SHA | Date | Subject | Ticket |")
		fmt.Fprintln(w, "| --- | --- | --- | --- |")
		for _, c := range rows {
			fmt.Fprintf(w, "| `%s` | %s | %s | %s |\n",
				c.ShortSHA, c.Date,
				mdEscape(linkBeadIDsFromRoot(c.Subject, beads)),
				ticketCell(c.Ticket, beads))
		}
	}
	if len(problems) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "## Problems")
		fmt.Fprintln(w)
		for _, p := range problems {
			fmt.Fprintf(w, "- %s\n", p)
		}
	}
	return nil
}

// gitCommit is one row in the commits.md table.
type gitCommit struct {
	SHA      string
	ShortSHA string
	Date     string
	Subject  string
	Ticket   string
}

// listCommits returns the commits in base..branch evaluated from the
// worktree at wtPath, plus the ticket-id parsed from the
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
			SHA:      parts[0],
			ShortSHA: parts[1],
			Date:     parts[2],
			Subject:  parts[3],
			Ticket:   ticketIDForCommit(wtPath, parts[0]),
		})
	}
	return commits, nil
}

func ticketIDForCommit(wtPath, sha string) string {
	out, err := exec.Command("git", "-C", wtPath,
		"notes", "--ref="+finish.NotesRef, "show", sha,
	).Output()
	if err != nil {
		return ""
	}
	var payload struct {
		TicketID string `json:"ticketId"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return ""
	}
	return payload.TicketID
}

// ticketCell renders the Ticket column for a commit row. When the id is
// in the subtree, the cell is a relative markdown link to the bead's
// conversation.md; otherwise (or when no note exists) we render the raw
// string — empty for commits authored outside any session.
func ticketCell(id string, beads map[string]*Bead) string {
	if id == "" {
		return ""
	}
	if _, ok := beads[id]; ok {
		return fmt.Sprintf("[%s](tickets/%s/conversation.md)", id, id)
	}
	return id
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
