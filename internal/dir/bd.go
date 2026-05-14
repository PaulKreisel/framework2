package dir

import (
	"encoding/json"
	"fmt"
	"os/exec"
)

func bdArgs(db string, rest ...string) []string {
	args := []string{}
	if db != "" {
		args = append(args, "--db", db)
	}
	return append(args, rest...)
}

// Load returns the linked-dir entries stored under the ticket's metadata.dirs
// key. Returns nil (no error) when the ticket has no entries yet.
func Load(db, ticketID string) ([]Entry, error) {
	return load(db, ticketID)
}

func load(db, ticketID string) ([]Entry, error) {
	out, err := exec.Command("bd", bdArgs(db, "--json", "show", ticketID)...).Output()
	if err != nil {
		return nil, fmt.Errorf("look up ticket %s: %w", ticketID, withStderr(err))
	}
	var rows []struct {
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse ticket store output: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("ticket not found: %s", ticketID)
	}
	raw, ok := rows[0].Metadata["dirs"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var entries []Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse dir entries: %w", err)
	}
	return entries, nil
}

func save(db, ticketID string, entries []Entry) error {
	if entries == nil {
		entries = []Entry{}
	}
	payload, err := json.Marshal(map[string]any{"dirs": entries})
	if err != nil {
		return err
	}
	cmd := exec.Command("bd", bdArgs(db, "update", ticketID, "--metadata", string(payload))...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("update ticket %s: %w: %s", ticketID, err, out)
	}
	return nil
}

func withStderr(err error) error {
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, ee.Stderr)
	}
	return err
}
