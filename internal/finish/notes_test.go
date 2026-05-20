package finish_test

import (
	"encoding/json"
	"strings"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/finish"
	"framework2/internal/testutil"
)

// TestFinishWritesAgentSessionNotes verifies that after a successful FF-merge
// of a child branch onto its parent, `this finish` writes a JSON note
// {sessionId, ticketId} on refs/notes/agent-session for every commit that
// landed on the parent. The session id is the most recent agent-mode entry
// recorded against the ticket.
func TestFinishWritesAgentSessionNotes(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "finish-notes-parent")
	child := testutil.CreateChildBead(t, beadsDir, "finish-notes-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)

	sessionID := "11111111-1111-4111-8111-111111111111"
	if err := agent.RegisterAgent("", beadsDir, child, agent.AgentEntry{
		SessionID: sessionID,
		Mode:      agent.ModeAgent.String(),
	}); err != nil {
		t.Fatalf("register agent: %v", err)
	}

	childWT := worktreePath(bare, child)
	writeAndCommit(t, childWT, "a.txt", "a\n", "child commit 1")
	writeAndCommit(t, childWT, "b.txt", "b\n", "child commit 2")
	childTip := gitOut(t, childWT, "rev-parse", "HEAD")
	childTipParent := gitOut(t, childWT, "rev-parse", "HEAD^")

	mustRunTicket(t, beadsDir, "finish", child)

	// Both commits introduced on the child branch should now carry a
	// session-attribution note. Read the notes from the bare repo (notes
	// land on the bare's notes ref by default).
	for _, sha := range []string{childTip, childTipParent} {
		got := gitOut(t, bare, "notes", "--ref="+finish.NotesRef, "show", sha)
		var payload struct {
			SessionID string `json:"sessionId"`
			TicketID  string `json:"ticketId"`
		}
		if err := json.Unmarshal([]byte(got), &payload); err != nil {
			t.Fatalf("note payload for %s is not JSON: %v\n%s", sha, err, got)
		}
		if payload.SessionID != sessionID {
			t.Errorf("note sessionId for %s = %q, want %q", sha, payload.SessionID, sessionID)
		}
		if payload.TicketID != child {
			t.Errorf("note ticketId for %s = %q, want %q", sha, payload.TicketID, child)
		}
	}

	// `git log --notes=...` should surface the notes alongside the commits
	// — that's the reviewer-facing affordance promised in the ticket spec.
	logOut := gitOut(t, bare, "log",
		"--notes="+finish.NotesRef,
		"--format=%H%n%N",
		childTipParent+"^.."+childTip,
	)
	if !strings.Contains(logOut, sessionID) {
		t.Errorf("git log --notes did not surface sessionId %q:\n%s", sessionID, logOut)
	}
}

// TestFinishSkipsNotesWhenNoAgentSession covers the "no agent-mode session
// recorded against the ticket" case: `this finish` should land the rebase
// cleanly but skip note-writing entirely.
func TestFinishSkipsNotesWhenNoAgentSession(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "finish-no-notes-parent")
	child := testutil.CreateChildBead(t, beadsDir, "finish-no-notes-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	childWT := worktreePath(bare, child)
	advanceBranch(t, childWT)
	childTip := gitOut(t, childWT, "rev-parse", "HEAD")

	mustRunTicket(t, beadsDir, "finish", child)

	// Bare must have no note on the new commit — and indeed no
	// refs/notes/agent-session ref at all, since nothing else writes it.
	out, err := runGitCmd(bare, gitEnv(), "notes", "--ref="+finish.NotesRef, "show", childTip)
	if err == nil {
		t.Errorf("expected no note on %s, got %q", childTip, strings.TrimSpace(out))
	}
}

// TestFinishSkipsNotesForSpecOnlySession covers the explicit-spec case:
// only spec-mode sessions have ever touched the ticket, so no notes are
// written. (spec sessions don't commit, but a manual user commit on the
// branch should not be attributed to the spec session either.)
func TestFinishSkipsNotesForSpecOnlySession(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "finish-spec-only-parent")
	child := testutil.CreateChildBead(t, beadsDir, "finish-spec-only-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	if err := agent.RegisterAgent("", beadsDir, child, agent.AgentEntry{
		SessionID: "22222222-2222-4222-8222-222222222222",
		Mode:      agent.ModeSpec.String(),
	}); err != nil {
		t.Fatalf("register spec agent: %v", err)
	}

	childWT := worktreePath(bare, child)
	advanceBranch(t, childWT)
	childTip := gitOut(t, childWT, "rev-parse", "HEAD")

	mustRunTicket(t, beadsDir, "finish", child)

	if out, err := runGitCmd(bare, gitEnv(), "notes", "--ref="+finish.NotesRef, "show", childTip); err == nil {
		t.Errorf("expected no note on %s (spec-only session), got %q", childTip, strings.TrimSpace(out))
	}
}

// TestFinishUsesLatestAgentSessionID covers the "latest agent-mode entry
// wins" rule: when a ticket has multiple agent-mode entries recorded, the
// most-recently-appended one supplies the note's sessionId.
func TestFinishUsesLatestAgentSessionID(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "finish-latest-parent")
	child := testutil.CreateChildBead(t, beadsDir, "finish-latest-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)

	first := "aaaaaaaa-1111-4111-8111-111111111111"
	second := "bbbbbbbb-2222-4222-8222-222222222222"
	if err := agent.RegisterAgent("", beadsDir, child, agent.AgentEntry{
		SessionID: first, Mode: agent.ModeAgent.String(),
	}); err != nil {
		t.Fatalf("register first agent: %v", err)
	}
	if err := agent.RegisterAgent("", beadsDir, child, agent.AgentEntry{
		SessionID: second, Mode: agent.ModeAgent.String(),
	}); err != nil {
		t.Fatalf("register second agent: %v", err)
	}

	childWT := worktreePath(bare, child)
	advanceBranch(t, childWT)
	childTip := gitOut(t, childWT, "rev-parse", "HEAD")

	mustRunTicket(t, beadsDir, "finish", child)

	got := gitOut(t, bare, "notes", "--ref="+finish.NotesRef, "show", childTip)
	var payload struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("note payload not JSON: %v\n%s", err, got)
	}
	if payload.SessionID != second {
		t.Errorf("note sessionId = %q, want %q (latest agent-mode entry)", payload.SessionID, second)
	}
}
