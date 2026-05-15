package agent

import (
	"errors"
	"fmt"
	"strings"
)

// DeleteAgents removes the agent records named by sessionIDs from the
// ticket's metadata.agents array. Operates on the records only — never tries
// to signal or kill the underlying processes.
//
// Each id must match an existing record; an unknown id errors before any
// write happens, with the same shape as [unknownSessionError], so a typo can
// never leave the list partially mutated. Duplicate ids in sessionIDs are
// rejected for the same reason — silently collapsing them would hide a bug
// in the caller.
//
// By default a record whose wrapper is still alive ([stateOf] reports
// "running"/"waiting") is refused: the list exists to track launched
// sessions, and deleting a live one would lose the user's only handle on
// the running agent. Pass force=true to delete anyway (the process itself
// is unaffected — the user is on their own to stop it).
func DeleteAgents(db, beadsDir, ticketID string, sessionIDs []string, force bool) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	if len(sessionIDs) == 0 {
		return errors.New("at least one session id required")
	}
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, sid := range sessionIDs {
		if sid == "" {
			return errors.New("session id required")
		}
		if _, dup := seen[sid]; dup {
			return fmt.Errorf("session id %q listed more than once", sid)
		}
		seen[sid] = struct{}{}
	}

	entries, err := LoadAgents(db, beadsDir, ticketID)
	if err != nil {
		return err
	}

	// Pre-validate every id against the current list so a typo aborts with
	// the same error shape as attach and never partially mutates state.
	idx := make(map[string]int, len(entries))
	for i, e := range entries {
		idx[e.SessionID] = i
	}
	var liveBlockers []string
	for _, sid := range sessionIDs {
		i, ok := idx[sid]
		if !ok {
			return unknownSessionError(ticketID, sid, entries)
		}
		if !force {
			state := stateOf(entries[i])
			if state != "exited" {
				liveBlockers = append(liveBlockers, fmt.Sprintf("%s (%s)", sid, state))
			}
		}
	}
	if len(liveBlockers) > 0 {
		return fmt.Errorf("refusing to delete live agent record(s); stop the agent(s) first or pass --force:\n  %s",
			strings.Join(liveBlockers, "\n  "))
	}

	kept := entries[:0]
	for _, e := range entries {
		if _, drop := seen[e.SessionID]; drop {
			continue
		}
		kept = append(kept, e)
	}
	return saveAgents(db, beadsDir, ticketID, kept)
}
