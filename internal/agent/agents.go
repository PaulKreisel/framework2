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
// in [PIDFile], which changes each iteration. Liveness checks ([stateOf]) use
// this pid to decide whether the agent is still running.
//
// Status is a free-form string driven by Claude Code hooks via
// `this agent set-status`. Today's writers only emit "running" and "waiting";
// the field is intentionally not an enum so finer waiting states can be
// added later without a schema migration. Empty Status on a legacy entry is
// treated as "running" by [stateOf].
type AgentEntry struct {
	SessionID       string `json:"sessionId"`
	PID             int    `json:"pid"`
	Mode            string `json:"mode"`
	StartedAt       string `json:"startedAt"`
	Env             string `json:"env"`
	Status          string `json:"status,omitempty"`
	StatusUpdatedAt string `json:"statusUpdatedAt,omitempty"`
}

// StatusRunning is the initial status assigned when an agent is registered,
// and the value the UserPromptSubmit hook writes back when the user submits
// a prompt.
const StatusRunning = "running"

// StatusWaiting is what the Stop hook writes when an assistant turn ends and
// claude is waiting for the next user input.
const StatusWaiting = "waiting"

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
// tab-aligned table with columns SESSION ID, MODE, PID, STARTED, STATE, ENV
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
	fmt.Fprintln(tw, "SESSION ID\tMODE\tPID\tSTARTED\tSTATE\tENV")
	for _, e := range reversed {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n",
			e.SessionID, e.Mode, e.PID, formatStarted(e.StartedAt), stateOf(e), e.Env)
	}
	return tw.Flush()
}

// stateOf collapses (wrapper-pid liveness, recorded Status) into the value
// shown in the STATE column: "exited" wins when the wrapper is dead so a
// stale "running"/"waiting" record never masks a dead session; otherwise the
// hook-driven Status is surfaced (defaulting to "running" for legacy entries
// that predate the status field).
//
// Liveness uses signal 0: ESRCH means the pid is gone, EPERM means the pid
// exists but we don't own it (e.g. init on macOS). EPERM therefore counts as
// alive — denying that would falsely report any running wrapper as "exited"
// whenever it runs under a different uid.
func stateOf(e AgentEntry) string {
	if e.PID <= 0 {
		return "exited"
	}
	err := syscall.Kill(e.PID, 0)
	if errors.Is(err, syscall.ESRCH) {
		return "exited"
	}
	if e.Status == "" {
		return StatusRunning
	}
	return e.Status
}

// ErrSessionNotFound is returned by SetStatus when no agent record matches
// the supplied session id. Callers route this differently depending on how
// the lookup was initiated: a manual `set-status --session <id>` exits with
// the error (it's a typo backstop), but a hook-driven invocation suppresses
// it (the hook fires for every claude session, including ones that weren't
// launched via `ticket agent` and never registered an entry).
var ErrSessionNotFound = errors.New("no agent record found for session")

// SetStatus updates the matching agent record's Status and StatusUpdatedAt.
// Lookup is by sessionID; ticketID, if non-empty, scopes the search to a
// single ticket. With ticketID empty all open tickets are scanned. A no-op
// (Status already equal to status) is fast-pathed without touching the bead.
// Returns [ErrSessionNotFound] when no record matches.
func SetStatus(db, beadsDir, sessionID, status, ticketID string) error {
	if sessionID == "" {
		return errors.New("session id required")
	}
	if status == "" {
		return errors.New("status required")
	}
	tickets := []string{ticketID}
	if ticketID == "" {
		all, err := ticketsWithAgents(db, beadsDir)
		if err != nil {
			return err
		}
		tickets = all
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, tid := range tickets {
		entries, err := LoadAgents(db, beadsDir, tid)
		if err != nil {
			return err
		}
		for i, e := range entries {
			if e.SessionID != sessionID {
				continue
			}
			if e.Status == status {
				return nil
			}
			entries[i].Status = status
			entries[i].StatusUpdatedAt = now
			return saveAgents(db, beadsDir, tid, entries)
		}
	}
	return fmt.Errorf("%w %s", ErrSessionNotFound, sessionID)
}

// ticketsWithAgents lists every bead id whose metadata carries a non-empty
// agents array. Used by SetStatus when the caller didn't pin a ticket — hooks
// only have a session id, so we scan to locate the owning bead.
func ticketsWithAgents(db, beadsDir string) ([]string, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "list", "--all").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("list tickets: %v: %s", err, ee.Stderr)
		}
		return nil, fmt.Errorf("list tickets: %w", err)
	}
	var rows []struct {
		ID       string                     `json:"id"`
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse ticket list: %w", err)
	}
	var ids []string
	for _, r := range rows {
		raw, ok := r.Metadata["agents"]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			continue
		}
		// Cheap presence check: an empty array marshals as "[]".
		if string(raw) == "[]" {
			continue
		}
		ids = append(ids, r.ID)
	}
	return ids, nil
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

