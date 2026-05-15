package agent

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"syscall"
	"text/tabwriter"
	"time"

	"framework2/internal/bdcli"
)

// AgentEntry is one row in the ticket's metadata.agents array: a record of a
// Claude session that was launched against the ticket. Only the wrapper
// process writes these; `agent list` reads them back.
//
// PID is the wrapper process pid (the Go process running [Run]), which is
// stable across the wrapper's relaunch loop — unlike the claude pid recorded
// in [PIDFile], which changes each iteration. Liveness checks ([statusOf]) use
// this pid to decide whether the agent is still running.
type AgentEntry struct {
	SessionID string `json:"sessionId"`
	PID       int    `json:"pid"`
	Mode      string `json:"mode"`
	StartedAt string `json:"startedAt"`
	Env       string `json:"env"`
}

// String returns "agent" or "spec".
func (m Mode) String() string {
	switch m {
	case ModeSpec:
		return "spec"
	default:
		return "agent"
	}
}

// NewSessionID returns a freshly generated UUIDv4 suitable for passing to
// claude via `--session-id`. Exposed so callers that need the id before
// invoking [Run] (e.g. tests) can pre-generate it.
func NewSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// RegisterAgent appends entry to the ticket's metadata.agents array via a
// read-modify-write on the bead's metadata blob. Concurrent writes to other
// metadata keys (e.g. `dirs`) are preserved because `bd update --metadata`
// merges at the key level; concurrent writes to `agents` itself last-write-win,
// matching how [dir] handles its own array.
func RegisterAgent(db, beadsDir, ticketID string, entry AgentEntry) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	entries, err := LoadAgents(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	entries = append(entries, entry)
	return saveAgents(db, beadsDir, ticketID, entries)
}

// LoadAgents returns the agent records stored under the ticket's
// metadata.agents key, oldest first (insertion order). Nil when no agent has
// ever been launched against the ticket.
func LoadAgents(db, beadsDir, ticketID string) ([]AgentEntry, error) {
	row, err := showAgentsRow(db, beadsDir, ticketID)
	if err != nil {
		return nil, err
	}
	raw, ok := row.Metadata["agents"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var entries []AgentEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse agents entries: %w", err)
	}
	return entries, nil
}

func saveAgents(db, beadsDir, ticketID string, entries []AgentEntry) error {
	if entries == nil {
		entries = []AgentEntry{}
	}
	payload, err := json.Marshal(map[string]any{"agents": entries})
	if err != nil {
		return err
	}
	cmd := bdcli.Cmd(beadsDir, db, "update", ticketID, "--metadata", string(payload))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("update ticket %s agents: %w: %s", ticketID, err, out)
	}
	return nil
}

type agentsRow struct {
	Metadata map[string]json.RawMessage `json:"metadata"`
}

func showAgentsRow(db, beadsDir, ticketID string) (*agentsRow, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "show", ticketID).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("look up ticket %s: %v: %s", ticketID, err, ee.Stderr)
		}
		return nil, fmt.Errorf("look up ticket %s: %w", ticketID, err)
	}
	var rows []agentsRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse ticket store output: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("ticket not found: %s", ticketID)
	}
	return &rows[0], nil
}

// ListAgents writes the ticket's agent records to out. With jsonOutput=true
// the raw entries (newest first) are emitted as indented JSON; otherwise a
// tab-aligned table with columns SESSION ID, MODE, PID, STARTED, STATUS, ENV
// is written.
func ListAgents(db, beadsDir, ticketID string, jsonOutput bool, out io.Writer) error {
	entries, err := LoadAgents(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	// Newest first.
	reversed := make([]AgentEntry, len(entries))
	for i, e := range entries {
		reversed[len(entries)-1-i] = e
	}
	if jsonOutput {
		// reversed is always a (possibly empty) slice from make, so
		// MarshalIndent emits "[]" rather than "null" for empty input —
		// no nil guard needed.
		b, err := json.MarshalIndent(reversed, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(b))
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION ID\tMODE\tPID\tSTARTED\tSTATUS\tENV")
	for _, e := range reversed {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n",
			e.SessionID, e.Mode, e.PID, formatStarted(e.StartedAt), statusOf(e), e.Env)
	}
	return tw.Flush()
}

// statusOf returns "running" if entry.PID is alive, else "exited".
func statusOf(e AgentEntry) string {
	if e.PID <= 0 {
		return "exited"
	}
	if err := syscall.Kill(e.PID, 0); err != nil {
		return "exited"
	}
	return "running"
}

// formatStarted re-renders an RFC 3339 timestamp as "YYYY-MM-DD HH:MM" in
// local time for the table view. Invalid input is passed through verbatim so
// no agent record ever silently disappears.
func formatStarted(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04")
}

