package dir

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// StatusResult is the JSON-shaped output of [Status].
type StatusResult struct {
	Entry      Entry            `json:"entry"`
	Worktree   WorktreeStatus   `json:"worktree"`
	Divergence *DivergenceStatus `json:"divergence,omitempty"`
}

// WorktreeStatus describes the working-tree state of one linked entry.
type WorktreeStatus struct {
	Branch    string `json:"branch"`
	Clean     bool   `json:"clean"`
	Staged    int    `json:"staged"`
	Unstaged  int    `json:"unstaged"`
	Untracked int    `json:"untracked"`
	Porcelain string `json:"porcelain,omitempty"`
}

// DivergenceStatus describes how the child's branch diverges from the parent
// ticket's branch on the same bare repo. When SameBranch is true the two
// tickets link the same branch and Ahead/Behind/AheadCommits are zero/empty.
type DivergenceStatus struct {
	ParentTicket string   `json:"parentTicket"`
	ParentBranch string   `json:"parentBranch"`
	SameBranch   bool     `json:"sameBranch"`
	Ahead        int      `json:"ahead"`
	Behind       int      `json:"behind"`
	AheadCommits []string `json:"aheadCommits,omitempty"`
}

// Status writes the status of a single linked entry of ticketID. selector picks
// the entry by path, localRepo, or bareRepo; when ticketID has exactly one
// linked entry selector may be empty. jsonOut toggles JSON output (default is
// plain text).
func Status(db, beadsDir, ticketID, selector string, jsonOut bool, out io.Writer) error {
	entries, err := load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("ticket %s has no linked dirs; link one with `ticket dir link` first", ticketID)
	}
	entry, err := pickEntry(ticketID, entries, selector)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(entry.Path); statErr != nil {
		return fmt.Errorf("linked path is gone: %s: %w", entry.Path, statErr)
	}
	wt, err := computeWorktreeStatus(*entry)
	if err != nil {
		return err
	}
	div, err := computeDivergence(db, beadsDir, ticketID, *entry, wt.Branch)
	if err != nil {
		return err
	}
	result := StatusResult{Entry: *entry, Worktree: wt, Divergence: div}
	if jsonOut {
		b, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(b))
		return err
	}
	return writeStatusText(out, result)
}

func pickEntry(ticketID string, entries []Entry, selector string) (*Entry, error) {
	if selector == "" {
		if len(entries) == 1 {
			return &entries[0], nil
		}
		return nil, ambiguousEntryError(ticketID, "", entries)
	}
	abs := selector
	if a, err := filepath.Abs(selector); err == nil {
		abs = a
	}
	var matches []int
	for i, e := range entries {
		switch selector {
		case e.Path, e.LocalRepo, e.BareRepo:
			matches = append(matches, i)
			continue
		}
		if abs != selector {
			switch abs {
			case e.Path, e.LocalRepo, e.BareRepo:
				matches = append(matches, i)
			}
		}
	}
	if len(matches) == 1 {
		return &entries[matches[0]], nil
	}
	return nil, ambiguousEntryError(ticketID, selector, entries)
}

func ambiguousEntryError(ticketID, selector string, entries []Entry) error {
	var b strings.Builder
	switch {
	case selector == "":
		fmt.Fprintf(&b, "ticket %s has %d linked dirs; pass one to select", ticketID, len(entries))
	default:
		fmt.Fprintf(&b, "selector %q does not match exactly one linked dir for ticket %s", selector, ticketID)
	}
	b.WriteString("; valid choices:")
	for _, e := range entries {
		b.WriteString("\n  ")
		b.WriteString(e.Path)
		if e.BareRepo != "" {
			fmt.Fprintf(&b, " (bareRepo: %s)", e.BareRepo)
		}
	}
	return errors.New(b.String())
}

func computeWorktreeStatus(e Entry) (WorktreeStatus, error) {
	branch, ok := gitBranch(e.Path)
	if !ok {
		return WorktreeStatus{}, fmt.Errorf("cannot read git branch at %s (not a repo or detached HEAD)", e.Path)
	}
	cmd := exec.Command("git", "-C", e.Path, "status", "--porcelain=v1")
	out, err := cmd.Output()
	if err != nil {
		return WorktreeStatus{}, fmt.Errorf("git status at %s: %w", e.Path, withStderr(err))
	}
	s := WorktreeStatus{Branch: branch}
	porcelain := strings.TrimRight(string(out), "\n")
	if porcelain != "" {
		s.Porcelain = porcelain
		for _, line := range strings.Split(porcelain, "\n") {
			if len(line) < 2 {
				continue
			}
			x, y := line[0], line[1]
			if x == '?' && y == '?' {
				s.Untracked++
				continue
			}
			if x != ' ' && x != '?' {
				s.Staged++
			}
			if y != ' ' && y != '?' {
				s.Unstaged++
			}
		}
	}
	s.Clean = s.Staged == 0 && s.Unstaged == 0 && s.Untracked == 0
	return s, nil
}

func computeDivergence(db, beadsDir, ticketID string, e Entry, liveChildBranch string) (*DivergenceStatus, error) {
	if e.BareRepo == "" {
		return nil, nil
	}
	parent, err := parentID(db, beadsDir, ticketID)
	if err != nil {
		return nil, err
	}
	if parent == "" {
		return nil, nil
	}
	parentEntries, err := load(db, beadsDir, parent)
	if err != nil {
		return nil, err
	}
	var pe *Entry
	for i := range parentEntries {
		if parentEntries[i].BareRepo == e.BareRepo {
			pe = &parentEntries[i]
			break
		}
	}
	if pe == nil {
		return nil, nil
	}
	parentBranch := pe.Branch
	if live, ok := gitBranch(pe.Path); ok {
		parentBranch = live
	}
	d := &DivergenceStatus{ParentTicket: parent, ParentBranch: parentBranch}
	if parentBranch == liveChildBranch {
		d.SameBranch = true
		return d, nil
	}
	spec := parentBranch + "...HEAD"
	out, err := exec.Command("git", "-C", e.Path, "rev-list", "--left-right", "--count", spec).Output()
	if err != nil {
		return nil, fmt.Errorf("git rev-list %s at %s: %w", spec, e.Path, withStderr(err))
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) != 2 {
		return nil, fmt.Errorf("unexpected rev-list output %q", string(out))
	}
	behind, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("parse rev-list behind count %q: %w", parts[0], err)
	}
	ahead, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("parse rev-list ahead count %q: %w", parts[1], err)
	}
	d.Behind = behind
	d.Ahead = ahead
	if ahead > 0 {
		logOut, err := exec.Command("git", "-C", e.Path, "log", "--oneline", parentBranch+"..HEAD").Output()
		if err != nil {
			return nil, fmt.Errorf("git log %s..HEAD at %s: %w", parentBranch, e.Path, withStderr(err))
		}
		for _, line := range strings.Split(strings.TrimRight(string(logOut), "\n"), "\n") {
			if line != "" {
				d.AheadCommits = append(d.AheadCommits, line)
			}
		}
	}
	return d, nil
}

func writeStatusText(out io.Writer, r StatusResult) error {
	var b strings.Builder
	fmt.Fprintf(&b, "path: %s\n", r.Entry.Path)
	fmt.Fprintf(&b, "branch: %s\n", r.Worktree.Branch)
	if r.Worktree.Clean {
		b.WriteString("status: clean\n")
	} else {
		fmt.Fprintf(&b, "status: dirty (staged: %d, unstaged: %d, untracked: %d)\n",
			r.Worktree.Staged, r.Worktree.Unstaged, r.Worktree.Untracked)
		if r.Worktree.Porcelain != "" {
			b.WriteString(r.Worktree.Porcelain)
			b.WriteString("\n")
		}
	}
	if r.Divergence != nil {
		d := r.Divergence
		if d.SameBranch {
			fmt.Fprintf(&b, "\nvs parent %s: same branch (%s)\n", d.ParentTicket, d.ParentBranch)
		} else {
			fmt.Fprintf(&b, "\nvs parent %s (branch %s):\n", d.ParentTicket, d.ParentBranch)
			fmt.Fprintf(&b, "  ahead %d, behind %d\n", d.Ahead, d.Behind)
			for _, c := range d.AheadCommits {
				fmt.Fprintf(&b, "  + %s\n", c)
			}
		}
	}
	_, err := io.WriteString(out, b.String())
	return err
}
