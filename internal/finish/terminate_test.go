// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package finish_test

import (
	"bytes"
	"strings"
	"sync"
	"testing"

	"framework2/internal/finish"
)

// TestRunTerminateAsyncRunsOsascript covers the happy path: a valid tty
// drives an osascript call whose script targets the matching Terminal.app
// tab, and the "OK" reply maps cleanly to a non-error.
func TestRunTerminateAsyncRunsOsascript(t *testing.T) {
	restore := finish.ExecOsascript
	t.Cleanup(func() { finish.ExecOsascript = restore })

	var mu sync.Mutex
	var captured []string
	finish.ExecOsascript = func(script string) (string, error) {
		mu.Lock()
		captured = append(captured, script)
		mu.Unlock()
		return "OK\n", nil
	}

	finish.TestSkipSleep = true
	t.Cleanup(func() { finish.TestSkipSleep = false })

	var buf bytes.Buffer
	if err := finish.RunTerminateAsync(0, "ttys999", &buf); err != nil {
		t.Fatalf("RunTerminateAsync: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 1 {
		t.Fatalf("want 1 osascript call, got %d", len(captured))
	}
	if !strings.Contains(captured[0], "/dev/ttys999") {
		t.Errorf("osascript script missing /dev/ttys999:\n%s", captured[0])
	}
	if !strings.Contains(captured[0], "close w saving no") {
		t.Errorf("osascript script does not close the window:\n%s", captured[0])
	}
}

// TestRunTerminateAsyncRequiresArgs covers the validation path: both --tty
// and --wrapper-pid empty / zero is a configuration bug, not a no-op.
func TestRunTerminateAsyncRequiresArgs(t *testing.T) {
	finish.TestSkipSleep = true
	t.Cleanup(func() { finish.TestSkipSleep = false })

	var buf bytes.Buffer
	if err := finish.RunTerminateAsync(0, "", &buf); err == nil {
		t.Fatal("expected error when both --tty and --wrapper-pid are empty")
	}
}
