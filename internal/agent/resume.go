package agent

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// PickerCommand is the interactive fuzzy finder shelled out to by
// [PickSession]. Exported as a var so a future config knob can swap it.
var PickerCommand = "fzf"

// ErrPickCancelled is returned by [PickSession] when the operator aborts the
// picker (Esc / Ctrl-C) or narrows it to nothing. Callers treat it as a
// no-op success, not a failure — nothing was chosen, so nothing happens.
var ErrPickCancelled = errors.New("no session picked")

// ExecPicker feeds table to [PickerCommand] and returns the line the operator
// selected. Exported as a var so tests can substitute a stub: the real path
// needs an interactive terminal (fzf draws on /dev/tty, which is why only its
// stdout is captured here).
//
// fzf's exit codes are mapped to [ErrPickCancelled]: 1 means "no match" and
// 130 means "aborted by the user"; neither is an error worth surfacing.
var ExecPicker = func(table string, args []string) (string, error) {
	bin, err := exec.LookPath(PickerCommand)
	if err != nil {
		return "", fmt.Errorf("%s not found in PATH (install it, or pick by hand: `this agent list`, then `this agent --resume <session-id>`): %w", PickerCommand, err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdin = strings.NewReader(table)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && (ee.ExitCode() == 1 || ee.ExitCode() == 130) {
			return "", ErrPickCancelled
		}
		return "", fmt.Errorf("run %s: %w", PickerCommand, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// ExecClaude replaces the current process with a bare [AgentCommand] launch —
// no wrapper, no env folder, no agent record. It is the escape hatch for
// `ticket agent continue` / `ticket agent resume` when the caller is not
// inside a ticket env: the framework has nothing to resume there, so the
// operator's plain `claude --continue` / `claude --resume` behaviour is
// preserved verbatim.
//
// Exported as a var so tests can stub it instead of being replaced by claude.
var ExecClaude = func(args []string) error {
	bin, err := exec.LookPath(AgentCommand)
	if err != nil {
		return fmt.Errorf("%s not found in PATH: %w", AgentCommand, err)
	}
	argv := append([]string{bin}, AgentDefaultArgs...)
	argv = append(argv, args...)
	return syscall.Exec(bin, argv, os.Environ())
}

// ContinueLatest resumes the most recently active session recorded on
// ticketID — the `cc`-from-a-ticket-env path. Runs in the caller's terminal
// (unlike [Attach], which hunts for the original Terminal.app tab), so the
// operator lands back in the session they were in.
//
// "Most recently active" is [LatestSession]'s definition. Errors when the
// ticket has no recorded sessions at all: a plain claude launch in the env
// folder would silently skip the wrapper (no hooks, no system prompt, no
// --add-dir), so we refuse rather than guess.
func ContinueLatest(db, beadsDir, ticketID string) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	entries, err := LoadAgents(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	latest := LatestSession(entries)
	if latest == nil {
		return fmt.Errorf("ticket %s has no recorded agent sessions; launch `this agent` or `this spec` first", ticketID)
	}
	return ResumeSession(db, beadsDir, ticketID, latest.SessionID)
}

// PickAndResume renders table (the `agent list` output, header row included)
// through [ExecPicker] and resumes whichever session the operator picked.
// Returns [ErrPickCancelled] when nothing was chosen — the caller decides
// that this is a quiet exit.
//
// The table is passed in rather than built here because the renderer lives in
// internal/next (which imports this package): feeding it verbatim is what
// guarantees the picker shows exactly the rows `this agent list` shows.
func PickAndResume(db, beadsDir, ticketID, table string) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	if !hasSessionRows(table) {
		return fmt.Errorf("ticket %s has no recorded agent sessions; launch `this agent` or `this spec` first", ticketID)
	}
	line, err := ExecPicker(table, []string{
		"--header-lines=1",
		"--no-multi",
		"--prompt=session> ",
	})
	if err != nil {
		return err
	}
	sessionID := firstField(line)
	if sessionID == "" {
		return ErrPickCancelled
	}
	return ResumeSession(db, beadsDir, ticketID, sessionID)
}

// ResumeSession re-launches the wrapper against an existing transcript in the
// caller's terminal. The mode comes from the record, so a spec session resumes
// as spec and a coding session as agent without the operator having to
// remember which surface launched it.
//
// Lookup is strict — an unknown session id errors with the ticket's known ids,
// matching [Attach].
func ResumeSession(db, beadsDir, ticketID, sessionID string) error {
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
	for i := range entries {
		if entries[i].SessionID == sessionID {
			return RunWithOptions(db, beadsDir, ticketID, ParseMode(entries[i].Mode),
				RunOptions{ResumeSessionID: sessionID})
		}
	}
	return unknownSessionError(ticketID, sessionID, entries)
}

// LatestSession returns the entry with the most recent activity, or nil for an
// empty slice. Activity is max(StartedAt, StatusUpdatedAt): StartedAt is
// rewritten on every (re)launch of the wrapper and StatusUpdatedAt on every
// running/waiting flip, so the pair tracks "the session I was last in" better
// than either alone. Ties go to the later record (insertion order).
//
// Both fields are RFC 3339 in UTC (every writer formats them that way), so a
// plain string comparison is a chronological one.
func LatestSession(entries []AgentEntry) *AgentEntry {
	var best *AgentEntry
	var bestKey string
	for i := range entries {
		key := max(entries[i].StartedAt, entries[i].StatusUpdatedAt)
		if best == nil || key >= bestKey {
			best, bestKey = &entries[i], key
		}
	}
	return best
}

// hasSessionRows reports whether table carries at least one row below its
// header line. `agent list` always prints the header, even for a ticket with
// no records, so the header alone means "nothing to pick".
func hasSessionRows(table string) bool {
	rows := 0
	for _, line := range strings.Split(table, "\n") {
		if strings.TrimSpace(line) != "" {
			rows++
		}
	}
	return rows > 1
}

// firstField returns the first whitespace-separated field of line — the
// SESSION ID column of an `agent list` row.
func firstField(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
