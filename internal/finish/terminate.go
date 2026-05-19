package finish

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"framework2/internal/agent"
)

// TerminateGraceMillis is how long the detached helper waits before sending
// the close. It needs to outlast the time it takes for `this finish`'s
// success output to flow back through claude to the terminal — too short and
// the user never sees the "finished" line; too long and the terminal lingers
// past its welcome.
const TerminateGraceMillis = 2000

// spawnTerminateOnFinish is invoked from [Run] right before the env folder is
// removed. When `this finish` is running inside an active wrapper session
// (detected via the env folder's PIDFile), it captures the wrapper pid + tty
// and detaches a [TerminateAsync] runner. After [Run] returns, that runner
// SIGHUPs the wrapper and closes the hosting Terminal.app tab.
//
// Best-effort: silently no-ops when not in a wrapper session, when the
// current platform isn't macOS, when `go test` is the caller, or when
// information lookup fails. None of those cases should abort an otherwise
// successful finish.
func spawnTerminateOnFinish(envPath string, stdout io.Writer) {
	if testing.Testing() {
		return
	}
	if runtime.GOOS != "darwin" {
		return
	}
	claudePID, ok := readClaudePID(filepath.Join(envPath, agent.PIDFile))
	if !ok {
		// Not running under a wrapper — finish was invoked from a plain
		// shell. Nothing to tear down.
		return
	}
	wrapperPID, ok := parentPID(claudePID)
	if !ok || wrapperPID <= 1 {
		return
	}
	tty, ok := ttyOf(claudePID)
	if !ok {
		return
	}
	if err := spawnTerminateAsync(wrapperPID, tty, stdout); err != nil {
		// Surface as a warning but do not fail the finish — the user
		// can still close the terminal manually.
		fmt.Fprintf(stdout, "warn: could not spawn terminate helper: %v\n", err)
	}
}

func readClaudePID(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return 0, false
	}
	return pid, true
}

func parentPID(pid int) (int, bool) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "ppid=").Output()
	if err != nil {
		return 0, false
	}
	ppid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, false
	}
	return ppid, true
}

func ttyOf(pid int) (string, bool) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "tty=").Output()
	if err != nil {
		return "", false
	}
	tty := strings.TrimSpace(string(out))
	if tty == "" || tty == "?" || tty == "??" {
		return "", false
	}
	return tty, true
}

// spawnTerminateAsync detaches a `ticket terminate-async` runner that, after
// [TerminateGraceMillis], closes the Terminal.app tab/window owning tty and
// SIGHUPs wrapperPID. Detached via Setsid so the closing terminal can't
// SIGHUP it mid-execution.
func spawnTerminateAsync(wrapperPID int, tty string, stdout io.Writer) error {
	ticketBin, err := findTicketBin()
	if err != nil {
		return fmt.Errorf("locate ticket binary: %w", err)
	}
	args := []string{
		"terminate-async",
		"--wrapper-pid", strconv.Itoa(wrapperPID),
		"--tty", tty,
	}
	cmd := exec.Command(ticketBin, args...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := cmd.Process.Release(); err != nil {
		return err
	}
	return nil
}

// RunTerminateAsync is the entrypoint for the hidden `ticket terminate-async`
// subcommand. It sleeps briefly so the parent finish's output reaches the
// user, then closes the macOS Terminal.app tab/window whose tty matches and
// SIGHUPs wrapperPID as a fallback in case the close-window step missed.
//
// Stdout writes are effectively discarded — the parent's stdio was detached —
// but the function still accepts an io.Writer so tests (which set
// [TestSkipSleep]) can observe the trace.
func RunTerminateAsync(wrapperPID int, tty string, stdout io.Writer) error {
	if tty == "" && wrapperPID <= 0 {
		return errors.New("terminate-async: --tty or --wrapper-pid required")
	}
	if !TestSkipSleep {
		time.Sleep(time.Duration(TerminateGraceMillis) * time.Millisecond)
	}
	if tty != "" && runtime.GOOS == "darwin" {
		if err := closeTerminalTab(tty); err != nil {
			fmt.Fprintf(stdout, "terminate-async: close terminal failed: %v\n", err)
		}
	}
	if wrapperPID > 0 {
		// Belt-and-suspenders: if closing the terminal tab missed (e.g.
		// the user is on a non-Terminal.app frontend), kill the wrapper
		// directly so the loop returns instead of relaunching.
		_ = syscall.Kill(wrapperPID, syscall.SIGHUP)
	}
	return nil
}

// TestSkipSleep lets the unit test suite skip the 2-second grace without
// reaching for the clock. Production callers leave it false.
var TestSkipSleep bool

// ExecOsascript is a package-level var so tests can substitute a stub. Real
// osascript is unavailable in CI; covering closeTerminalTab inline would
// otherwise force a darwin+Terminal.app dependency on every test run.
var ExecOsascript = func(script string) (string, error) {
	out, err := exec.Command("osascript", "-e", script).Output()
	return string(out), err
}

// closeTerminalTab asks Terminal.app to close the window whose first tab's
// tty matches /dev/<tty>. Closing the window — not just the tab — is the
// reliable path: Terminal.app's AppleScript surface lacks a per-tab close,
// and a session launched via `do script` lives in its own window, so closing
// the window matches the spawn-resume mental model in
// [agent.spawnResumeWindow].
func closeTerminalTab(tty string) error {
	script := fmt.Sprintf(`tell application "Terminal"
    repeat with w in windows
        repeat with t in tabs of w
            if tty of t is "/dev/%s" then
                close w saving no
                return "OK"
            end if
        end repeat
    end repeat
    return "NOT_FOUND"
end tell`, tty)
	out, err := ExecOsascript(script)
	if err != nil {
		return fmt.Errorf("osascript close: %w", err)
	}
	if strings.TrimSpace(out) != "OK" {
		return fmt.Errorf("no Terminal.app tab owns /dev/%s", tty)
	}
	return nil
}
