package handover

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"framework2/internal/bdcli"
)

// Bead is the decoded JSONL row from `bd export`, plus a few fields we
// surface in rendering. The Raw map preserves every field bd emitted so we
// can re-marshal without losing schema we don't model in Go.
type Bead struct {
	ID       string
	Title    string
	Status   string
	Owner    string
	Created  string
	Updated  string
	ParentID string
	// Children is the set of subtree ids whose parent-child dep points at
	// this bead. Populated by loadSubtree from the inverse index.
	Children []string
	// Raw is the original JSON object emitted by `bd export`. We mutate it
	// in place to strip host-specific metadata.dirs fields before writing.
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
			ID:      id,
			Title:   strOr(raw["title"]),
			Status:  strOr(raw["status"]),
			Owner:   strOr(raw["owner"]),
			Created: strOr(raw["created_at"]),
			Updated: strOr(raw["updated_at"]),
			Raw:     raw,
		}
		b.ParentID = parentFromDeps(raw)
		all[id] = b
	}
	if _, ok := all[root]; !ok {
		return nil, nil, fmt.Errorf("ticket %s not found in export", root)
	}

	// Inverse index: parent → children. Restricted to ids present in the
	// export so we don't trip on dangling parents.
	childIdx := map[string][]string{}
	for id, b := range all {
		if b.ParentID != "" {
			childIdx[b.ParentID] = append(childIdx[b.ParentID], id)
		}
	}

	// BFS from root, preserving discovery order and de-duplicating against
	// stable visits.
	order := []string{root}
	subtree := map[string]*Bead{root: all[root]}
	queue := []string{root}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		children := childIdx[cur]
		// Sort children for deterministic output.
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
		// The owning bead depends-on its parent; depends_on_id is the
		// parent's id.
		if pid, _ := dm["depends_on_id"].(string); pid != "" {
			return pid
		}
	}
	return ""
}

// writeBeadsJSONL emits one JSONL row per subtree bead, in the order
// produced by loadSubtree. Host-specific fields inside metadata.dirs[] are
// stripped on the way out (path, localRepo, bareRepo); branch is kept.
func writeBeadsJSONL(dirPath string, order []string, beads map[string]*Bead) error {
	f, err := os.Create(filepath.Join(dirPath, "beads.jsonl"))
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for _, id := range order {
		raw := cloneJSONMap(beads[id].Raw)
		stripHostDirs(raw)
		if err := enc.Encode(raw); err != nil {
			return err
		}
	}
	return nil
}

// stripHostDirs rewrites metadata.dirs[] inside raw to remove host-specific
// keys (path, localRepo, bareRepo). The branch field is preserved so the
// reviewer can still see which branch a ticket lived on.
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

// cloneJSON returns a deep copy but typed as a map for the top-level row.
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
	// Avoid pulling in sort just for a tiny slice; insertion sort is fine.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
