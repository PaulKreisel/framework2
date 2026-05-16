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
		agent.StatusWaiting, ticket, 0); err != nil {
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
		agent.StatusWaiting, ticket, 0); err != nil {
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
		agent.StatusRunning, ticket, 0)
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
		agent.StatusWaiting, "", 0); err != nil {
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

func TestListAgentsExitedWinsOverStoredStatus(t *testing.T) {
	// If the wrapper process is dead, a stale Status="running" must not mask
	// that — stateOf collapses to "exited" so triage stays accurate.
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "list-exited-wins", "x", "")

	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: "55555555-5555-4555-8555-555555555555",
		PID:       999999, // not in use
		Mode:      "agent",
		StartedAt: "2026-05-15T14:56:00Z",
		Env:       "/env-zombie",
		Status:    agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket)
	if code != 0 {
		t.Fatalf("agent list: exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "exited") {
		t.Errorf("dead wrapper not reported exited:\n%s", stdout)
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
	for _, event := range []string{"Stop", "UserPromptSubmit"} {
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
		if matches != 1 {
			t.Errorf("event %s: want exactly 1 status hook after 3 installs, got %d", event, matches)
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
	hooks := settings["hooks"].(map[string]any)
	if hooks["PreToolUse"] == nil {
		t.Errorf("PreToolUse hook dropped:\n%s", mustReadFile(t, path))
	}
	assertHookContains(t, settings, "Stop", "ticket agent set-status waiting")
	assertHookContains(t, settings, "UserPromptSubmit", "ticket agent set-status running")
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
