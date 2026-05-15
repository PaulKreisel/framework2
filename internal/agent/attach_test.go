package agent_test

import (
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/testutil"
)

// stubOsascript redirects ExecOsascript to a recorder for the lifetime of t.
// Returns the recorder slice the test can inspect. The recorder is appended
// to under a mutex so callers can stub concurrent flows safely.
type osaCall struct {
	script string
	out    string
	err    error
}

func stubOsascript(t *testing.T, reply func(script string) (string, error)) *[]osaCall {
	t.Helper()
	prev := agent.ExecOsascript
	var mu sync.Mutex
	var calls []osaCall
	agent.ExecOsascript = func(script string) (string, error) {
		out, err := reply(script)
		mu.Lock()
		calls = append(calls, osaCall{script: script, out: out, err: err})
		mu.Unlock()
		return out, err
	}
	t.Cleanup(func() { agent.ExecOsascript = prev })
	return &calls
}

func stubLookupTTY(t *testing.T, fn func(pid int) (string, error)) {
	t.Helper()
	prev := agent.LookupTTY
	agent.LookupTTY = fn
	t.Cleanup(func() { agent.LookupTTY = prev })
}

func TestAttachUnknownSessionReportsKnownIDs(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "attach-unknown", "x", "")

	known := "11111111-1111-4111-8111-111111111111"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: known, PID: 1, Mode: "agent", StartedAt: "2026-05-15T14:56:00Z", Env: t.TempDir(),
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	err := agent.Attach("", beadsDir, ticket, "22222222-2222-4222-8222-222222222222")
	if err == nil {
		t.Fatal("Attach to unknown session succeeded; want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "not found") {
		t.Errorf("error = %q, want to mention 'not found'", msg)
	}
	if !strings.Contains(msg, known) {
		t.Errorf("error = %q, want to list known session id %q", msg, known)
	}
}

func TestAttachNoRecordedSessionsErrors(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "attach-empty", "x", "")

	err := agent.Attach("", beadsDir, ticket, "00000000-0000-4000-8000-000000000000")
	if err == nil {
		t.Fatal("Attach on bead with no records succeeded; want error")
	}
	if !strings.Contains(err.Error(), "no recorded agent sessions") {
		t.Errorf("error = %q, want to mention 'no recorded agent sessions'", err.Error())
	}
}

func TestAttachRequiresSessionID(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "attach-no-sid", "x", "")

	err := agent.Attach("", beadsDir, ticket, "")
	if err == nil {
		t.Fatal("Attach with empty session id succeeded; want error")
	}
	if !strings.Contains(err.Error(), "session id required") {
		t.Errorf("error = %q, want to mention 'session id required'", err.Error())
	}
}

// TestAttach*FocusOr*Spawn tests stub package-level vars (ExecOsascript,
// LookupTTY); they share global state and must not run alongside each other
// under t.Parallel. They still use TestDB for per-test bd isolation.
func TestAttachFocusesRunningWrapper(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("focus path is macOS-only; GOOS=%s", runtime.GOOS)
	}
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "attach-focus-running", "x", "")

	sid := "33333333-3333-4333-8333-333333333333"
	// PID 1 (init) is always alive — Attach treats this as running.
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:56:00Z", Env: t.TempDir(),
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stubLookupTTY(t, func(pid int) (string, error) {
		if pid != 1 {
			t.Errorf("LookupTTY called with pid=%d, want 1", pid)
		}
		return "ttys999", nil
	})
	calls := stubOsascript(t, func(script string) (string, error) {
		if !strings.Contains(script, "/dev/ttys999") {
			t.Errorf("osascript missing /dev/ttys999 reference:\n%s", script)
		}
		return "OK\n", nil
	})

	if err := agent.Attach("", beadsDir, ticket, sid); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("osascript invoked %d times, want 1", len(*calls))
	}
}

func TestAttachFocusErrorsWhenNoTab(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("focus path is macOS-only; GOOS=%s", runtime.GOOS)
	}
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "attach-focus-notab", "x", "")

	sid := "44444444-4444-4444-8444-444444444444"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:56:00Z", Env: t.TempDir(),
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	stubLookupTTY(t, func(int) (string, error) { return "ttys999", nil })
	stubOsascript(t, func(string) (string, error) { return "NOT_FOUND\n", nil })

	err := agent.Attach("", beadsDir, ticket, sid)
	if err == nil {
		t.Fatal("Attach with NOT_FOUND succeeded; want error")
	}
	if !strings.Contains(err.Error(), "backgrounded") {
		t.Errorf("error = %q, want to mention 'backgrounded'", err.Error())
	}
}

func TestAttachFocusErrorsWhenNoTTY(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("focus path is macOS-only; GOOS=%s", runtime.GOOS)
	}
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "attach-focus-notty", "x", "")

	sid := "55555555-5555-4555-8555-555555555555"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:56:00Z", Env: t.TempDir(),
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	stubLookupTTY(t, func(int) (string, error) { return "??", nil })
	osaCalled := false
	stubOsascript(t, func(string) (string, error) {
		osaCalled = true
		return "OK", nil
	})

	err := agent.Attach("", beadsDir, ticket, sid)
	if err == nil {
		t.Fatal("Attach with no tty succeeded; want error")
	}
	if !strings.Contains(err.Error(), "no controlling terminal") {
		t.Errorf("error = %q, want to mention 'no controlling terminal'", err.Error())
	}
	if osaCalled {
		t.Errorf("osascript should not run when no tty is available")
	}
}

func TestAttachFocusesWaitingWrapper(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("focus path is macOS-only; GOOS=%s", runtime.GOOS)
	}
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "attach-focus-waiting", "x", "")

	sid := "66666666-6666-4666-8666-666666666666"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:56:00Z", Env: t.TempDir(),
		Status: agent.StatusWaiting,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	stubLookupTTY(t, func(int) (string, error) { return "ttys777", nil })
	stubOsascript(t, func(string) (string, error) { return "OK", nil })

	if err := agent.Attach("", beadsDir, ticket, sid); err != nil {
		t.Fatalf("Attach (waiting): %v", err)
	}
}

func TestAttachSpawnsResumeWindowForExited(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("spawn path is macOS-only; GOOS=%s", runtime.GOOS)
	}
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "attach-spawn-exited", "x", "")

	envPath := t.TempDir()
	sid := "77777777-7777-4777-8777-777777777777"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 999999, Mode: "agent",
		StartedAt: "2026-05-15T14:56:00Z", Env: envPath,
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	calls := stubOsascript(t, func(string) (string, error) { return "", nil })

	if err := agent.Attach("", beadsDir, ticket, sid); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("osascript invoked %d times, want 1", len(*calls))
	}
	script := (*calls)[0].script
	if !strings.Contains(script, "do script") {
		t.Errorf("spawn script missing 'do script':\n%s", script)
	}
	if !strings.Contains(script, "--resume "+shellSingleQuote(sid)) {
		t.Errorf("spawn script missing '--resume %s':\n%s", sid, script)
	}
	if !strings.Contains(script, "cd "+shellSingleQuote(envPath)) {
		t.Errorf("spawn script missing cd to env path %s:\n%s", envPath, script)
	}
	if !strings.Contains(script, "--ticket "+shellSingleQuote(ticket)) {
		t.Errorf("spawn script missing --ticket %s:\n%s", ticket, script)
	}
}

func TestAttachExitedWithMissingEnvFails(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("spawn path is macOS-only; GOOS=%s", runtime.GOOS)
	}
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "attach-spawn-no-env", "x", "")

	envPath := t.TempDir()
	sid := "88888888-8888-4888-8888-888888888888"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 999999, Mode: "agent",
		StartedAt: "2026-05-15T14:56:00Z", Env: envPath,
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := os.RemoveAll(envPath); err != nil {
		t.Fatalf("remove env: %v", err)
	}
	osaCalled := false
	stubOsascript(t, func(string) (string, error) { osaCalled = true; return "", nil })

	err := agent.Attach("", beadsDir, ticket, sid)
	if err == nil {
		t.Fatal("Attach with missing env succeeded; want error")
	}
	if !strings.Contains(err.Error(), "no longer exists") {
		t.Errorf("error = %q, want to mention 'no longer exists'", err.Error())
	}
	if osaCalled {
		t.Errorf("osascript should not run when env is missing")
	}
}

func TestUpsertAgentReplacesExisting(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "attach-upsert", "x", "")

	sid := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	first := agent.AgentEntry{
		SessionID: sid, PID: 999999, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/old-env",
		Status: agent.StatusRunning, StatusUpdatedAt: "2026-05-15T14:00:00Z",
	}
	if err := agent.RegisterAgent("", beadsDir, ticket, first); err != nil {
		t.Fatalf("Register: %v", err)
	}
	second := agent.AgentEntry{
		SessionID: sid, PID: 12345, Mode: "agent",
		StartedAt: "2026-05-15T15:00:00Z", Env: "/new-env",
		Status: agent.StatusRunning, StatusUpdatedAt: "2026-05-15T15:00:00Z",
	}
	if err := agent.UpsertAgent("", beadsDir, ticket, second); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := agent.LoadAgents("", beadsDir, ticket)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 entry after upsert, got %d: %+v", len(got), got)
	}
	if got[0] != second {
		t.Errorf("entry = %+v, want %+v", got[0], second)
	}
}

func TestUpsertAgentAppendsWhenAbsent(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "attach-upsert-new", "x", "")

	e := agent.AgentEntry{
		SessionID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		PID:       1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
	}
	if err := agent.UpsertAgent("", beadsDir, ticket, e); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	got, err := agent.LoadAgents("", beadsDir, ticket)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if len(got) != 1 || got[0] != e {
		t.Errorf("entries = %+v, want [%+v]", got, e)
	}
}

func TestAttachCLINoArg(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "attach-cli-noarg", "x", "")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "attach", ticket)
	if code == 0 {
		t.Fatalf("agent attach <id> (no session) succeeded; want error")
	}
	if !strings.Contains(stderr, "usage:") {
		t.Errorf("stderr = %q, want usage line", stderr)
	}
}

// shellSingleQuote mirrors the (unexported) shellQuote helper in attach.go
// so the spawn-script assertions don't have to pin the exact escape rules.
// The session ids and env paths used in tests never contain single quotes,
// so a straight wrap is sufficient.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
