package ticketcli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/env"
	"framework2/internal/testutil"
	"framework2/internal/ticketcli"
)

// stubExecClaude redirects the bare-claude fallback for the lifetime of t and
// returns the recorded argv slices. Global state — no t.Parallel.
func stubExecClaude(t *testing.T) *[][]string {
	t.Helper()
	prev := agent.ExecClaude
	var calls [][]string
	agent.ExecClaude = func(args []string) error {
		calls = append(calls, args)
		return nil
	}
	t.Cleanup(func() { agent.ExecClaude = prev })
	return &calls
}

// pinCwd points the .ticket-id walk-up at dir for the lifetime of t.
func pinCwd(t *testing.T, dir string) {
	t.Helper()
	prev := ticketcli.CwdForDiscovery
	ticketcli.CwdForDiscovery = func() (string, error) { return dir, nil }
	t.Cleanup(func() { ticketcli.CwdForDiscovery = prev })
}

// Outside a ticket env there is nothing framework-shaped to resume, so both
// commands must degrade to the plain claude launch the `cc` / `cr` aliases
// performed before this existed.
func TestAgentContinueOutsideTicketEnvFallsBackToClaude(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	calls := stubExecClaude(t)
	pinCwd(t, t.TempDir())

	for _, sub := range []string{"continue", "resume"} {
		_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", sub)
		if code != 0 {
			t.Fatalf("agent %s outside env: exit=%d stderr=%q", sub, code, stderr)
		}
	}
	if len(*calls) != 2 {
		t.Fatalf("claude fallback invoked %d times, want 2: %v", len(*calls), *calls)
	}
	if got := (*calls)[0]; len(got) != 1 || got[0] != "--continue" {
		t.Errorf("continue fallback args = %v, want [--continue]", got)
	}
	if got := (*calls)[1]; len(got) != 1 || got[0] != "--resume" {
		t.Errorf("resume fallback args = %v, want [--resume]", got)
	}
}

// Inside an env folder the ticket is resolved from the .ticket-id marker, with
// no id on the command line — the shape the `cc` alias uses. A bead with no
// recorded sessions must refuse rather than fall back to a bare claude launch
// (that would skip the wrapper's hooks and system prompt).
func TestAgentContinueDiscoversTicketFromCwd(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "cc-discovers-cwd", "x", "")
	envPath, err := env.Create("", beadsDir, ticket)
	if err != nil {
		t.Fatalf("env.Create: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(beadsDir, "envs", ticket)) })

	calls := stubExecClaude(t)
	pinCwd(t, envPath)

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "continue")
	if code == 0 {
		t.Fatal("agent continue on a ticket with no sessions succeeded; want error")
	}
	if !strings.Contains(stderr, ticket) {
		t.Errorf("stderr = %q, want to name the discovered ticket %q", stderr, ticket)
	}
	if !strings.Contains(stderr, "no recorded agent sessions") {
		t.Errorf("stderr = %q, want to mention 'no recorded agent sessions'", stderr)
	}
	if len(*calls) != 0 {
		t.Errorf("claude fallback invoked inside a ticket env: %v", *calls)
	}
}

// The picker must be fed exactly the rows `agent list` prints — that parity is
// the ticket's requirement, so assert against the real `agent list` output.
func TestAgentResumePickerSeesAgentListRows(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "cr-picker-rows", "x", "")

	for i, sid := range []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
	} {
		mode := "agent"
		if i == 1 {
			mode = "spec"
		}
		if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
			SessionID: sid, PID: 100 + i, Mode: mode,
			StartedAt: "2026-08-11T09:00:00Z", Env: t.TempDir(),
			Status: agent.StatusWaiting, StatusUpdatedAt: "2026-08-11T09:00:00Z",
		}); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}

	wantTable, _, code := testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket)
	if code != 0 {
		t.Fatalf("agent list: exit=%d", code)
	}

	prev := agent.ExecPicker
	var gotTable string
	agent.ExecPicker = func(table string, args []string) (string, error) {
		gotTable = table
		// Abort the picker: the resume itself would exec claude.
		return "", agent.ErrPickCancelled
	}
	t.Cleanup(func() { agent.ExecPicker = prev })

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "resume", ticket)
	if code != 0 {
		t.Fatalf("agent resume: exit=%d stderr=%q", code, stderr)
	}
	if gotTable != wantTable {
		t.Errorf("picker input drifted from `agent list`:\n picker = %q\n list   = %q", gotTable, wantTable)
	}
}

// An explicit id (and the --ticket flag `this` supplies) wins over cwd
// discovery, and a trailing unknown positional is a usage error.
func TestAgentContinueRejectsExtraPositional(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "cc-extra-positional", "x", "")
	stubExecClaude(t)
	pinCwd(t, t.TempDir())

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "--ticket", ticket, "agent", "continue", "extra")
	if code == 0 {
		t.Fatal("agent continue with an extra positional succeeded; want usage error")
	}
	if !strings.Contains(stderr, "usage:") {
		t.Errorf("stderr = %q, want usage line", stderr)
	}
}
