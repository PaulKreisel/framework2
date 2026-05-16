package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Status writes the full record for one agent session to stdout. The `agent
// list` view truncates the waiting reason to fit a single column; this
// subcommand is the read path for the untruncated value plus the rest of the
// entry's metadata.
//
// Lookup is strict — an unknown session id errors with the list of valid ids
// for ticketID, matching the shape of [Attach] / [DeleteAgents] so a typo
// can't silently succeed.
//
// jsonOutput emits the raw [AgentEntry] (same shape as `agent list --json`'s
// element) instead of the human-readable block.
func Status(db, beadsDir, ticketID, sessionID string, jsonOutput bool, stdout io.Writer) error {
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
	if jsonOutput {
		b, err := json.MarshalIndent(*entry, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(stdout, string(b))
		return err
	}
	return renderStatusBlock(*entry, stdout)
}

// renderStatusBlock prints the human-readable form: a `KEY: value` block for
// the metadata followed by the full WaitingReason verbatim (empty when the
// agent isn't waiting). The reason is emitted last and unindented so a
// multi-line assistant message reads naturally.
func renderStatusBlock(e AgentEntry, stdout io.Writer) error {
	fields := [][2]string{
		{"Session ID", e.SessionID},
		{"Mode", e.Mode},
		{"PID", fmt.Sprintf("%d", e.PID)},
		{"Started", FormatStarted(e.StartedAt)},
		{"State", StateOf(e)},
		{"Status Updated", FormatStarted(e.StatusUpdatedAt)},
		{"Env", e.Env},
	}
	for _, f := range fields {
		if _, err := fmt.Fprintf(stdout, "%s: %s\n", f[0], f[1]); err != nil {
			return err
		}
	}
	if e.WaitingReason == "" {
		return nil
	}
	if _, err := fmt.Fprintln(stdout, "Waiting Reason:"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, e.WaitingReason); err != nil {
		return err
	}
	return nil
}
