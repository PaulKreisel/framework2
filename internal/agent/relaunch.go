package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Filenames the wrapper and the relaunch helper communicate through. Both
// live at the top level of the env folder so they're discoverable via cwd
// from inside a session.
const (
	// SignalFile is touched by Relaunch to ask the wrapper to re-exec claude.
	// Its contents (if any) are extra claude args, one per line, appended
	// after the wrapper's automatic --continue.
	SignalFile = ".relaunch"
	// PIDFile holds the pid of the currently running claude child of the
	// wrapper. The wrapper writes it after launch and removes it after wait;
	// Relaunch reads it to know whom to signal.
	PIDFile = ".claude.pid"
)

// DefaultRelaunchPrompt is the initial prompt passed to claude when `ticket
// agent relaunch` is invoked with no extra args. It gives the relaunched
// session something to respond to so the user sees a visible signal that the
// relaunch actually happened (and that any newly linked dirs are in scope).
const DefaultRelaunchPrompt = "Relaunch successful"

// Relaunch asks a running `ticket agent` wrapper to re-exec claude with
// `--continue` plus extraArgs. It writes [SignalFile] in the ticket's env
// folder, then signals the claude process whose pid is in [PIDFile].
//
// When extraArgs is empty, [DefaultRelaunchPrompt] is passed as the prompt
// instead so the relaunched session has something to acknowledge.
//
// Returns an error if no pid file exists (i.e. not running inside a wrapped
// session) or if the recorded pid is no longer alive. Callers that want a
// best-effort relaunch (no error when no wrapper is running) should use
// [RelaunchIfActive] instead.
func Relaunch(beadsDir, ticketID string, extraArgs []string) error {
	if beadsDir == "" {
		return errors.New("BEADS_DIR not set: cannot derive env path")
	}
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	envPath := filepath.Join(beadsDir, "envs", ticketID)
	pid, err := readPIDFile(filepath.Join(envPath, PIDFile))
	if err != nil {
		return err
	}
	if len(extraArgs) == 0 {
		extraArgs = []string{DefaultRelaunchPrompt}
	}
	if err := writeSignal(filepath.Join(envPath, SignalFile), extraArgs); err != nil {
		return err
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal claude (pid %d): %w", pid, err)
	}
	return nil
}

// RelaunchIfActive is the best-effort variant used by the auto-relaunch flow
// in `ticket dir link` and `ticket dir worktree`: if a live wrapper is
// running for ticketID it relaunches it, otherwise it silently returns nil.
// Empty beadsDir or ticketID, a missing pid file, an unreadable/garbled pid
// file, and a recorded-but-dead process all collapse to the no-op case so
// that running the wrapping CLI from outside any session is never an error.
func RelaunchIfActive(beadsDir, ticketID string) error {
	if beadsDir == "" || ticketID == "" {
		return nil
	}
	pidPath := filepath.Join(beadsDir, "envs", ticketID, PIDFile)
	if _, err := readPIDFile(pidPath); err != nil {
		return nil
	}
	return Relaunch(beadsDir, ticketID, nil)
}

func readPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("not running inside ticket agent (no %s)", path)
		}
		return 0, fmt.Errorf("read %s: %w", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	// Signal 0 = liveness check.
	if err := syscall.Kill(pid, 0); err != nil {
		return 0, fmt.Errorf("recorded claude process %d not running: %w", pid, err)
	}
	return pid, nil
}

func writeSignal(path string, args []string) error {
	body := strings.Join(args, "\n")
	if body != "" {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// readSignal returns the relaunch args from path. The bool reports whether
// the signal was present; when true, the file is removed before returning so
// the next iteration starts clean.
func readSignal(path string) ([]string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if err := os.Remove(path); err != nil {
		return nil, false, fmt.Errorf("clear signal: %w", err)
	}
	var args []string
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line != "" {
			args = append(args, line)
		}
	}
	return args, true, nil
}
