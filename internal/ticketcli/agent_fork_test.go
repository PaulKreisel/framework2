// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package ticketcli_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"framework2/internal/agent"
	"framework2/internal/env"
	"framework2/internal/testutil"
)

// Outside a ticket env `cf` must be the plain `claude --resume --fork-session`.
func TestAgentForkOutsideTicketEnvFallsBackToClaude(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	calls := stubExecClaude(t)
	pinCwd(t, t.TempDir())

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "fork")
	if code != 0 {
		t.Fatalf("agent fork outside env: exit=%d stderr=%q", code, stderr)
	}
	if len(*calls) != 1 {
		t.Fatalf("claude fallback invoked %d times, want 1: %v", len(*calls), *calls)
	}
	if got := (*calls)[0]; strings.Join(got, " ") != "--resume --fork-session" {
		t.Errorf("fork fallback args = %v, want [--resume --fork-session]", got)
	}
}

// Inside an env with no sessions there is nothing to fork; refuse rather
// than fall back to a bare claude.
func TestAgentForkWithoutSessionsErrors(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "cf-no-sessions", "x", "")
	envPath, err := env.Create("", beadsDir, ticket)
	if err != nil {
		t.Fatalf("env.Create: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(beadsDir, "envs", ticket)) })
	calls := stubExecClaude(t)
	pinCwd(t, envPath)

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "fork")
	if code == 0 {
		t.Fatal("agent fork on a ticket with no sessions succeeded; want error")
	}
	if !strings.Contains(stderr, "no recorded agent sessions") {
		t.Errorf("stderr = %q, want to mention 'no recorded agent sessions'", stderr)
	}
	if len(*calls) != 0 {
		t.Errorf("claude fallback invoked inside a ticket env: %v", *calls)
	}
}

// The fork picker is fed exactly the rows `agent list` prints, like the
// resume picker, and aborting it is a quiet exit 0.
func TestAgentForkPickerSeesAgentListRowsAndAbortIsNoOp(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "cf-picker-rows", "x", "")
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: "11111111-1111-4111-8111-111111111111", PID: 100, Mode: "agent",
		StartedAt: "2026-08-11T09:00:00Z", Env: t.TempDir(),
		Status: agent.StatusWaiting, StatusUpdatedAt: "2026-08-11T09:00:00Z",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	wantTable, _, code := testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket)
	if code != 0 {
		t.Fatalf("agent list: exit=%d", code)
	}

	prev := agent.ExecPicker
	var gotTable string
	agent.ExecPicker = func(table string, args []string) (string, error) {
		gotTable = table
		return "", agent.ErrPickCancelled
	}
	t.Cleanup(func() { agent.ExecPicker = prev })

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "fork", ticket)
	if code != 0 {
		t.Fatalf("agent fork (aborted picker): exit=%d stderr=%q, want quiet exit 0", code, stderr)
	}
	if gotTable != wantTable {
		t.Errorf("picker input drifted from `agent list`:\n picker = %q\n list   = %q", gotTable, wantTable)
	}
	entries, err := agent.LoadAgents("", beadsDir, ticket)
	if err != nil || len(entries) != 1 {
		t.Errorf("aborted fork changed the records: %v, %v", entries, err)
	}
}

// `agent list` surfaces a fork's parent: an 8-char prefix in the table, the
// full id in --json and in `agent status`.
func TestAgentListShowsForkParent(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "cf-list-parent", "x", "")
	parent := "aaaaaaaa-1111-4111-8111-111111111111"
	fork := "bbbbbbbb-2222-4222-8222-222222222222"
	for _, e := range []agent.AgentEntry{
		{SessionID: parent, PID: 100, Mode: "agent", StartedAt: "2026-08-11T09:00:00Z", Env: t.TempDir()},
		{SessionID: fork, PID: 101, Mode: "agent", StartedAt: "2026-08-11T10:00:00Z", Env: t.TempDir(), ForkedFrom: parent},
	} {
		if err := agent.RegisterAgent("", beadsDir, ticket, e); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}

	table, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket)
	if code != 0 {
		t.Fatalf("agent list: exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(table, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("agent list printed %d lines, want header + 2 rows:\n%s", len(lines), table)
	}
	if !strings.Contains(lines[0], "FORKED FROM") {
		t.Errorf("header = %q, want a FORKED FROM column", lines[0])
	}
	var forkRow, parentRow string
	for _, l := range lines[1:] {
		switch {
		case strings.HasPrefix(l, fork):
			forkRow = l
		case strings.HasPrefix(l, parent):
			parentRow = l
		}
	}
	if forkRow == "" || parentRow == "" {
		t.Fatalf("missing rows in:\n%s", table)
	}
	if !strings.Contains(forkRow, "  aaaaaaaa  ") {
		t.Errorf("fork row = %q, want the parent prefix 'aaaaaaaa' in FORKED FROM", forkRow)
	}
	// The parent's only "aaaaaaaa" is its own session id: its FORKED FROM
	// cell is blank.
	if strings.Count(parentRow, "aaaaaaaa") != 1 {
		t.Errorf("parent row = %q, must not list a parent", parentRow)
	}

	jsonOut, _, code := testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket, "--json")
	if code != 0 || !strings.Contains(jsonOut, `"forkedFrom": "`+parent+`"`) {
		t.Errorf("agent list --json = %q, want forkedFrom %q", jsonOut, parent)
	}
	status, _, code := testutil.RunTicketIn(t, beadsDir, "agent", "status", ticket, fork)
	if code != 0 || !strings.Contains(status, "Forked From: "+parent) {
		t.Errorf("agent status = %q, want 'Forked From: %s'", status, parent)
	}
	status, _, code = testutil.RunTicketIn(t, beadsDir, "agent", "status", ticket, parent)
	if code != 0 || strings.Contains(status, "Forked From") {
		t.Errorf("agent status (parent) = %q, must not print Forked From", status)
	}
}

// `ticket dir worktree <repo>` for a repo the ticket already has: exit 0,
// no second entry, no new worktree — but the live wrapper is still
// relaunched so the calling session (which may not be the one that linked
// the repo) picks the directory up.
func TestDirWorktreeReAddRelaunchesWithoutCreating(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "wt-readd-relaunch")
	bare := bareRepoMain(t)
	envPath, err := env.Create("", beadsDir, ticket)
	if err != nil {
		t.Fatalf("env.Create: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(beadsDir, "envs", ticket)) })

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare); code != 0 {
		t.Fatalf("first worktree: exit=%d stderr=%q", code, stderr)
	}
	before, _, _ := testutil.RunTicketIn(t, beadsDir, "dir", "list", ticket)
	wtDir := filepath.Dir(bare)
	entriesBefore, _ := os.ReadDir(wtDir)

	// A live stand-in for the wrapper's claude child.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	if err := os.WriteFile(filepath.Join(envPath, agent.PIDFile), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare)
	if code != 0 {
		t.Fatalf("re-add worktree: exit=%d stderr=%q, want 0", code, stderr)
	}

	after, _, _ := testutil.RunTicketIn(t, beadsDir, "dir", "list", ticket)
	if after != before {
		t.Errorf("re-add changed the linked dirs:\n before=%s\n after=%s", before, after)
	}
	entriesAfter, _ := os.ReadDir(wtDir)
	if len(entriesAfter) != len(entriesBefore) {
		t.Errorf("re-add created something next to the bare repo: before=%d after=%d entries", len(entriesBefore), len(entriesAfter))
	}

	body, err := os.ReadFile(filepath.Join(envPath, agent.SignalFile))
	if err != nil {
		t.Fatalf("re-add did not write the relaunch signal: %v", err)
	}
	if want := agent.DefaultRelaunchPrompt + "\n"; string(body) != want {
		t.Errorf("signal body = %q, want %q", string(body), want)
	}
	waitErr := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("sleep returned %v; expected exit-error from SIGTERM", waitErr)
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && (!ws.Signaled() || ws.Signal() != syscall.SIGTERM) {
		t.Errorf("sleep exit status = %#v; want killed by SIGTERM (the relaunch)", ws)
	}
}
