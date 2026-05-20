package finish

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"framework2/internal/agent"
)

// writeSessionNotes attaches a JSON-encoded {sessionId, ticketId} note on the
// [NotesRef] notes ref to every commit in OldParentSHA..NewParentSHA of each
// FF-advanced parent worktree. The session id is the most recent agent-mode
// session recorded against ticketID; tickets that never hosted an agent-mode
// session (e.g. spec-only, or `this finish` run outside any agent context)
// skip note writing entirely.
//
// Notes are written via the parent worktree (`git -C <parentWT>`) so they
// land in the bare repo's notes ref by default — they are visible to every
// worktree of that bare. Per-commit writes use `git notes add -f`; existing
// notes on the same commit are overwritten with the new attribution so a
// re-run of `this finish` after a stale failed run does the right thing.
func writeSessionNotes(db, beadsDir, ticketID string, results []rebaseResult) error {
	if len(results) == 0 {
		return nil
	}
	sessionID, err := latestAgentSessionID(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	if sessionID == "" {
		return nil
	}
	payload, err := json.Marshal(struct {
		SessionID string `json:"sessionId"`
		TicketID  string `json:"ticketId"`
	}{SessionID: sessionID, TicketID: ticketID})
	if err != nil {
		return fmt.Errorf("marshal note payload: %w", err)
	}
	note := string(payload)
	for _, r := range results {
		if r.OldParentSHA == r.NewParentSHA {
			continue
		}
		commits, err := listCommitsInRange(r.ParentEntry.Path, r.OldParentSHA, r.NewParentSHA)
		if err != nil {
			return err
		}
		for _, sha := range commits {
			if err := runGitChecked(r.ParentEntry.Path,
				"notes", "--ref="+NotesRef, "add", "-f", "-m", note, sha,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

// latestAgentSessionID returns the SessionID of the most recent agent-mode
// entry recorded against ticketID. Returns "" when no such entry exists.
func latestAgentSessionID(db, beadsDir, ticketID string) (string, error) {
	entries, err := agent.LoadAgents(db, beadsDir, ticketID)
	if err != nil {
		return "", err
	}
	want := agent.ModeAgent.String()
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Mode == want {
			return entries[i].SessionID, nil
		}
	}
	return "", nil
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

