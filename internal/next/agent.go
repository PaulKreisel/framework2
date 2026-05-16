package next

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"text/tabwriter"

	"framework2/internal/agent"
	"framework2/internal/bdcli"
)

// nonClosedBuiltinStatuses is the comma-separated --status filter we pass
// to `bd list`. bd has no native "not closed" inverse, so we enumerate the
// built-in non-closed statuses. Custom statuses (e.g. "new") are NOT
// included: bd 1.0.2 rejects unknown names in a multi-status filter on
// databases where the custom status isn't registered, which would break
// the testutil DBs. Acceptable trade-off because beads in custom statuses
// can't currently hold live `waiting` agents in practice.
const nonClosedBuiltinStatuses = "open,in_progress,blocked,deferred"

// RunAgent prints the agents that are currently `waiting` for input across the
// ticketID subtree. With includeRunning, `running` agents are listed alongside
// `waiting` ones; `exited` (dead-PID) records stay hidden either way. minDepth
// and maxDepth bound the walk by hop count from ticketID: 0 is the root
// itself, 1 is direct children, 2 is grandchildren, and so on. A node at
// depth d is included when minDepth <= d <= maxDepth. maxDepth < 0 means
// unlimited (walks the entire subtree). An empty ticketID matches every
// non-closed bead (no ancestry filter); minDepth/maxDepth then gate against
// a synthetic depth of 1.
//
// Output is a tab-aligned table whose columns mirror `ticket agent list` with
// a leading TICKET column. Rows are sorted with mode=agent before mode=spec,
// and within each mode by ticket id. Empty result prints nothing — there is
// no header row when there is no data to show, matching `ticket next` on a
// leaf.
//
// Implementation: instead of a per-node BFS (one `bd children` round-trip
// per internal node), we fetch the entire candidate set in one shot via
// `bd list --has-metadata-key agents --status <non-closed>` and — only when
// a concrete root was requested — verify ancestry against a second
// `bd list --status <non-closed> --flat` call that supplies the parent map.
// Closed beads with stale `agents` entries never enter the candidate set,
// so a `waiting` wrapper recorded on a manually-closed bead is silently
// hidden here; `ticket agent list <closed-id>` still surfaces it.
func RunAgent(db, beadsDir, ticketID string, minDepth, maxDepth int, includeRunning bool, stdout io.Writer) error {
	candidates, err := bdJSONList(beadsDir, db, "--has-metadata-key", "agents", "--status", nonClosedBuiltinStatuses, "--json", "-n", "0")
	if err != nil {
		return err
	}

	// depthOf[id] gives the candidate's hop count from ticketID along the
	// open-bead parent chain. A value of 0 means "is the root itself"; >0
	// means a descendant at that depth. Inclusion is gated below by the
	// [minDepth, maxDepth] window. When ticketID is empty we skip ancestry
	// resolution entirely and include every candidate at synthetic depth 1.
	depthOf := make(map[string]int, len(candidates))
	if ticketID == "" {
		for _, c := range candidates {
			depthOf[c.ID] = 1
		}
	} else {
		all, err := bdJSONList(beadsDir, db, "--status", nonClosedBuiltinStatuses, "--json", "-n", "0", "--flat")
		if err != nil {
			return err
		}
		parent := make(map[string]string, len(all))
		for _, r := range all {
			parent[r.ID] = r.Parent
		}
		for _, c := range candidates {
			d, ok := hopsToRoot(c.ID, ticketID, parent)
			if !ok {
				continue
			}
			depthOf[c.ID] = d
		}
	}

	type row struct {
		ticket string
		entry  agent.AgentEntry
	}
	var rows []row
	for _, c := range candidates {
		d, ok := depthOf[c.ID]
		if !ok {
			continue
		}
		if d < minDepth {
			continue
		}
		if maxDepth >= 0 && d > maxDepth {
			continue
		}
		for _, e := range c.Agents {
			s := agent.StateOf(e)
			if s == agent.StatusWaiting || (includeRunning && s == agent.StatusRunning) {
				rows = append(rows, row{ticket: c.ID, entry: e})
			}
		}
	}

	if len(rows) == 0 {
		return nil
	}

	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].entry.Mode != rows[j].entry.Mode {
			// "agent" sorts before "spec".
			return rows[i].entry.Mode == "agent"
		}
		return rows[i].ticket < rows[j].ticket
	})

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TICKET\tSESSION ID\tMODE\tPID\tSTARTED\tSTATE\tENV")
	for _, r := range rows {
		e := r.entry
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			r.ticket, e.SessionID, e.Mode, e.PID,
			agent.FormatStarted(e.StartedAt), agent.StateOf(e), e.Env)
	}
	return tw.Flush()
}

// hopsToRoot walks `id` up the parent chain until it reaches root, runs out
// of parents, or revisits a node. Returns (hops, true) on success — 0 if
// id == root, >0 for a descendant — and (0, false) otherwise.
func hopsToRoot(id, root string, parent map[string]string) (int, bool) {
	seen := make(map[string]struct{})
	hops := 0
	cur := id
	for {
		if cur == root {
			return hops, true
		}
		if _, dup := seen[cur]; dup {
			return 0, false
		}
		seen[cur] = struct{}{}
		next, ok := parent[cur]
		if !ok || next == "" {
			return 0, false
		}
		cur = next
		hops++
	}
}

// issueRow is the slice of `bd list --json` output the agent walker reads.
// Other fields are intentionally dropped.
type issueRow struct {
	ID     string             `json:"id"`
	Parent string             `json:"parent"`
	Agents []agent.AgentEntry `json:"-"`
}

func (r *issueRow) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID       string                     `json:"id"`
		Parent   string                     `json:"parent"`
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	r.ID = raw.ID
	r.Parent = raw.Parent
	if blob, ok := raw.Metadata["agents"]; ok && len(blob) > 0 && string(blob) != "null" {
		if err := json.Unmarshal(blob, &r.Agents); err != nil {
			return fmt.Errorf("parse agents for %s: %w", raw.ID, err)
		}
	}
	return nil
}

func bdJSONList(beadsDir, db string, args ...string) ([]issueRow, error) {
	full := append([]string{"list"}, args...)
	out, err := bdcli.Cmd(beadsDir, db, full...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("bd %v: %v: %s", full, err, ee.Stderr)
		}
		return nil, fmt.Errorf("bd %v: %w", full, err)
	}
	var rows []issueRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse bd %v output: %w", full, err)
	}
	return rows, nil
}
