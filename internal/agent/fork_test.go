// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package agent_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"framework2/internal/agent"
	"framework2/internal/testutil"
)

// fakeClaudeArgv swaps AgentCommand for a shell script that dumps its argv,
// one arg per line, into the returned file. Global state — no t.Parallel.
func fakeClaudeArgv(t *testing.T) string {
	t.Helper()
	argvFile := filepath.Join(t.TempDir(), "argv")
	script := fmt.Sprintf(`printf "%%s\n" "$@" > %q`, argvFile)
	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"
	agent.AgentDefaultArgs = []string{"-c", script, "fake-claude"}
	return argvFile
}

func readArgv(t *testing.T, argvFile string) []string {
	t.Helper()
	body, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	return strings.Split(strings.TrimRight(string(body), "\n"), "\n")
}

func waitRun(t *testing.T, what string, run func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("%s hung", what)
	}
}

func indexOf(argv []string, want string) int {
	for i, a := range argv {
		if a == want {
			return i
		}
	}
	return -1
}

// TestPickAndForkLaunchesForkOfPickedSession drives `ticket agent fork`'s
// core: the picked (spec-mode) session is forked, not resumed. The fake
// claude must receive `--resume <parent> --fork-session --session-id <new>`
// with a new id, no kickoff prompt, and a new record carrying ForkedFrom
// must sit next to the untouched parent record. The bead is not claimed.
func TestPickAndForkLaunchesForkOfPickedSession(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "fork-picked-session", "x", "")
	envPath := mustEnv(t, beadsDir, ticket)

	parent := "55555555-5555-4555-8555-555555555555"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: parent, PID: 1, Mode: "spec",
		StartedAt: "2026-08-11T09:00:00Z", Env: envPath,
		Status: agent.StatusWaiting, StatusUpdatedAt: "2026-08-11T09:00:00Z",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	argvFile := fakeClaudeArgv(t)
	row := parent + "  spec  1  2026-08-11 09:00  waiting"
	stubPicker(t, func(string) (string, error) { return row, nil })

	waitRun(t, "PickAndFork", func() error {
		return agent.PickAndFork("", beadsDir, ticket, "SESSION ID  MODE\n"+row+"\n")
	})

	argv := readArgv(t, argvFile)
	if !containsPair(argv, "--resume", parent) {
		t.Errorf("argv = %v, want `--resume %s`", argv, parent)
	}
	if indexOf(argv, "--fork-session") < 0 {
		t.Errorf("argv = %v, want --fork-session", argv)
	}
	sid := indexOf(argv, "--session-id")
	if sid < 0 || sid+1 >= len(argv) {
		t.Fatalf("argv = %v, want `--session-id <new>`", argv)
	}
	newID := argv[sid+1]
	if newID == parent {
		t.Errorf("fork reused the parent's session id %s; want a fresh one", parent)
	}
	if strings.Contains(strings.Join(argv, "\n"), agent.InitialAgentPrompt) {
		t.Errorf("fork must not pass InitialAgentPrompt; argv:\n%s", strings.Join(argv, "\n"))
	}
	if argv[len(argv)-1] != newID {
		t.Errorf("trailing argv = %q, want the --session-id value (no kickoff prompt)", argv[len(argv)-1])
	}

	entries, err := agent.LoadAgents("", beadsDir, ticket)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d records, want 2 (parent + fork): %+v", len(entries), entries)
	}
	if entries[0].SessionID != parent || entries[0].ForkedFrom != "" {
		t.Errorf("parent record changed: %+v", entries[0])
	}
	fork := entries[1]
	if fork.SessionID != newID {
		t.Errorf("fork record session id = %q, want %q (the --session-id passed to claude)", fork.SessionID, newID)
	}
	if fork.ForkedFrom != parent {
		t.Errorf("fork record ForkedFrom = %q, want %q", fork.ForkedFrom, parent)
	}
	if fork.Mode != "spec" {
		t.Errorf("fork record Mode = %q, want spec (inherited from the parent)", fork.Mode)
	}
	if len(fork.Conversation) != 0 {
		t.Errorf("fork conversation = %+v, want empty (parent log is not copied)", fork.Conversation)
	}
	if got := readStatus(t, beadsDir, ticket); got != "open" {
		t.Errorf("bead status = %q after fork, want open (fork does not claim)", got)
	}
}

func TestForkSessionUnknownSessionReportsKnownIDs(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "fork-unknown-sid", "x", "")

	known := "66666666-6666-4666-8666-666666666666"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: known, PID: 1, Mode: "agent",
		StartedAt: "2026-08-11T09:00:00Z", Env: t.TempDir(),
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	err := agent.ForkSession("", beadsDir, ticket, "77777777-7777-4777-8777-777777777777")
	if err == nil {
		t.Fatal("ForkSession with unknown session id succeeded; want error")
	}
	if !strings.Contains(err.Error(), known) {
		t.Errorf("error = %q, want to list known session id %q", err.Error(), known)
	}
}

func TestRunWithOptionsRejectsResumeAndForkTogether(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "fork-and-resume", "x", "")
	mustEnv(t, beadsDir, ticket)

	err := agent.RunWithOptions("", beadsDir, ticket, agent.ModeAgent, agent.RunOptions{
		ResumeSessionID:   "88888888-8888-4888-8888-888888888888",
		ForkFromSessionID: "99999999-9999-4999-8999-999999999999",
	})
	if err == nil {
		t.Fatal("RunWithOptions with both resume and fork succeeded; want error")
	}
}

// TestRunSecondAgentSkipsClaimAndKickoffPrompt: a fresh `ticket agent` on a
// bead another agent already claimed must launch (no "already in_progress"
// refusal), leave the bead status alone, and open to a blank input instead
// of auto-submitting InitialAgentPrompt.
func TestRunSecondAgentSkipsClaimAndKickoffPrompt(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "second-agent-empty-prompt", "x", "")
	mustEnv(t, beadsDir, ticket)
	if err := agent.Claim("", beadsDir, ticket); err != nil {
		t.Fatalf("first Claim: %v", err)
	}

	argvFile := fakeClaudeArgv(t)
	waitRun(t, "agent.Run (second agent)", func() error {
		return agent.Run("", beadsDir, ticket, agent.ModeAgent)
	})

	argv := readArgv(t, argvFile)
	joined := strings.Join(argv, "\n")
	if strings.Contains(joined, agent.InitialAgentPrompt) {
		t.Errorf("second agent must not auto-submit InitialAgentPrompt; argv:\n%s", joined)
	}
	sid := indexOf(argv, "--session-id")
	if sid < 0 || sid+1 >= len(argv) {
		t.Fatalf("argv = %v, want `--session-id <id>` (fresh session, not a resume)", argv)
	}
	if argv[len(argv)-1] != argv[sid+1] {
		t.Errorf("trailing argv = %q, want the --session-id value (empty first turn)", argv[len(argv)-1])
	}
	if got := readStatus(t, beadsDir, ticket); got != "in_progress" {
		t.Errorf("bead status = %q, want in_progress (untouched by the second agent)", got)
	}
}

// --prompt still wins on a second agent.
func TestRunSecondAgentHonoursExplicitPrompt(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "second-agent-custom-prompt", "x", "")
	mustEnv(t, beadsDir, ticket)
	if err := agent.Claim("", beadsDir, ticket); err != nil {
		t.Fatalf("first Claim: %v", err)
	}

	argvFile := fakeClaudeArgv(t)
	custom := "Review what the other session did"
	waitRun(t, "agent.RunWithOptions (second agent, --prompt)", func() error {
		return agent.RunWithOptions("", beadsDir, ticket, agent.ModeAgent,
			agent.RunOptions{InitialPrompt: custom})
	})

	argv := readArgv(t, argvFile)
	if argv[len(argv)-1] != custom {
		t.Errorf("trailing argv = %q, want the explicit --prompt %q", argv[len(argv)-1], custom)
	}
}

// A first agent on an open bead still claims it and still gets the kickoff
// prompt — the loosened rule only applies once the bead is in_progress.
func TestRunFirstAgentStillClaims(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "first-agent-claims", "x", "")
	mustEnv(t, beadsDir, ticket)

	argvFile := fakeClaudeArgv(t)
	waitRun(t, "agent.Run (first agent)", func() error {
		return agent.Run("", beadsDir, ticket, agent.ModeAgent)
	})

	argv := readArgv(t, argvFile)
	if argv[len(argv)-1] != agent.InitialAgentPrompt {
		t.Errorf("trailing argv = %q, want InitialAgentPrompt", argv[len(argv)-1])
	}
	if got := readStatus(t, beadsDir, ticket); got != "in_progress" {
		t.Errorf("bead status = %q, want in_progress (first agent claims)", got)
	}
}

func TestFormatForkedFrom(t *testing.T) {
	t.Parallel()
	if got := agent.FormatForkedFrom(""); got != "" {
		t.Errorf("FormatForkedFrom(\"\") = %q, want \"\"", got)
	}
	if got := agent.FormatForkedFrom("55555555-5555-4555-8555-555555555555"); got != "55555555" {
		t.Errorf("FormatForkedFrom(uuid) = %q, want 8-char prefix", got)
	}
}
