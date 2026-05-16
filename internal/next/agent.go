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

// StateFilter values understood by [RunAgent].
//
// StateFilterNone disables state filtering — every recorded agent (including
// "exited" wrappers with a dead pid) is surfaced. This is the path
// `ticket agent list` takes.
//
// StateFilterWaiting is the original `ticket next agent` behaviour: only
// rows whose [agent.StateOf] is "waiting" appear (pair with includeRunning
// to also surface running wrappers, as `--all` does).
const (
	StateFilterNone    = ""
	StateFilterWaiting = agent.StatusWaiting
)

// RunAgent prints the agents stored on ticketID and (optionally) its
// descendants. It is the single renderer behind both `ticket next agent`
// (subtree walk) and `ticket agent list` (single-ticket via depth 0..0).
//
// Walk bounds: a node at hop count d from ticketID is included when
// minDepth <= d <= maxDepth. maxDepth < 0 means unlimited. An empty
// ticketID matches every non-closed bead with no ancestry filter and
// ignores minDepth/maxDepth entirely.
//
// State filter: stateFilter == "" lets every row through (this is how
// `agent list` surfaces exited records too). stateFilter == "waiting"
// keeps only waiting rows; pair with includeRunning to also surface
// running wrappers (the `--all` flag on `next agent`).
//
// Rendering modes:
//   - Single-ticket (ticketID != "" and minDepth == maxDepth == 0):
//     the columns match `ticket agent list` (no TICKET, with CONTEXT),
//     rows are newest-first, the header is always printed (even on empty
//     input), and the single ticket is read via [agent.LoadAgents] so a
//     manually-closed bead still surfaces its records.
//   - Subtree: the original `next agent` shape — TICKET column on the
//     front, no CONTEXT, rows sorted (mode=agent before mode=spec, then
//     by ticket id), and an empty result prints nothing (no header row).
//
// jsonOutput emits indented JSON instead of a table. Single-ticket mode
// emits `[]agent.AgentEntry` (matching the existing `agent list --json`
// shape); subtree mode emits objects that embed the entry plus a
// `ticket` field.
//
// Implementation: subtree mode fetches the entire candidate set in one
// `bd list --has-metadata-key agents --status <non-closed>` call and
// (when a concrete root is supplied) verifies ancestry via a second
// `bd list --status <non-closed> --flat`. Closed beads never enter the
// subtree candidate set, so a `waiting` wrapper recorded on a
// manually-closed bead stays hidden there; the single-ticket path
// reaches it via [agent.LoadAgents].
func RunAgent(
	db, beadsDir, ticketID string,
	minDepth, maxDepth int,
	includeRunning bool,
	stateFilter string,
	jsonOutput bool,
	stdout io.Writer,
) error {
	singleTicket := ticketID != "" && minDepth == 0 && maxDepth == 0

	var rows []agentRow

	if singleTicket {
		entries, err := agent.LoadAgents(db, beadsDir, ticketID)
		if err != nil {
			return err
		}
		// Newest first — entries are stored oldest-first.
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			if !matchesStateFilter(agent.StateOf(e), stateFilter, includeRunning) {
				continue
			}
			rows = append(rows, agentRow{ticket: ticketID, entry: e})
		}
	} else {
		candidates, err := bdJSONList(beadsDir, db, "--has-metadata-key", "agents", "--status", nonClosedBuiltinStatuses, "--json", "-n", "0")
		if err != nil {
			return err
		}

		// depthOf[id] gives the candidate's hop count from ticketID along
		// the open-bead parent chain when a concrete root was supplied.
		// With an empty ticketID we skip ancestry resolution entirely and
		// include every candidate regardless of minDepth/maxDepth.
		var depthOf map[string]int
		if ticketID != "" {
			all, err := bdJSONList(beadsDir, db, "--status", nonClosedBuiltinStatuses, "--json", "-n", "0", "--flat")
			if err != nil {
				return err
			}
			parent := make(map[string]string, len(all))
			for _, r := range all {
				parent[r.ID] = r.Parent
			}
			depthOf = make(map[string]int, len(candidates))
			for _, c := range candidates {
				d, ok := hopsToRoot(c.ID, ticketID, parent)
				if !ok {
					continue
				}
				depthOf[c.ID] = d
			}
		}

		for _, c := range candidates {
			if ticketID != "" {
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
			}
			for _, e := range c.Agents {
				if !matchesStateFilter(agent.StateOf(e), stateFilter, includeRunning) {
					continue
				}
				rows = append(rows, agentRow{ticket: c.ID, entry: e})
			}
		}

		sort.SliceStable(rows, func(i, j int) bool {
			if rows[i].entry.Mode != rows[j].entry.Mode {
				// "agent" sorts before "spec".
				return rows[i].entry.Mode == "agent"
			}
			return rows[i].ticket < rows[j].ticket
		})
	}

	if jsonOutput {
		return renderJSON(rows, singleTicket, stdout)
	}
	if singleTicket {
		return renderSingleTicketTable(rows, stdout)
	}
	return renderSubtreeTable(rows, stdout)
}

// matchesStateFilter implements the inclusion rule documented on
// [RunAgent]: an empty stateFilter accepts every row, a non-empty
// stateFilter accepts rows whose state equals it, and includeRunning
// additionally accepts "running".
func matchesStateFilter(state, stateFilter string, includeRunning bool) bool {
	if stateFilter == "" {
		return true
	}
	if state == stateFilter {
		return true
	}
	return includeRunning && state == agent.StatusRunning
}

// agentRow pairs an agent entry with its owning ticket id during a walk.
// Kept as a package-level type so the renderer helpers can take a slice
// of it without re-declaring the shape.
type agentRow struct {
	ticket string
	entry  agent.AgentEntry
}

// renderJSON emits rows as indented JSON. Single-ticket mode emits a bare
// []agent.AgentEntry to match the existing `agent list --json` contract;
// subtree mode wraps each entry with its owning ticket id.
func renderJSON(rows []agentRow, singleTicket bool, stdout io.Writer) error {
	var payload any
	if singleTicket {
		out := make([]agent.AgentEntry, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.entry)
		}
		payload = out
	} else {
		type subtreeRow struct {
			Ticket string `json:"ticket"`
			agent.AgentEntry
		}
		out := make([]subtreeRow, 0, len(rows))
		for _, r := range rows {
			out = append(out, subtreeRow{Ticket: r.ticket, AgentEntry: r.entry})
		}
		payload = out
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(b))
	return err
}

func renderSingleTicketTable(rows []agentRow, stdout io.Writer) error {
	// Header is always emitted in single-ticket mode — matches the legacy
	// `ticket agent list` empty-table behaviour.
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION ID\tMODE\tPID\tSTARTED\tSTATE\tCONTEXT\tENV\tWAITING REASON")
	for _, r := range rows {
		e := r.entry
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\n",
			e.SessionID, e.Mode, e.PID, agent.FormatStarted(e.StartedAt),
			agent.StateOf(e), agent.FormatContextTokens(e.ContextTokens), e.Env,
			agent.FormatWaitingReason(e))
	}
	return tw.Flush()
}

func renderSubtreeTable(rows []agentRow, stdout io.Writer) error {
	// Subtree mode matches the legacy `ticket next agent` shape: no
	// header on an empty result.
	if len(rows) == 0 {
		return nil
	}
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
