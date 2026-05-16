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

// RunAgent prints the agents that are currently `waiting` for input across the
// descendants of ticketID. recursionDepth bounds the walk: 1 means direct
// children only, 2 means children + grandchildren, and so on; any value <= 0
// is treated as unlimited and walks the entire subtree.
//
// Output is a tab-aligned table whose columns mirror `ticket agent list` with
// a leading TICKET column. Rows are sorted with mode=agent before mode=spec,
// and within each mode by ticket id. Empty result prints nothing — there is
// no header row when there is no data to show, matching `ticket next` on a
// leaf.
//
// The walk fetches one level at a time via `bd children`, which returns each
// child's metadata (including agents) in a single call. We descend into every
// non-leaf node until depth runs out; we cannot short-circuit on leaves
// without an explicit "has children" signal from bd, which today's JSON
// surface doesn't provide.
func RunAgent(db, beadsDir, ticketID string, recursionDepth int, stdout io.Writer) error {
	if ticketID == "" {
		return fmt.Errorf("ticket id required")
	}
	type row struct {
		ticket string
		entry  agent.AgentEntry
	}
	var rows []row

	type frame struct {
		id    string
		depth int // remaining levels we are allowed to descend into
	}
	queue := []frame{{id: ticketID, depth: recursionDepth}}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		if f.depth == 0 {
			continue
		}
		children, err := fetchChildren(beadsDir, db, f.id)
		if err != nil {
			return err
		}
		nextDepth := f.depth - 1
		if f.depth < 0 {
			nextDepth = f.depth // stay negative — "unlimited" is sticky
		}
		for _, c := range children {
			for _, e := range c.agents {
				if agent.StateOf(e) == agent.StatusWaiting {
					rows = append(rows, row{ticket: c.id, entry: e})
				}
			}
			if nextDepth != 0 {
				queue = append(queue, frame{id: c.id, depth: nextDepth})
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

// childRecord pairs a child's id with the agents recorded on it. Other
// metadata fields are intentionally dropped — the walker only ever needs the
// agents array.
type childRecord struct {
	id     string
	agents []agent.AgentEntry
}

func fetchChildren(beadsDir, db, parentID string) ([]childRecord, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "children", parentID).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("bd children %s: %v: %s", parentID, err, ee.Stderr)
		}
		return nil, fmt.Errorf("bd children %s: %w", parentID, err)
	}
	var rows []struct {
		ID       string                     `json:"id"`
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse bd children output for %s: %w", parentID, err)
	}
	results := make([]childRecord, 0, len(rows))
	for _, r := range rows {
		rec := childRecord{id: r.ID}
		raw, ok := r.Metadata["agents"]
		if ok && len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &rec.agents); err != nil {
				return nil, fmt.Errorf("parse agents for %s: %w", r.ID, err)
			}
		}
		results = append(results, rec)
	}
	return results, nil
}
