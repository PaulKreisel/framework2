package agent_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"framework2/internal/agent"
	"framework2/internal/testutil"
)

// helper: load the single registered agent for ticket.
func loadOne(t *testing.T, beadsDir, ticket string) agent.AgentEntry {
	t.Helper()
	entries, err := agent.LoadAgents("", beadsDir, ticket)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want exactly 1 agent record, got %d", len(entries))
	}
	return entries[0]
}

func TestSetStatusUpdatesStatusAndTimestamp(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "setstatus-update", "x", "")

	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID:       "11111111-1111-4111-8111-111111111111",
		PID:             1,
		Mode:            "agent",
		StartedAt:       "2026-05-15T14:56:00Z",
		Env:             "/env-x",
		Status:          agent.StatusRunning,
		StatusUpdatedAt: "2026-05-15T14:56:00Z",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := agent.SetStatus("", beadsDir,
		"11111111-1111-4111-8111-111111111111",
		agent.StatusWaiting, "", "", ticket, 0); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	got := loadOne(t, beadsDir, ticket)
	if got.Status != agent.StatusWaiting {
		t.Errorf("Status = %q, want %q", got.Status, agent.StatusWaiting)
	}
	// StatusUpdatedAt should have advanced (post-1970, RFC 3339 parseable).
	ts, err := time.Parse(time.RFC3339, got.StatusUpdatedAt)
	if err != nil {
		t.Errorf("StatusUpdatedAt = %q: not RFC 3339 (%v)", got.StatusUpdatedAt, err)
	}
	if got.StatusUpdatedAt == "2026-05-15T14:56:00Z" {
		t.Errorf("StatusUpdatedAt = %q, want a fresh timestamp", got.StatusUpdatedAt)
	}
	if ts.Before(time.Now().Add(-5 * time.Minute)) {
		t.Errorf("StatusUpdatedAt = %q, looks stale relative to now", got.StatusUpdatedAt)
	}
}

func TestSetStatusIsNoOpWhenAlreadyAtTarget(t *testing.T) {
	// Re-firing `Stop` while the agent is already "waiting" must be a no-op:
	// no error, no timestamp churn. This guards the Caveat in the spec — Stop
	// can re-fire on tool-result continues.
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "setstatus-noop", "x", "")

	original := "2026-05-15T14:56:00Z"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID:       "22222222-2222-4222-8222-222222222222",
		PID:             1,
		Mode:            "agent",
		StartedAt:       original,
		Env:             "/env-x",
		Status:          agent.StatusWaiting,
		StatusUpdatedAt: original,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := agent.SetStatus("", beadsDir,
		"22222222-2222-4222-8222-222222222222",
		agent.StatusWaiting, "", "", ticket, 0); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	got := loadOne(t, beadsDir, ticket)
	if got.StatusUpdatedAt != original {
		t.Errorf("StatusUpdatedAt churned on no-op transition: %q != %q",
			got.StatusUpdatedAt, original)
	}
}

func TestSetStatusErrorsWhenSessionMissing(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "setstatus-missing", "x", "")

	err := agent.SetStatus("", beadsDir,
		"deadbeef-dead-4dea-8dea-deaddeaddead",
		agent.StatusRunning, "", "", ticket, 0)
	if err == nil {
		t.Fatalf("SetStatus on missing session: want error, got nil")
	}
	if !errors.Is(err, agent.ErrSessionNotFound) {
		t.Errorf("error %q should wrap ErrSessionNotFound", err)
	}
	if !strings.Contains(err.Error(), "deadbeef") {
		t.Errorf("error %q should mention the missing session id", err)
	}
}

func TestSetStatusLocatesTicketWhenUnspecified(t *testing.T) {
	// Hooks only have the session id — they call set-status without --ticket.
	// SetStatus must scan beads to find the owning ticket.
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	other := testutil.CreateBeadFull(t, beadsDir, "setstatus-locate-other", "x", "")
	target := testutil.CreateBeadFull(t, beadsDir, "setstatus-locate-target", "x", "")

	// Decoy entry on `other`: different session id, must be left alone.
	if err := agent.RegisterAgent("", beadsDir, other, agent.AgentEntry{
		SessionID:       "11111111-1111-4111-8111-111111111111",
		PID:             1,
		Mode:            "agent",
		StartedAt:       "2026-05-15T14:00:00Z",
		Env:             "/env-other",
		Status:          agent.StatusRunning,
		StatusUpdatedAt: "2026-05-15T14:00:00Z",
	}); err != nil {
		t.Fatalf("Register decoy: %v", err)
	}
	// Real target entry on `target`.
	if err := agent.RegisterAgent("", beadsDir, target, agent.AgentEntry{
		SessionID:       "33333333-3333-4333-8333-333333333333",
		PID:             1,
		Mode:            "agent",
		StartedAt:       "2026-05-15T14:30:00Z",
		Env:             "/env-target",
		Status:          agent.StatusRunning,
		StatusUpdatedAt: "2026-05-15T14:30:00Z",
	}); err != nil {
		t.Fatalf("Register target: %v", err)
	}

	if err := agent.SetStatus("", beadsDir,
		"33333333-3333-4333-8333-333333333333",
		agent.StatusWaiting, "", "", "", 0); err != nil {
		t.Fatalf("SetStatus (no ticket id): %v", err)
	}

	if got := loadOne(t, beadsDir, target); got.Status != agent.StatusWaiting {
		t.Errorf("target.Status = %q, want %q", got.Status, agent.StatusWaiting)
	}
	if got := loadOne(t, beadsDir, other); got.Status != agent.StatusRunning {
		t.Errorf("decoy entry was mutated: Status = %q, want %q",
			got.Status, agent.StatusRunning)
	}
}

func TestListAgentsShowsWaitingState(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "list-waiting-state", "x", "")

	// PID 1 alive + Status=waiting → STATE column shows "waiting".
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID:       "44444444-4444-4444-8444-444444444444",
		PID:             1,
		Mode:            "agent",
		StartedAt:       "2026-05-15T14:56:00Z",
		Env:             "/env-w",
		Status:          agent.StatusWaiting,
		StatusUpdatedAt: "2026-05-15T15:00:00Z",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket)
	if code != 0 {
		t.Fatalf("agent list: exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "waiting") {
		t.Errorf("STATE column missing waiting:\n%s", stdout)
	}
}

func TestListAgentsKeepsStoredStatusForDeadWrapper(t *testing.T) {
	// StateOf does not probe the wrapper pid: a record whose terminal was
	// killed keeps its last hook-recorded Status. This preserves the user's
	// only handle on a waiting-then-killed agent — they can still see it in
	// the list and resume it via `agent attach`.
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "list-deadpid-keeps-status", "x", "")

	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: "55555555-5555-4555-8555-555555555555",
		PID:       999999, // dead
		Mode:      "agent",
		StartedAt: "2026-05-15T14:56:00Z",
		Env:       "/env-zombie",
		Status:    agent.StatusWaiting,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket)
	if code != 0 {
		t.Fatalf("agent list: exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "waiting") {
		t.Errorf("dead-wrapper agent lost its waiting Status:\n%s", stdout)
	}
	if strings.Contains(stdout, "exited") {
		t.Errorf("dead-wrapper agent should keep stored Status, not auto-flip to exited:\n%s", stdout)
	}
}

func TestSetStatusCLIWithSessionFlag(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "setstatus-cli", "x", "")

	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: "66666666-6666-4666-8666-666666666666",
		PID:       1,
		Mode:      "agent",
		StartedAt: "2026-05-15T14:56:00Z",
		Env:       "/env-cli",
		Status:    agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, stderr, code := testutil.RunTicketIn(t, beadsDir,
		"agent", "set-status", "waiting",
		"--session", "66666666-6666-4666-8666-666666666666")
	if code != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", code, stderr)
	}

	if got := loadOne(t, beadsDir, ticket); got.Status != agent.StatusWaiting {
		t.Errorf("Status = %q, want %q", got.Status, agent.StatusWaiting)
	}
}

func TestSetStatusCLIHookPathSilentOnUnknownSession(t *testing.T) {
	// Stop / UserPromptSubmit hooks fire for EVERY claude session, including
	// ones not launched via `ticket agent` (and thus never registered in any
	// bead's metadata.agents). When the session id arrives off stdin (the
	// hook signature), an unknown session must be a silent exit-0 — not an
	// error that pollutes the parent session's UI. Explicit --session calls
	// (manual testing backstop) keep the strict behaviour, covered separately
	// by TestSetStatusCLIRejectsUnknownState.
	beadsDir := testutil.TestDB(t)

	// Plant a registered session on a decoy ticket so the "any tickets to
	// scan" code path runs — we want the not-found branch, not the
	// no-tickets branch.
	decoy := testutil.CreateBeadFull(t, beadsDir, "hookpath-decoy", "x", "")
	if err := agent.RegisterAgent("", beadsDir, decoy, agent.AgentEntry{
		SessionID: "00000000-0000-4000-8000-decoydecoyde",
		PID:       1, Mode: "agent", StartedAt: "2026-05-15T14:00:00Z",
		Env: "/env-decoy", Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register decoy: %v", err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.Write([]byte(`{"session_id":"99999999-9999-4999-8999-unknownunknown","hook_event_name":"Stop"}`)); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	w.Close()

	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir,
		"agent", "set-status", "waiting")
	if code != 0 {
		t.Fatalf("hook-path set-status: exit=%d, want 0 (silent skip)\nstdout=%q\nstderr=%q",
			code, stdout, stderr)
	}
	if stderr != "" {
		t.Errorf("hook-path set-status should be silent; stderr=%q", stderr)
	}
}

func TestSetStatusCLIRejectsUnknownState(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	_, stderr, code := testutil.RunTicketIn(t, beadsDir,
		"agent", "set-status", "frobnicating",
		"--session", "77777777-7777-4777-8777-777777777777")
	if code == 0 {
		t.Fatalf("set-status with bad state: want non-zero exit, got 0 (stderr=%q)", stderr)
	}
	if !strings.Contains(stderr, "frobnicating") {
		t.Errorf("error %q should mention the bad state", stderr)
	}
}

// TestSetStatusRoundTripsWaitingReason covers the round-trip described in
// description.md: register, flip to waiting with a reason, reload, assert
// persisted; flip back to running (empty reason), assert cleared.
func TestSetStatusRoundTripsWaitingReason(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "setstatus-reason", "x", "")

	sid := "abababab-abab-4bab-8bab-ababababab01"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env-r",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	reason := "Waiting for the user to confirm the schema migration."
	if err := agent.SetStatus("", beadsDir, sid, agent.StatusWaiting, reason, "", ticket, 0); err != nil {
		t.Fatalf("SetStatus waiting: %v", err)
	}
	if got := loadOne(t, beadsDir, ticket); got.WaitingReason != reason {
		t.Errorf("WaitingReason = %q, want %q", got.WaitingReason, reason)
	}

	// Back to running: reason must clear so a stale message can't
	// survive a UserPromptSubmit.
	if err := agent.SetStatus("", beadsDir, sid, agent.StatusRunning, "", "", ticket, 0); err != nil {
		t.Fatalf("SetStatus running: %v", err)
	}
	got := loadOne(t, beadsDir, ticket)
	if got.WaitingReason != "" {
		t.Errorf("WaitingReason after running transition = %q, want empty", got.WaitingReason)
	}
	if got.Status != agent.StatusRunning {
		t.Errorf("Status = %q, want %q", got.Status, agent.StatusRunning)
	}
}

// TestSetStatusClearsReasonEvenWhenCallerPassesOne pins the "running implies
// no reason" rule end-to-end: even a misuse where a reason is passed
// alongside StatusRunning must clear the stored value.
func TestSetStatusClearsReasonEvenWhenCallerPassesOne(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "setstatus-reason-clear", "x", "")

	sid := "abababab-abab-4bab-8bab-ababababab02"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusWaiting, WaitingReason: "previous turn",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := agent.SetStatus("", beadsDir, sid, agent.StatusRunning, "ignored", "", ticket, 0); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if got := loadOne(t, beadsDir, ticket); got.WaitingReason != "" {
		t.Errorf("WaitingReason = %q, want empty", got.WaitingReason)
	}
}

func TestSetStatusCLIParsesLastAssistantMessageFromStdin(t *testing.T) {
	// The hook signature: session_id and last_assistant_message arrive
	// together on stdin. The CLI must forward the message through to the
	// stored WaitingReason.
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "setstatus-stdin-reason", "x", "")

	sid := "abababab-abab-4bab-8bab-ababababab03"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	payload := `{"session_id":"abababab-abab-4bab-8bab-ababababab03",` +
		`"hook_event_name":"Stop",` +
		`"last_assistant_message":"I have applied the rename and am ready for the next step."}`
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	w.Close()
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "set-status", "waiting")
	if code != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", code, stderr)
	}
	got := loadOne(t, beadsDir, ticket)
	if got.WaitingReason != "I have applied the rename and am ready for the next step." {
		t.Errorf("WaitingReason = %q, want the message from stdin", got.WaitingReason)
	}
}

func TestSetStatusCLIToleratesMissingLastAssistantMessage(t *testing.T) {
	// A stdin payload without last_assistant_message must not error — older
	// hook payloads or non-Stop events (UserPromptSubmit) lack the field
	// and should still succeed.
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "setstatus-stdin-noreason", "x", "")

	sid := "abababab-abab-4bab-8bab-ababababab04"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.Write([]byte(`{"session_id":"abababab-abab-4bab-8bab-ababababab04","hook_event_name":"Stop"}`)); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	w.Close()
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "set-status", "waiting")
	if code != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", code, stderr)
	}
	if got := loadOne(t, beadsDir, ticket); got.WaitingReason != "" {
		t.Errorf("WaitingReason = %q, want empty when payload omits the field", got.WaitingReason)
	}
}

// TestSetStatusCLIAskUserQuestionSource: stdin JSON with session_id and a
// tool_input.questions[...] block round-trips through set-status with
// --source ask-user-question. WaitingReason holds the raw tool_input JSON
// for full fidelity; WaitingReasonSource tags how the table view should
// render it; FormatWaitingReason picks up the first question's text.
func TestSetStatusCLIAskUserQuestionSource(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "setstatus-source-ask", "x", "")

	sid := "abababab-abab-4bab-8bab-ababababab10"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	toolInput := `{"questions":[{"question":"Which library should we use for date formatting?","header":"Library","options":[{"label":"date-fns"},{"label":"luxon"}]}]}`
	payload := `{"session_id":"` + sid + `","hook_event_name":"PreToolUse","tool_input":` + toolInput + `}`
	pipeStdin(t, payload)

	_, stderr, code := testutil.RunTicketIn(t, beadsDir,
		"agent", "set-status", "waiting", "--source", "ask-user-question")
	if code != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", code, stderr)
	}

	got := loadOne(t, beadsDir, ticket)
	if got.WaitingReasonSource != agent.WaitingReasonSourceAskUserQuestion {
		t.Errorf("WaitingReasonSource = %q, want %q",
			got.WaitingReasonSource, agent.WaitingReasonSourceAskUserQuestion)
	}
	if got.WaitingReason != toolInput {
		t.Errorf("WaitingReason = %q, want raw tool_input JSON %q", got.WaitingReason, toolInput)
	}
	summary := agent.FormatWaitingReason(got)
	if !strings.HasPrefix(summary, "asking: ") {
		t.Errorf("FormatWaitingReason = %q, want \"asking: …\" prefix", summary)
	}
	if !strings.Contains(summary, "date formatting") {
		t.Errorf("FormatWaitingReason = %q, want it to mention the question text", summary)
	}
}

// TestSetStatusCLIExitPlanModeSource: stdin JSON with tool_input.plan
// parses through; WaitingReason holds the plan body; the table summary
// starts with "plan: " and quotes the first non-empty line.
func TestSetStatusCLIExitPlanModeSource(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "setstatus-source-plan", "x", "")

	sid := "abababab-abab-4bab-8bab-ababababab11"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	plan := "Refactor the auth flow\n\nDetails follow…"
	planJSON, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	payload := `{"session_id":"` + sid + `","hook_event_name":"PreToolUse","tool_input":{"plan":` + string(planJSON) + `}}`
	pipeStdin(t, payload)

	_, stderr, code := testutil.RunTicketIn(t, beadsDir,
		"agent", "set-status", "waiting", "--source", "exit-plan-mode")
	if code != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", code, stderr)
	}

	got := loadOne(t, beadsDir, ticket)
	if got.WaitingReasonSource != agent.WaitingReasonSourceExitPlanMode {
		t.Errorf("WaitingReasonSource = %q, want %q",
			got.WaitingReasonSource, agent.WaitingReasonSourceExitPlanMode)
	}
	if got.WaitingReason != plan {
		t.Errorf("WaitingReason = %q, want plan body %q", got.WaitingReason, plan)
	}
	summary := agent.FormatWaitingReason(got)
	if summary != "plan: Refactor the auth flow" {
		t.Errorf("FormatWaitingReason = %q, want \"plan: Refactor the auth flow\"", summary)
	}
}

// TestSetStatusCLIStopFailureSource: stdin JSON with matcher (and optional
// message) renders as the stored reason; the truncated column shows the
// bare error type when a matcher is set.
func TestSetStatusCLIStopFailureSource(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "setstatus-source-stopfail", "x", "")

	sid := "abababab-abab-4bab-8bab-ababababab12"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	payload := `{"session_id":"` + sid + `","hook_event_name":"StopFailure","matcher":"rate_limit","message":"API rate limit reached"}`
	pipeStdin(t, payload)

	_, stderr, code := testutil.RunTicketIn(t, beadsDir,
		"agent", "set-status", "waiting", "--source", "stop-failure")
	if code != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", code, stderr)
	}

	got := loadOne(t, beadsDir, ticket)
	if got.WaitingReasonSource != agent.WaitingReasonSourceStopFailure {
		t.Errorf("WaitingReasonSource = %q, want %q",
			got.WaitingReasonSource, agent.WaitingReasonSourceStopFailure)
	}
	if got.WaitingReason != "rate_limit: API rate limit reached" {
		t.Errorf("WaitingReason = %q, want \"rate_limit: API rate limit reached\"", got.WaitingReason)
	}
	// Table cell: per description.md, the stop-failure summary leads with
	// the matcher (no "asking:" / "plan:" prefix).
	summary := agent.FormatWaitingReason(got)
	if !strings.HasPrefix(summary, "rate_limit") {
		t.Errorf("FormatWaitingReason = %q, want it to start with \"rate_limit\"", summary)
	}
}

// TestSetStatusRunningClearsReasonAndSource exercises the round-trip
// described in description.md: a `waiting --source <kind>` populates both
// fields; a follow-up `running` (from UserPromptSubmit) clears both. This
// is what guarantees a stale reason from a previous turn doesn't bleed
// into a "running" row.
func TestSetStatusRunningClearsReasonAndSource(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "setstatus-clear-source", "x", "")

	sid := "abababab-abab-4bab-8bab-ababababab13"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := agent.SetStatus("", beadsDir, sid,
		agent.StatusWaiting, "rate_limit", agent.WaitingReasonSourceStopFailure,
		ticket, 0); err != nil {
		t.Fatalf("SetStatus waiting: %v", err)
	}
	got := loadOne(t, beadsDir, ticket)
	if got.WaitingReason == "" || got.WaitingReasonSource == "" {
		t.Fatalf("after waiting: reason=%q source=%q, want both populated",
			got.WaitingReason, got.WaitingReasonSource)
	}

	if err := agent.SetStatus("", beadsDir, sid,
		agent.StatusRunning, "", "", ticket, 0); err != nil {
		t.Fatalf("SetStatus running: %v", err)
	}
	got = loadOne(t, beadsDir, ticket)
	if got.WaitingReason != "" {
		t.Errorf("WaitingReason = %q, want empty after running", got.WaitingReason)
	}
	if got.WaitingReasonSource != "" {
		t.Errorf("WaitingReasonSource = %q, want empty after running", got.WaitingReasonSource)
	}
}

func TestListAgentsTruncatesWaitingReason(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "list-truncates-reason", "x", "")

	// Reason longer than the column max, with a newline that must be
	// collapsed to a single space in the table view.
	reason := "line one is short\nline two adds enough text that the rendered cell goes past the eighty-character cap that the table view enforces"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: "ccccccc1-cccc-4ccc-8ccc-cccccccccccc",
		PID:       1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status:        agent.StatusWaiting,
		WaitingReason: reason,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket)
	if code != 0 {
		t.Fatalf("agent list: exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "WAITING REASON") {
		t.Errorf("WAITING REASON header missing:\n%s", stdout)
	}
	if !strings.Contains(stdout, "line one is short line two") {
		t.Errorf("collapsed reason missing:\n%s", stdout)
	}
	if strings.Contains(stdout, "\nline two") {
		t.Errorf("multi-line reason leaked into table view:\n%s", stdout)
	}
	if !strings.Contains(stdout, "…") {
		t.Errorf("ellipsis missing for over-long reason:\n%s", stdout)
	}
}

func TestListAgentsOmitsReasonForRunningAgent(t *testing.T) {
	// Only waiting agents own a meaningful reason. A row that's running (or
	// exited) must have a blank cell even if a stale reason somehow survived.
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "list-running-blank-reason", "x", "")

	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: "ccccccc2-cccc-4ccc-8ccc-cccccccccccc",
		PID:       1, Mode: "agent",
		StartedAt:     "2026-05-15T14:00:00Z",
		Env:           "/env",
		Status:        agent.StatusRunning,
		WaitingReason: "stale-reason-should-not-render",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket)
	if code != 0 {
		t.Fatalf("agent list: exit=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "stale-reason-should-not-render") {
		t.Errorf("running row leaked a stale reason:\n%s", stdout)
	}
}

func TestAgentStatusCLIPrintsFullReason(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-status-full-reason", "x", "")

	full := "I just finished applying the rename across all callers.\nNext I'll re-run the suite, but I want you to confirm the surrounding files look right first."
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
		PID:       1, Mode: "agent",
		StartedAt:     "2026-05-15T14:00:00Z",
		Env:           "/env",
		Status:        agent.StatusWaiting,
		WaitingReason: full,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir,
		"agent", "status", ticket, "dddddddd-dddd-4ddd-8ddd-dddddddddddd")
	if code != 0 {
		t.Fatalf("agent status: exit=%d stderr=%q", code, stderr)
	}
	// Full reason, including its embedded newline, must appear verbatim.
	if !strings.Contains(stdout, full) {
		t.Errorf("untruncated reason missing from output:\n%s", stdout)
	}
	for _, want := range []string{"dddddddd-dddd-4ddd-8ddd-dddddddddddd", "agent", "waiting"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("expected %q in output:\n%s", want, stdout)
		}
	}
}

func TestAgentStatusCLIRejectsUnknownSession(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-status-typo", "x", "")

	known := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: known, PID: 999999, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, stderr, code := testutil.RunTicketIn(t, beadsDir,
		"agent", "status", ticket, "ffffffff-ffff-4fff-8fff-ffffffffffff")
	if code == 0 {
		t.Fatalf("agent status with bad id: want non-zero exit, got 0 (stderr=%q)", stderr)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("error %q should say 'not found'", stderr)
	}
	if !strings.Contains(stderr, known) {
		t.Errorf("error %q should list the valid session id %q", stderr, known)
	}
}

func TestAgentStatusCLIJSONShape(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-status-json", "x", "")

	sid := "f1f1f1f1-f1f1-4f1f-8f1f-f1f1f1f1f1f1"
	reason := "Waiting on user input for X."
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt:     "2026-05-15T14:00:00Z",
		Env:           "/env",
		Status:        agent.StatusWaiting,
		WaitingReason: reason,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir,
		"agent", "status", ticket, sid, "--json")
	if code != 0 {
		t.Fatalf("agent status --json: exit=%d stderr=%q", code, stderr)
	}
	var got agent.AgentEntry
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse json: %v\n%s", err, stdout)
	}
	if got.SessionID != sid {
		t.Errorf("SessionID = %q, want %q", got.SessionID, sid)
	}
	if got.WaitingReason != reason {
		t.Errorf("WaitingReason = %q, want %q", got.WaitingReason, reason)
	}
}

func TestAgentListJSONExposesWaitingReason(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-list-json-reason", "x", "")

	reason := "Need confirmation before I drop the old column."
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: "aaaa1111-aaaa-4aaa-8aaa-aaaa11111111", PID: 1, Mode: "agent",
		StartedAt:     "2026-05-15T14:00:00Z",
		Env:           "/env",
		Status:        agent.StatusWaiting,
		WaitingReason: reason,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket, "--json")
	if code != 0 {
		t.Fatalf("agent list --json: exit=%d stderr=%q", code, stderr)
	}
	var entries []agent.AgentEntry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("parse json: %v\n%s", err, stdout)
	}
	if len(entries) != 1 || entries[0].WaitingReason != reason {
		t.Errorf("entries = %+v, want one with WaitingReason=%q", entries, reason)
	}
}

// --- hooks ---

func TestInstallStatusHooksCreatesFreshFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	t.Setenv("CLAUDE_SETTINGS_PATH", path)

	if err := agent.InstallStatusHooks(); err != nil {
		t.Fatalf("InstallStatusHooks: %v", err)
	}

	settings := readHookSettings(t, path)
	assertHookContains(t, settings, "Stop", "ticket agent set-status waiting")
	assertHookContains(t, settings, "UserPromptSubmit", "ticket agent set-status running")
	assertHookContains(t, settings, "StopFailure", "ticket agent set-status waiting --source stop-failure")
	assertHookContainsMatcher(t, settings, "PreToolUse", "AskUserQuestion",
		"ticket agent set-status waiting --source ask-user-question")
	assertHookContainsMatcher(t, settings, "PreToolUse", "ExitPlanMode",
		"ticket agent set-status waiting --source exit-plan-mode")
}

func TestInstallStatusHooksIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	t.Setenv("CLAUDE_SETTINGS_PATH", path)

	for i := range 3 {
		if err := agent.InstallStatusHooks(); err != nil {
			t.Fatalf("InstallStatusHooks iter %d: %v", i, err)
		}
	}

	settings := readHookSettings(t, path)
	// Per-event expected count of ticket-agent-set-status commands across
	// the event's matcher blocks. PreToolUse has two (AskUserQuestion,
	// ExitPlanMode); the others have one matcher-less block each.
	expected := map[string]int{
		"Stop":             1,
		"UserPromptSubmit": 1,
		"PreToolUse":       2,
		"StopFailure":      1,
	}
	for event, want := range expected {
		blocks := settings["hooks"].(map[string]any)[event].([]any)
		matches := 0
		for _, blk := range blocks {
			inner := blk.(map[string]any)["hooks"].([]any)
			for _, h := range inner {
				m := h.(map[string]any)
				if m["type"] == "command" && strings.HasPrefix(m["command"].(string), "ticket agent set-status") {
					matches++
				}
			}
		}
		if matches != want {
			t.Errorf("event %s: want exactly %d status hook(s) after 3 installs, got %d", event, want, matches)
		}
	}
}

func TestInstallStatusHooksPreservesUnrelatedKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	t.Setenv("CLAUDE_SETTINGS_PATH", path)

	existing := `{
  "permissions": {"additionalDirectories": ["/some/dir"]},
  "hooks": {
    "PreToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "echo before"}]}
    ],
    "StopFailure": [
      {"matcher": "rate_limit", "hooks": [{"type": "command", "command": "echo ratelimit"}]}
    ]
  }
}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	if err := agent.InstallStatusHooks(); err != nil {
		t.Fatalf("InstallStatusHooks: %v", err)
	}

	settings := readHookSettings(t, path)
	if settings["permissions"] == nil {
		t.Errorf("permissions key dropped:\n%s", mustReadFile(t, path))
	}
	// User's pre-existing matcher blocks must survive verbatim.
	assertHookContainsMatcher(t, settings, "PreToolUse", "Bash", "echo before")
	assertHookContainsMatcher(t, settings, "StopFailure", "rate_limit", "echo ratelimit")
	// And our matcher-aware blocks must be added alongside them.
	assertHookContains(t, settings, "Stop", "ticket agent set-status waiting")
	assertHookContains(t, settings, "UserPromptSubmit", "ticket agent set-status running")
	assertHookContains(t, settings, "StopFailure", "ticket agent set-status waiting --source stop-failure")
	assertHookContainsMatcher(t, settings, "PreToolUse", "AskUserQuestion",
		"ticket agent set-status waiting --source ask-user-question")
	assertHookContainsMatcher(t, settings, "PreToolUse", "ExitPlanMode",
		"ticket agent set-status waiting --source exit-plan-mode")
}

func TestInstallStatusHooksFailsLoudlyOnMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	t.Setenv("CLAUDE_SETTINGS_PATH", path)

	junk := []byte("{ this is not json")
	if err := os.WriteFile(path, junk, 0o644); err != nil {
		t.Fatalf("write junk: %v", err)
	}

	err := agent.InstallStatusHooks()
	if err == nil {
		t.Fatalf("malformed settings: want error, got nil")
	}
	got, _ := os.ReadFile(path)
	if string(got) != string(junk) {
		t.Errorf("malformed settings was rewritten:\n%s", got)
	}
}

// --- helpers ---

func readHookSettings(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse settings: %v\n%s", err, raw)
	}
	return out
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

func assertHookContains(t *testing.T, settings map[string]any, event, command string) {
	t.Helper()
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks key missing or not an object: %#v", settings["hooks"])
	}
	blocks, ok := hooks[event].([]any)
	if !ok {
		t.Fatalf("event %s missing or not an array: %#v", event, hooks[event])
	}
	for _, blk := range blocks {
		inner, ok := blk.(map[string]any)["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range inner {
			m, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if m["type"] == "command" && m["command"] == command {
				return
			}
		}
	}
	t.Errorf("event %s missing command %q. settings:\n%#v", event, command, settings)
}

// assertHookContainsMatcher verifies that settings.hooks[event] contains a
// block whose "matcher" field equals matcher and whose inner hooks include
// command. Used for PreToolUse / StopFailure entries that scope to a
// specific tool name or error type.
func assertHookContainsMatcher(t *testing.T, settings map[string]any, event, matcher, command string) {
	t.Helper()
	hooks, ok := settings["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("hooks key missing or not an object: %#v", settings["hooks"])
	}
	blocks, ok := hooks[event].([]any)
	if !ok {
		t.Fatalf("event %s missing or not an array: %#v", event, hooks[event])
	}
	for _, blk := range blocks {
		entry, ok := blk.(map[string]any)
		if !ok {
			continue
		}
		got, _ := entry["matcher"].(string)
		if got != matcher {
			continue
		}
		inner, ok := entry["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range inner {
			m, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if m["type"] == "command" && m["command"] == command {
				return
			}
		}
	}
	t.Errorf("event %s matcher %q missing command %q. settings:\n%#v",
		event, matcher, command, settings)
}
