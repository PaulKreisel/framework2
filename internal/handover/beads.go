package handover

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"framework2/internal/bdcli"
)

// Bead is the decoded JSONL row from `bd export`, plus a few fields we
// surface to other helpers (parent/children lookup). The Raw map preserves
// every field bd emitted so we can re-marshal without losing schema we
// don't model in Go.
type Bead struct {
	ID       string
	Title    string
	ParentID string
	// Children is the set of subtree ids whose parent-child dep points at
	// this bead. Populated by loadSubtree from the inverse index.
	Children []string
	// Raw is the original JSON object emitted by `bd export`. We mutate
	// clones of it (via cloneJSONMap) to strip host-specific metadata.dirs
	// fields before serialising.
	Raw map[string]any
}

// loadSubtree runs `bd export --no-memories`, indexes every row, then walks
// the parent-child dep graph from root outward to collect the subtree. Returns
// a map keyed by bead id and the deterministic emit order (root first, then
// BFS by child).
func loadSubtree(db, beadsDir, root string) (map[string]*Bead, []string, error) {
	cmd := bdcli.Cmd(beadsDir, db, "export", "--no-memories")
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, nil, fmt.Errorf("bd export: %v: %s", err, ee.Stderr)
		}
		return nil, nil, fmt.Errorf("bd export: %w", err)
	}
	all := map[string]*Bead{}
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			return nil, nil, fmt.Errorf("parse bd export row: %w", err)
		}
		id, _ := raw["id"].(string)
		if id == "" {
			continue
		}
		b := &Bead{
			ID:    id,
			Title: strOr(raw["title"]),
			Raw:   raw,
		}
		b.ParentID = parentFromDeps(raw)
		all[id] = b
	}
	if _, ok := all[root]; !ok {
		return nil, nil, fmt.Errorf("ticket %s not found in export", root)
	}

	childIdx := map[string][]string{}
	for id, b := range all {
		if b.ParentID != "" {
			childIdx[b.ParentID] = append(childIdx[b.ParentID], id)
		}
	}

	order := []string{root}
	subtree := map[string]*Bead{root: all[root]}
	queue := []string{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		children := childIdx[cur]
		sortStrings(children)
		all[cur].Children = children
		for _, c := range children {
			if _, seen := subtree[c]; seen {
				continue
			}
			subtree[c] = all[c]
			order = append(order, c)
			queue = append(queue, c)
		}
	}
	return subtree, order, nil
}

func parentFromDeps(raw map[string]any) string {
	deps, ok := raw["dependencies"].([]any)
	if !ok {
		return ""
	}
	for _, d := range deps {
		dm, ok := d.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := dm["type"].(string); t != "parent-child" {
			continue
		}
		if pid, _ := dm["depends_on_id"].(string); pid != "" {
			return pid
		}
	}
	return ""
}

// subtreeBeadsRaw returns one host-stripped raw row per subtree bead, in
// the deterministic order produced by loadSubtree. Each row is a deep
// clone of the export payload with host-specific keys removed from
// metadata.dirs[]; the branch field is preserved so the reviewer can still
// see which branch a ticket lived on.
func subtreeBeadsRaw(order []string, beads map[string]*Bead) []map[string]any {
	out := make([]map[string]any, 0, len(order))
	for _, id := range order {
		raw := cloneJSONMap(beads[id].Raw)
		stripHostDirs(raw)
		out = append(out, raw)
	}
	return out
}

// stripHostDirs rewrites metadata.dirs[] inside raw to remove host-specific
// keys (path, localRepo, bareRepo, repo). The branch field is preserved.
func stripHostDirs(raw map[string]any) {
	md, ok := raw["metadata"].(map[string]any)
	if !ok {
		return
	}
	dirs, ok := md["dirs"].([]any)
	if !ok {
		return
	}
	for i, d := range dirs {
		entry, ok := d.(map[string]any)
		if !ok {
			continue
		}
		delete(entry, "path")
		delete(entry, "localRepo")
		delete(entry, "bareRepo")
		delete(entry, "repo")
		dirs[i] = entry
	}
	md["dirs"] = dirs
}

// cloneJSON returns a deep copy of a JSON-shaped tree. Necessary because
// stripHostDirs mutates in place and we don't want to disturb the in-memory
// Bead.Raw used elsewhere.
func cloneJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, vv := range t {
			out[k] = cloneJSON(vv)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, vv := range t {
			out[i] = cloneJSON(vv)
		}
		return out
	default:
		return v
	}
}

func cloneJSONMap(v map[string]any) map[string]any {
	return cloneJSON(v).(map[string]any)
}

func strOr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
