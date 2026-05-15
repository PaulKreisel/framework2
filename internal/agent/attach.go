package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// ExecOsascript runs `osascript -e <script>` and returns its stdout. Exposed
// as a package-level var so tests can substitute a stub: the real path drives
// macOS Terminal.app, which is unavailable in CI / headless runners.
var ExecOsascript = func(script string) (string, error) {
	out, err := exec.Command("osascript", "-e", script).Output()
	return string(out), err
}

// LookupTTY returns the controlling TTY for pid via `ps -p <pid> -o tty=`,
// trimmed. Returns "??" verbatim when ps reports no controlling terminal
// (background/nohup process); callers should treat that as "no terminal".
// Exposed as a var so tests can stub the lookup.
var LookupTTY = func(pid int) (string, error) {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "tty=").Output()
	if err != nil {
		return "", fmt.Errorf("ps -p %d: %w", pid, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Attach is the entry point for `ticket agent attach`: it finds the
// AgentEntry for sessionID under ticketID and either focuses the macOS
// Terminal.app tab hosting a live wrapper or spawns a fresh wrapper resuming
// the same claude transcript.
//
// Lookup is strict: when sessionID does not match any record an error is
// returned listing the known session ids so the caller (often a script) can
// recover without an interactive picker.
//
// Behaviour by state of the matched entry:
//
//   - running / waiting: focus the Terminal.app tab whose TTY belongs to the
//     wrapper PID via osascript. If the wrapper has no controlling terminal
//     (e.g. backgrounded with nohup) the call returns a useful error.
//   - exited (wrapper PID is gone): open a new Terminal.app window that
//     execs `<ticket> agent --resume <sessionID>` from the env folder. The
//     spawned wrapper updates the existing agent record in place via
//     [RunWithResume]; claude reattaches to the original transcript so the
//     same session id stays valid.
//
// macOS-only: callers on other platforms get an explicit error rather than
// a silent no-op so scripts can branch on it.
func Attach(db, beadsDir, ticketID, sessionID string) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	if sessionID == "" {
		return errors.New("session id required")
	}
	entries, err := LoadAgents(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	var entry *AgentEntry
	for i := range entries {
		if entries[i].SessionID == sessionID {
			entry = &entries[i]
			break
		}
	}
	if entry == nil {
		return unknownSessionError(ticketID, sessionID, entries)
	}
	state := stateOf(*entry)
	if state == StatusRunning || state == StatusWaiting {
		if runtime.GOOS != "darwin" {
			return fmt.Errorf("focus is macOS-only (GOOS=%s); no portable terminal API yet", runtime.GOOS)
		}
		return focusTerminal(entry.PID)
	}
	// Treat anything that isn't running/waiting as "exited" — the wrapper's
	// PID is gone, so we resume.
	if _, statErr := os.Stat(entry.Env); os.IsNotExist(statErr) {
		return fmt.Errorf("env folder %s no longer exists; bead was probably finished", entry.Env)
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("resume-in-new-window is macOS-only (GOOS=%s); no portable terminal API yet", runtime.GOOS)
	}
	return spawnResumeWindow(entry.Env, ticketID, sessionID)
}

func unknownSessionError(ticketID, sessionID string, entries []AgentEntry) error {
	if len(entries) == 0 {
		return fmt.Errorf("ticket %s has no recorded agent sessions; launch `ticket agent` first", ticketID)
	}
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.SessionID
	}
	return fmt.Errorf("session %q not found for ticket %s; known sessions:\n  %s",
		sessionID, ticketID, strings.Join(ids, "\n  "))
}

// focusTerminal walks Terminal.app's windows looking for the tab whose `tty`
// matches the wrapper's controlling tty. Mirrors the bash `claude-session
// focus` reference at framework/commands/claude-session so behaviour is easy
// to verify side-by-side.
func focusTerminal(pid int) error {
	tty, err := LookupTTY(pid)
	if err != nil {
		return err
	}
	if tty == "" || tty == "?" || tty == "??" {
		return fmt.Errorf("pid %d has no controlling terminal — wrapper is probably backgrounded", pid)
	}
	script := fmt.Sprintf(`tell application "Terminal"
    repeat with w in windows
        repeat with t in tabs of w
            if tty of t is "/dev/%s" then
                set selected of t to true
                set index of w to 1
                activate
                return "OK"
            end if
        end repeat
    end repeat
    return "NOT_FOUND"
end tell`, tty)
	out, err := ExecOsascript(script)
	if err != nil {
		return fmt.Errorf("osascript focus: %w", err)
	}
	if strings.TrimSpace(out) != "OK" {
		return fmt.Errorf("no Terminal.app tab owns /dev/%s (pid %d) — wrapper is probably backgrounded", tty, pid)
	}
	return nil
}

// spawnResumeWindow opens a new Terminal.app window that re-runs the current
// `ticket` binary in resume mode. The new shell first cd's into envPath so
// the wrapper inherits the same cwd a fresh `ticket agent` would.
func spawnResumeWindow(envPath, ticketID, sessionID string) error {
	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate ticket binary: %w", err)
	}
	cmd := fmt.Sprintf("cd %s && exec %s --ticket %s agent --resume %s",
		shellQuote(envPath), shellQuote(bin), shellQuote(ticketID), shellQuote(sessionID))
	script := fmt.Sprintf(`tell application "Terminal"
    do script %s
    activate
end tell`, applescriptString(cmd))
	if _, err := ExecOsascript(script); err != nil {
		return fmt.Errorf("osascript spawn: %w", err)
	}
	return nil
}

// shellQuote wraps s in single quotes for safe interpolation into a bash
// command line. Embedded single quotes are escaped via the standard
// '\'' close-quote-escape-open-quote dance.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// applescriptString wraps s in double quotes for use as an AppleScript
// string literal. Backslashes and double-quotes are escaped.
func applescriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
