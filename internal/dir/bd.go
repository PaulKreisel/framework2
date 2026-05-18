package dir

import (
	"encoding/json"
	"fmt"
	"os/exec"

	"framework2/internal/bdcli"
)

// Load returns the linked-dir entries stored under the ticket's metadata.dirs
// key. Returns nil (no error) when the ticket has no entries yet.
func Load(db, beadsDir, ticketID string) ([]Entry, error) {
	return load(db, beadsDir, ticketID)
}

func load(db, beadsDir, ticketID string) ([]Entry, error) {
	row, err := showTicket(db, beadsDir, ticketID)
	if err != nil {
		return nil, err
	}
	raw, ok := row.Metadata["dirs"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var entries []Entry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse dir entries: %w", err)
	}
	return entries, nil
}

// ParentID returns the immediate parent bead id, or "" if the bead is a root.
func ParentID(db, beadsDir, ticketID string) (string, error) {
	return parentID(db, beadsDir, ticketID)
}

// LoadAncestorEntries walks the parent chain of ticketID and returns the
// concatenated dir entries of every ancestor (immediate parent first). The
// current ticket's own entries are NOT included. Callers should de-dupe as
// needed — the same path can appear under multiple ancestors when worktree
// cascades have propagated it.
func LoadAncestorEntries(db, beadsDir, ticketID string) ([]Entry, error) {
	var out []Entry
	cur := ticketID
	for {
		p, err := parentID(db, beadsDir, cur)
		if err != nil {
			return nil, err
		}
		if p == "" {
			return out, nil
		}
		es, err := load(db, beadsDir, p)
		if err != nil {
			return nil, err
		}
		out = append(out, es...)
		cur = p
	}
}

// parentID returns the immediate parent bead id, or "" if the bead is a root.
func parentID(db, beadsDir, ticketID string) (string, error) {
	row, err := showTicket(db, beadsDir, ticketID)
	if err != nil {
		return "", err
	}
	return row.Parent, nil
}

type ticketRow struct {
	Metadata    map[string]json.RawMessage `json:"metadata"`
	Parent      string                     `json:"parent"`
	IssueType   string                     `json:"issue_type"`
	ExternalRef string                     `json:"external_ref"`
}

// IssueType returns the bead's type ("bug", "feature", "task", …) or "" if
// the field is missing.
func IssueType(db, beadsDir, ticketID string) (string, error) {
	row, err := showTicket(db, beadsDir, ticketID)
	if err != nil {
		return "", err
	}
	return row.IssueType, nil
}

func showTicket(db, beadsDir, ticketID string) (*ticketRow, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "show", ticketID).Output()
	if err != nil {
		return nil, fmt.Errorf("look up ticket %s: %w", ticketID, withStderr(err))
	}
	var rows []ticketRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse ticket store output: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("ticket not found: %s", ticketID)
	}
	return &rows[0], nil
}

func save(db, beadsDir, ticketID string, entries []Entry) error {
	if entries == nil {
		entries = []Entry{}
	}
	payload, err := json.Marshal(map[string]any{"dirs": entries})
	if err != nil {
		return err
	}
	cmd := bdcli.Cmd(beadsDir, db, "update", ticketID, "--metadata", string(payload))
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
