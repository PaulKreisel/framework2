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
	beadsDir := testutil.SharedBdDB(t)
	ticket, _ := createBeadWithDescription(t, "agent-relaunch-ok", "x")
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

func TestRelaunchWithNoExtraArgsWritesEmptySignal(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket, _ := createBeadWithDescription(t, "agent-relaunch-empty", "x")
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
	if len(body) != 0 {
		t.Errorf("signal body = %q, want empty (no extra args)", string(body))
	}
	_ = cmd.Wait()
}

func TestRelaunchFailsWithoutPIDFile(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket, _ := createBeadWithDescription(t, "agent-relaunch-no-pid", "x")
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
	beadsDir := testutil.SharedBdDB(t)
	ticket, _ := createBeadWithDescription(t, "agent-relaunch-stale-pid", "x")
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

func TestRunLoopRelaunchesOnSignal(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket, _ := createBeadWithDescription(t, "agent-run-loop", "x")
	envPath := mustEnv(t, beadsDir, ticket)

	workDir := t.TempDir()
	counter := filepath.Join(workDir, "counter")

	// Fake claude:
	//   - appends $1 (which is "" on the first run, "--continue" on relaunch)
	//   - on the first run, writes the signal file (empty body → no extra
	//     args) to ask for a relaunch
	//   - exits 0
	script := fmt.Sprintf(`
echo "$1" >> %q
if [ ! -f %q ]; then
  : > %q.firstrun
  : > %q
fi
`, counter, fmt.Sprintf("%s.firstrun", counter), counter, filepath.Join(envPath, agent.SignalFile))

	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"
	// Trailing "fake-claude" supplies $0 for the script body; relaunch args
	// from the wrapper become $1, $2, ...
	agent.AgentDefaultArgs = []string{"-c", script, "fake-claude"}

	done := make(chan error, 1)
	go func() { done <- agent.Run("", beadsDir, ticket) }()

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
	if lines[0] != "" {
		t.Errorf("first invocation $1 = %q, want empty (no relaunch yet)", lines[0])
	}
	if lines[1] != "--continue" {
		t.Errorf("second invocation $1 = %q, want %q", lines[1], "--continue")
	}

	if _, err := os.Stat(filepath.Join(envPath, agent.SignalFile)); !os.IsNotExist(err) {
		t.Errorf("signal file still present after loop exit: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(envPath, agent.PIDFile)); !os.IsNotExist(err) {
		t.Errorf("pid file still present after loop exit: err=%v", err)
	}
}

func TestRunLoopClearsStaleSignalFile(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket, _ := createBeadWithDescription(t, "agent-run-clear-stale", "x")
	envPath := mustEnv(t, beadsDir, ticket)

	// Plant a stale signal — if the loop didn't clear it pre-flight, the
	// fake claude's single invocation would be followed by a second one.
	if err := os.WriteFile(filepath.Join(envPath, agent.SignalFile), []byte(""), 0o644); err != nil {
		t.Fatalf("plant stale signal: %v", err)
	}

	workDir := t.TempDir()
	counter := filepath.Join(workDir, "counter")
	script := fmt.Sprintf(`echo "$1" >> %q`, counter)

	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"
	agent.AgentDefaultArgs = []string{"-c", script, "fake-claude"}

	done := make(chan error, 1)
	go func() { done <- agent.Run("", beadsDir, ticket) }()
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

func writePID(t *testing.T, envPath string, pid int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(envPath, agent.PIDFile), []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		t.Fatalf("write pid file: %v", err)
	}
}
