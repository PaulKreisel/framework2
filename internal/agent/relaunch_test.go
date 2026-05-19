package agent_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"framework2/internal/agent"
	"framework2/internal/testutil"
)

func TestRelaunchWritesSignalAndKillsTarget(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-relaunch-ok", "x", "")
	envPath := mustEnv(t, beadsDir, ticket)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	writePID(t, envPath, cmd.Process.Pid)

	if err := agent.Relaunch(beadsDir, ticket, []string{"--model", "sonnet"}); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(envPath, agent.SignalFile))
	if err != nil {
		t.Fatalf("read signal file: %v", err)
	}
	want := "--model\nsonnet\n"
	if string(body) != want {
		t.Errorf("signal body = %q, want %q", string(body), want)
	}

	waitErr := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("sleep returned %v; expected exit-error from SIGTERM", waitErr)
	}
	if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
		if !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
			t.Errorf("sleep exit status = %#v; want killed by SIGTERM", ws)
		}
	}
}

func TestRelaunchWithNoExtraArgsWritesDefaultPrompt(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-relaunch-empty", "x", "")
	envPath := mustEnv(t, beadsDir, ticket)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	writePID(t, envPath, cmd.Process.Pid)

	if err := agent.Relaunch(beadsDir, ticket, nil); err != nil {
		t.Fatalf("Relaunch: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(envPath, agent.SignalFile))
	if err != nil {
		t.Fatalf("read signal file: %v", err)
	}
	want := agent.DefaultRelaunchPrompt + "\n"
	if string(body) != want {
		t.Errorf("signal body = %q, want %q (default prompt)", string(body), want)
	}
	_ = cmd.Wait()
}

func TestRelaunchFailsWithoutPIDFile(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-relaunch-no-pid", "x", "")
	mustEnv(t, beadsDir, ticket)

	err := agent.Relaunch(beadsDir, ticket, nil)
	if err == nil {
		t.Fatal("Relaunch succeeded without pid file, want error")
	}
	if !strings.Contains(err.Error(), "not running inside ticket agent") {
		t.Errorf("error = %q, want to mention 'not running inside ticket agent'", err.Error())
	}
}

func TestRelaunchFailsWithStalePID(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-relaunch-stale-pid", "x", "")
	envPath := mustEnv(t, beadsDir, ticket)

	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("true: %v", err)
	}
	writePID(t, envPath, cmd.Process.Pid)

	err := agent.Relaunch(beadsDir, ticket, nil)
	if err == nil {
		t.Fatal("Relaunch succeeded against dead pid, want error")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("error = %q, want to mention 'not running'", err.Error())
	}
}

func TestRelaunchIfActiveNoOpsWithoutPIDFile(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-relaunch-ifactive-nopid", "x", "")
	envPath := mustEnv(t, beadsDir, ticket)

	if err := agent.RelaunchIfActive(beadsDir, ticket); err != nil {
		t.Fatalf("RelaunchIfActive without pid file: %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(envPath, agent.SignalFile)); !os.IsNotExist(err) {
		t.Errorf("signal file should not be written when no wrapper is running (err=%v)", err)
	}
}

func TestRelaunchIfActiveNoOpsWithStalePID(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-relaunch-ifactive-stale", "x", "")
	envPath := mustEnv(t, beadsDir, ticket)

	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("true: %v", err)
	}
	writePID(t, envPath, cmd.Process.Pid)

	if err := agent.RelaunchIfActive(beadsDir, ticket); err != nil {
		t.Fatalf("RelaunchIfActive with stale pid: %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(envPath, agent.SignalFile)); !os.IsNotExist(err) {
		t.Errorf("signal file should not be written for stale pid (err=%v)", err)
	}
}

func TestRelaunchIfActiveSignalsLiveWrapper(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-relaunch-ifactive-live", "x", "")
	envPath := mustEnv(t, beadsDir, ticket)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	writePID(t, envPath, cmd.Process.Pid)

	if err := agent.RelaunchIfActive(beadsDir, ticket); err != nil {
		t.Fatalf("RelaunchIfActive: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(envPath, agent.SignalFile))
	if err != nil {
		t.Fatalf("read signal file: %v", err)
	}
	want := agent.DefaultRelaunchPrompt + "\n"
	if string(body) != want {
		t.Errorf("signal body = %q, want %q (default prompt)", string(body), want)
	}
	_ = cmd.Wait()
}

// TestRunLoopRelaunchesOnSignal and TestRunLoopClearsStaleSignalFile mutate
// the package-level agent.AgentCommand / AgentDefaultArgs to substitute a
// fake claude, so they cannot run alongside the other agent tests in
// parallel. They still use TestDB for DB isolation; they just opt out of
// t.Parallel.
func TestRunLoopRelaunchesOnSignal(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-run-loop", "x", "")
	envPath := mustEnv(t, beadsDir, ticket)

	workDir := t.TempDir()
	counter := filepath.Join(workDir, "counter")

	// Fake claude:
	//   - appends one line per invocation: "CONT" if --continue is present
	//     in argv (as its own arg, so the system-prompt text can't false-match),
	//     "PLAIN" otherwise.
	//   - on the first run, writes the signal file (empty body → no extra
	//     args) to ask for a relaunch
	//   - exits 0
	script := fmt.Sprintf(`
if printf "%%s\n" "$@" | grep -q -- "^--continue$"; then
  echo "CONT" >> %q
else
  echo "PLAIN" >> %q
fi
if [ ! -f %q.firstrun ]; then
  : > %q.firstrun
  : > %q
fi
`, counter, counter, counter, counter, filepath.Join(envPath, agent.SignalFile))

	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"
	// Trailing "fake-claude" supplies $0 for the script body; relaunch args
	// from the wrapper become $1, $2, ...
	agent.AgentDefaultArgs = []string{"-c", script, "fake-claude"}

	done := make(chan error, 1)
	go func() { done <- agent.Run("", beadsDir, ticket, agent.ModeAgent) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("agent.Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent.Run did not return within 10s; loop may not be exiting")
	}

	body, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("read counter: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("counter has %d lines, want 2; body=%q", len(lines), string(body))
	}
	if lines[0] != "PLAIN" {
		t.Errorf("first invocation = %q, want PLAIN (no --continue yet)", lines[0])
	}
	if lines[1] != "CONT" {
		t.Errorf("second invocation = %q, want CONT (--continue present)", lines[1])
	}

	if _, err := os.Stat(filepath.Join(envPath, agent.SignalFile)); !os.IsNotExist(err) {
		t.Errorf("signal file still present after loop exit: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(envPath, agent.PIDFile)); !os.IsNotExist(err) {
		t.Errorf("pid file still present after loop exit: err=%v", err)
	}
}

func TestRunLoopClearsStaleSignalFile(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-run-clear-stale", "x", "")
	envPath := mustEnv(t, beadsDir, ticket)

	// Plant a stale signal — if the loop didn't clear it pre-flight, the
	// fake claude's single invocation would be followed by a second one.
	if err := os.WriteFile(filepath.Join(envPath, agent.SignalFile), []byte(""), 0o644); err != nil {
		t.Fatalf("plant stale signal: %v", err)
	}

	workDir := t.TempDir()
	counter := filepath.Join(workDir, "counter")
	// One line per invocation, content irrelevant — we only count.
	script := fmt.Sprintf(`echo X >> %q`, counter)

	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"
	agent.AgentDefaultArgs = []string{"-c", script, "fake-claude"}

	done := make(chan error, 1)
	go func() { done <- agent.Run("", beadsDir, ticket, agent.ModeAgent) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("agent.Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent.Run hung")
	}

	body, _ := os.ReadFile(counter)
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 1 {
		t.Errorf("stale signal caused %d invocations, want 1; body=%q", len(lines), string(body))
	}
}

// TestRunLoopStopsWhenEnvFolderRemoved guards the belt-and-suspenders check
// in the wrapper loop: if `this finish` has removed the env folder before
// claude exited, the loop must return cleanly even when a (stale) signal
// file is still on disk. Mirrors the on-success state where finish has
// rm -rf'd the env but a Relaunch racing in could otherwise plant a new
// signal.
func TestRunLoopStopsWhenEnvFolderRemoved(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-run-loop-env-gone", "x", "")
	envPath := mustEnv(t, beadsDir, ticket)

	workDir := t.TempDir()
	counter := filepath.Join(workDir, "counter")
	// Fake claude: append one line per invocation, then on first run plant
	// a signal AND remove the env folder. The second iteration would
	// relaunch if the wrapper didn't notice the missing env.
	script := fmt.Sprintf(`
echo X >> %q
if [ ! -f %q.firstrun ]; then
  : > %q.firstrun
  : > %q
  rm -rf %q
fi
`, counter, counter, counter, filepath.Join(envPath, agent.SignalFile), envPath)

	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"
	agent.AgentDefaultArgs = []string{"-c", script, "fake-claude"}

	done := make(chan error, 1)
	go func() { done <- agent.Run("", beadsDir, ticket, agent.ModeAgent) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("agent.Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent.Run hung; loop relaunched into a deleted env")
	}

	body, _ := os.ReadFile(counter)
	lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(lines) != 1 {
		t.Errorf("loop ran %d times after env removed, want 1; body=%q", len(lines), string(body))
	}
}

// TestRunSpecModeDoesNotClaim guards that `ticket spec` doesn't flip the bead
// to in_progress: only `ticket agent` owns that status transition.
func TestRunSpecModeDoesNotClaim(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-spec-no-claim", "x", "")
	mustEnv(t, beadsDir, ticket)

	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"
	agent.AgentDefaultArgs = []string{"-c", ":", "fake-claude"}

	done := make(chan error, 1)
	go func() { done <- agent.Run("", beadsDir, ticket, agent.ModeSpec) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("agent.Run (spec): %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent.Run (spec) hung")
	}

	if got := readStatus(t, beadsDir, ticket); got != "open" {
		t.Errorf("after spec Run, status = %q, want open (spec must not claim)", got)
	}
}

// TestRunAgentModeInitialPrompt asserts that a fresh `ticket agent` launch
// passes [agent.InitialAgentPrompt] as the trailing positional argument so
// claude dispatches it as turn 1 instead of opening to a blank input.
func TestRunAgentModeInitialPrompt(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-initial-prompt", "x", "")
	mustEnv(t, beadsDir, ticket)

	workDir := t.TempDir()
	argvFile := filepath.Join(workDir, "argv")
	// Fake claude: dump $@ one arg per line.
	script := fmt.Sprintf(`printf "%%s\n" "$@" > %q`, argvFile)

	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"
	agent.AgentDefaultArgs = []string{"-c", script, "fake-claude"}

	done := make(chan error, 1)
	go func() { done <- agent.Run("", beadsDir, ticket, agent.ModeAgent) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("agent.Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent.Run hung")
	}

	body, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	args := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(args) == 0 || args[len(args)-1] != agent.InitialAgentPrompt {
		t.Errorf("trailing argv = %q, want %q\nfull argv:\n%s",
			args[len(args)-1], agent.InitialAgentPrompt, strings.Join(args, "\n"))
	}
}

// TestRunSpecModeNoInitialPrompt asserts spec mode does NOT auto-submit a
// kickoff prompt: the operator must type the first input.
func TestRunSpecModeNoInitialPrompt(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "spec-no-initial-prompt", "x", "")
	mustEnv(t, beadsDir, ticket)

	workDir := t.TempDir()
	argvFile := filepath.Join(workDir, "argv")
	script := fmt.Sprintf(`printf "%%s\n" "$@" > %q`, argvFile)

	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"
	agent.AgentDefaultArgs = []string{"-c", script, "fake-claude"}

	done := make(chan error, 1)
	go func() { done <- agent.Run("", beadsDir, ticket, agent.ModeSpec) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("agent.Run (spec): %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent.Run (spec) hung")
	}

	body, _ := os.ReadFile(argvFile)
	if strings.Contains(string(body), agent.InitialAgentPrompt) {
		t.Errorf("spec mode must not pass InitialAgentPrompt; argv:\n%s", string(body))
	}
}

// TestRunResumeNoInitialPrompt asserts that a resume (existing transcript)
// does NOT re-inject the kickoff prompt — the transcript already has turn 1.
func TestRunResumeNoInitialPrompt(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-resume-no-prompt", "x", "")
	mustEnv(t, beadsDir, ticket)

	workDir := t.TempDir()
	argvFile := filepath.Join(workDir, "argv")
	script := fmt.Sprintf(`printf "%%s\n" "$@" > %q`, argvFile)

	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"
	agent.AgentDefaultArgs = []string{"-c", script, "fake-claude"}

	done := make(chan error, 1)
	go func() {
		done <- agent.RunWithResume("", beadsDir, ticket,
			"00000000-0000-4000-8000-000000000abc", agent.ModeAgent)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("agent.RunWithResume: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent.RunWithResume hung")
	}

	body, _ := os.ReadFile(argvFile)
	if strings.Contains(string(body), agent.InitialAgentPrompt) {
		t.Errorf("resume must not pass InitialAgentPrompt; argv:\n%s", string(body))
	}
}

// TestRunWithResumeRefusesModeMismatch asserts a resume can't silently
// re-classify the agent's type: `ticket agent --resume <spec-id>` (and the
// reverse) must fail before the UpsertAgent would overwrite the recorded
// mode.
func TestRunWithResumeRefusesModeMismatch(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-resume-mode-mismatch", "x", "")
	mustEnv(t, beadsDir, ticket)

	sid := "11111111-2222-4333-8444-555555555555"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: agent.ModeSpec.String(),
		StartedAt: "2026-05-15T14:56:00Z", Env: "/env",
		Status: agent.StatusWaiting,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	err := agent.RunWithResume("", beadsDir, ticket, sid, agent.ModeAgent)
	if err == nil {
		t.Fatal("RunWithResume across modes succeeded; want refusal")
	}
	if !strings.Contains(err.Error(), "recorded as spec") {
		t.Errorf("error = %q, want to mention 'recorded as spec'", err.Error())
	}

	got, _ := agent.LoadAgents("", beadsDir, ticket)
	if len(got) != 1 || got[0].Mode != agent.ModeSpec.String() {
		t.Errorf("entry mutated despite refusal: %+v", got)
	}
}

// TestRunRegistersAgentAsWaiting asserts the agent is registered with
// status="waiting" at launch — the UserPromptSubmit hook flips it to
// "running" once the first turn actually starts. Uniform across modes.
func TestRunRegistersAgentAsWaiting(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-initial-waiting", "x", "")
	mustEnv(t, beadsDir, ticket)

	// Fake claude that blocks until we let it exit, so we can read the
	// registered record while the wrapper is still inside the loop.
	gate := filepath.Join(t.TempDir(), "gate")
	script := fmt.Sprintf(`while [ ! -f %q ]; do sleep 0.05; done`, gate)

	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"
	agent.AgentDefaultArgs = []string{"-c", script, "fake-claude"}

	done := make(chan error, 1)
	go func() { done <- agent.Run("", beadsDir, ticket, agent.ModeAgent) }()

	// Poll for the registered record (registration happens before fork).
	deadline := time.Now().Add(5 * time.Second)
	var entries []agent.AgentEntry
	for time.Now().Before(deadline) {
		es, err := agent.LoadAgents("", beadsDir, ticket)
		if err == nil && len(es) > 0 {
			entries = es
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(entries) == 0 {
		_ = os.WriteFile(gate, nil, 0o644)
		<-done
		t.Fatal("no agent record registered within 5s")
	}
	if got := entries[0].Status; got != agent.StatusWaiting {
		t.Errorf("registered Status = %q, want %q", got, agent.StatusWaiting)
	}

	_ = os.WriteFile(gate, nil, 0o644)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("agent.Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent.Run hung")
	}
}

// TestRunWithOptionsCustomPromptAgent asserts that a fresh agent launch with
// a non-empty RunOptions.InitialPrompt passes it as the trailing positional
// argument, replacing the built-in InitialAgentPrompt.
func TestRunWithOptionsCustomPromptAgent(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-custom-prompt", "x", "")
	mustEnv(t, beadsDir, ticket)

	workDir := t.TempDir()
	argvFile := filepath.Join(workDir, "argv")
	script := fmt.Sprintf(`printf "%%s\n" "$@" > %q`, argvFile)

	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"
	agent.AgentDefaultArgs = []string{"-c", script, "fake-claude"}

	customPrompt := "Refactor the foo helper"
	done := make(chan error, 1)
	go func() {
		done <- agent.RunWithOptions("", beadsDir, ticket, agent.ModeAgent,
			agent.RunOptions{InitialPrompt: customPrompt})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("agent.RunWithOptions: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent.RunWithOptions hung")
	}

	body, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	args := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(args) == 0 || args[len(args)-1] != customPrompt {
		t.Errorf("trailing argv = %q, want %q\nfull argv:\n%s",
			args[len(args)-1], customPrompt, strings.Join(args, "\n"))
	}
	if strings.Contains(string(body), agent.InitialAgentPrompt) {
		t.Errorf("custom prompt must replace InitialAgentPrompt; argv:\n%s", string(body))
	}
}

// TestRunWithOptionsCustomPromptSpec asserts spec mode passes
// RunOptions.InitialPrompt as the trailing positional even though spec mode
// has no default prompt of its own.
func TestRunWithOptionsCustomPromptSpec(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "spec-custom-prompt", "x", "")
	mustEnv(t, beadsDir, ticket)

	workDir := t.TempDir()
	argvFile := filepath.Join(workDir, "argv")
	script := fmt.Sprintf(`printf "%%s\n" "$@" > %q`, argvFile)

	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"
	agent.AgentDefaultArgs = []string{"-c", script, "fake-claude"}

	customPrompt := "Draft the spec for X"
	done := make(chan error, 1)
	go func() {
		done <- agent.RunWithOptions("", beadsDir, ticket, agent.ModeSpec,
			agent.RunOptions{InitialPrompt: customPrompt})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("agent.RunWithOptions (spec): %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent.RunWithOptions (spec) hung")
	}

	body, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	args := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(args) == 0 || args[len(args)-1] != customPrompt {
		t.Errorf("trailing argv = %q, want %q\nfull argv:\n%s",
			args[len(args)-1], customPrompt, strings.Join(args, "\n"))
	}
}

// TestRunWithOptionsEmptyPromptFallsBack asserts that an empty
// InitialPrompt is treated as absent: agent mode still gets
// InitialAgentPrompt; spec mode still gets no trailing prompt.
func TestRunWithOptionsEmptyPromptFallsBack(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticketAgent := testutil.CreateBeadFull(t, beadsDir, "agent-empty-prompt", "x", "")
	mustEnv(t, beadsDir, ticketAgent)
	ticketSpec := testutil.CreateBeadFull(t, beadsDir, "spec-empty-prompt", "x", "")
	mustEnv(t, beadsDir, ticketSpec)

	workDir := t.TempDir()
	argvAgent := filepath.Join(workDir, "argv-agent")
	argvSpec := filepath.Join(workDir, "argv-spec")
	// The fake claude writes to a different file depending on a marker arg
	// the caller threads in; we use the ticket id, which the wrapper passes
	// nowhere — so instead we just route by mode-specific argv file via two
	// separate runs that swap AgentDefaultArgs between them.
	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"

	// Agent run.
	agent.AgentDefaultArgs = []string{"-c",
		fmt.Sprintf(`printf "%%s\n" "$@" > %q`, argvAgent), "fake-claude"}
	done := make(chan error, 1)
	go func() {
		done <- agent.RunWithOptions("", beadsDir, ticketAgent, agent.ModeAgent,
			agent.RunOptions{InitialPrompt: ""})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("agent.RunWithOptions (agent): %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent.RunWithOptions (agent) hung")
	}
	body, _ := os.ReadFile(argvAgent)
	args := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if len(args) == 0 || args[len(args)-1] != agent.InitialAgentPrompt {
		t.Errorf("agent mode trailing argv = %q, want %q (empty --prompt should fall back)",
			args[len(args)-1], agent.InitialAgentPrompt)
	}

	// Spec run.
	agent.AgentDefaultArgs = []string{"-c",
		fmt.Sprintf(`printf "%%s\n" "$@" > %q`, argvSpec), "fake-claude"}
	done = make(chan error, 1)
	go func() {
		done <- agent.RunWithOptions("", beadsDir, ticketSpec, agent.ModeSpec,
			agent.RunOptions{InitialPrompt: ""})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("agent.RunWithOptions (spec): %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent.RunWithOptions (spec) hung")
	}
	body, _ = os.ReadFile(argvSpec)
	if strings.Contains(string(body), agent.InitialAgentPrompt) {
		t.Errorf("spec mode must not inject InitialAgentPrompt on empty --prompt; argv:\n%s", string(body))
	}
}

// TestRunLoopRelaunchDoesNotInheritInitialPrompt asserts that the iter-2+
// wrapper-loop relaunch path does NOT re-inject the original launch's
// InitialPrompt — the signal file's extraArgs are the sole source of the
// post-relaunch prompt (today's default: "Relaunch successful").
func TestRunLoopRelaunchDoesNotInheritInitialPrompt(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-relaunch-prompt-inherit", "x", "")
	envPath := mustEnv(t, beadsDir, ticket)

	workDir := t.TempDir()
	argvFile := filepath.Join(workDir, "argv")
	// Fake claude: append all args (newline-delimited) into argvFile, with a
	// "---" separator between invocations. On the first invocation, drop a
	// signal file so the wrapper relaunches once.
	script := fmt.Sprintf(`
printf "%%s\n" "$@" >> %q
echo "---" >> %q
if [ ! -f %q.firstrun ]; then
  : > %q.firstrun
  : > %q
fi
`, argvFile, argvFile,
		argvFile, argvFile, filepath.Join(envPath, agent.SignalFile))

	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"
	agent.AgentDefaultArgs = []string{"-c", script, "fake-claude"}

	customPrompt := "Iter-1 custom prompt"
	done := make(chan error, 1)
	go func() {
		done <- agent.RunWithOptions("", beadsDir, ticket, agent.ModeAgent,
			agent.RunOptions{InitialPrompt: customPrompt})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("agent.RunWithOptions: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent.RunWithOptions hung")
	}

	body, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("read argv: %v", err)
	}
	chunks := strings.Split(strings.TrimRight(string(body), "\n"), "---")
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 fake-claude invocations, got %d; body=%q",
			len(chunks), string(body))
	}
	iter1 := strings.TrimSpace(chunks[0])
	iter2 := strings.TrimSpace(chunks[1])
	if !strings.Contains(iter1, customPrompt) {
		t.Errorf("iter 1 missing custom prompt %q; got:\n%s", customPrompt, iter1)
	}
	if strings.Contains(iter2, customPrompt) {
		t.Errorf("iter 2 must not inherit iter-1 custom prompt %q; got:\n%s",
			customPrompt, iter2)
	}
}

func writePID(t *testing.T, envPath string, pid int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(envPath, agent.PIDFile), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
}
