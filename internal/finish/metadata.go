package finish

import (
	"encoding/json"
	"fmt"
	"os/exec"

	"framework2/internal/bdcli"
)

// handoverMeta is the JSON shape stored under a bead's metadata.handover key.
// Contributors counts the number of child beads that have already produced at
// least one commit on this bead (i.e. its "parent" role). ContributionIndex
// records this bead's own slot under its parent (i.e. its "child" role). A
// single bead can carry both fields once it sits in the middle of a stack.
type handoverMeta struct {
	Contributors      int `json:"contributors,omitempty"`
	ContributionIndex int `json:"contributionIndex,omitempty"`
}

// loadHandoverMeta reads metadata.handover for ticketID. Returns the zero
// value when the key is missing.
func loadHandoverMeta(db, beadsDir, ticketID string) (handoverMeta, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "show", ticketID).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return handoverMeta{}, fmt.Errorf("look up ticket %s: %v: %s", ticketID, err, ee.Stderr)
		}
		return handoverMeta{}, fmt.Errorf("look up ticket %s: %w", ticketID, err)
	}
	var rows []struct {
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return handoverMeta{}, fmt.Errorf("parse ticket store output: %w", err)
	}
	if len(rows) == 0 {
		return handoverMeta{}, fmt.Errorf("ticket not found: %s", ticketID)
	}
	raw, ok := rows[0].Metadata["handover"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return handoverMeta{}, nil
	}
	var hm handoverMeta
	if err := json.Unmarshal(raw, &hm); err != nil {
		return handoverMeta{}, fmt.Errorf("parse handover metadata for %s: %w", ticketID, err)
	}
	return hm, nil
}

// saveHandoverMeta replaces the metadata.handover key on ticketID with hm.
// Other top-level metadata keys are preserved by bd's key-level merge.
func saveHandoverMeta(db, beadsDir, ticketID string, hm handoverMeta) error {
	payload, err := json.Marshal(map[string]any{"handover": hm})
	if err != nil {
		return err
	}
	out, err := bdcli.Cmd(beadsDir, db, "update", ticketID, "--metadata", string(payload)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("update ticket %s handover metadata: %w: %s", ticketID, err, out)
	}
	return nil
}

// allocateContributionIndex returns the index N this child should use under
// parent and a flag distinguishing fresh allocations from reuses. If the
// child already has a contributionIndex set (resume after a partial run),
// that value is reused (reused = true) and the parent's contributors counter
// is left untouched. Otherwise the parent's contributors is incremented and
// N is the new value, also written back to the child.
//
// The reused flag drives the per-commit rewrite's idempotency check: on a
// fresh allocation N is by definition unused on any rebased commit, so the
// rewrite always runs; on a reuse, a previous partial run may have already
// rewritten some commits with this same N, and the rewrite must detect that
// to avoid double-prepending.
//
// Both writes go to separate beads, so they can't race against each other on
// the same metadata key — the parent and child are always distinct rows.
func allocateContributionIndex(db, beadsDir, parentID, childID string) (n int, reused bool, err error) {
	childMeta, err := loadHandoverMeta(db, beadsDir, childID)
	if err != nil {
		return 0, false, err
	}
	if childMeta.ContributionIndex > 0 {
		return childMeta.ContributionIndex, true, nil
	}
	parentMeta, err := loadHandoverMeta(db, beadsDir, parentID)
	if err != nil {
		return 0, false, err
	}
	n = parentMeta.Contributors + 1
	parentMeta.Contributors = n
	if err := saveHandoverMeta(db, beadsDir, parentID, parentMeta); err != nil {
		return 0, false, err
	}
	childMeta.ContributionIndex = n
	if err := saveHandoverMeta(db, beadsDir, childID, childMeta); err != nil {
		return 0, false, err
	}
	return n, false, nil
}
