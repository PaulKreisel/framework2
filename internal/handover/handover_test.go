package handover_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/testutil"
)

// TestHandoverProducesJSONL exercises the end-to-end flow: subtree discovery
// produces one host-stripped bead row per line in a `.jsonl` file. Code is
// not shipped — reviewers reconstruct work from each bead's `metadata.prs`
// (which the trailer-rewrite from ai-vrc.57.12 already attributes back to
// the right subtask).
func TestHandoverProducesJSONL(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "handover-parent")
	child := testutil.CreateChildBead(t, beadsDir, "handover-child", parent)

	sessionID := "33333333-3333-4333-8333-333333333333"
	if err := agent.RegisterAgent("", beadsDir, child, agent.AgentEntry{
		SessionID: sessionID,
		Mode:      agent.ModeAgent.String(),
		Conversation: []agent.ConvEntry{
			{Type: agent.ConvTypeUser, Prompt: "Implement the ticket"},
			{Type: agent.ConvTypeAssistantReply, Text: "Done — see handover-child for details."},
		},
	}); err != nil {
		t.Fatalf("register agent: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "handover", parent)
	if code != 0 {
		t.Fatalf("handover exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("handover stdout empty")
	}
	jsonlPath := lines[len(lines)-1]

	wantPrefix := filepath.Join(beadsDir, "handovers") + string(os.PathSeparator)
	if !strings.HasPrefix(jsonlPath, wantPrefix) {
		t.Errorf("output not under beads handovers dir: %q (want prefix %q)", jsonlPath, wantPrefix)
	}
	if !strings.HasSuffix(jsonlPath, ".jsonl") {
		t.Errorf("expected .jsonl suffix, got %q", jsonlPath)
	}

	raw, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}

	rows := parseJSONL(t, raw)
	if len(rows) != 2 {
		t.Errorf("row count = %d, want 2", len(rows))
	}
	gotIDs := map[string]bool{}
	for _, b := range rows {
		id, _ := b["id"].(string)
		gotIDs[id] = true
		md, _ := b["metadata"].(map[string]any)
		if md == nil {
			continue
		}
		dirs, _ := md["dirs"].([]any)
		for _, d := range dirs {
			e, _ := d.(map[string]any)
			for _, banned := range []string{"path", "localRepo", "bareRepo", "repo"} {
				if _, has := e[banned]; has {
					t.Errorf("host field %q leaked into bead %s: %v", banned, id, e)
				}
			}
		}
	}
	if !gotIDs[parent] || !gotIDs[child] {
		t.Errorf("rows missing expected ids: got %v", gotIDs)
	}

	// Conversations live under metadata.agents[].conversation[]; the
	// sessionID we registered must round-trip.
	if !strings.Contains(string(raw), sessionID) {
		t.Errorf("expected session id %q in metadata.agents[]", sessionID)
	}
}

// TestHandoverHonoursOutputFlag covers the --output override: caller picks
// the exact final file path.
func TestHandoverHonoursOutputFlag(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	root := testutil.CreateBead(t, beadsDir, "handover-output-flag")
	custom := filepath.Join(t.TempDir(), "my-handover.jsonl")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "handover", root, "--output", custom)
	if code != 0 {
		t.Fatalf("handover exit=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Errorf("custom output file not produced: %v", err)
	}
	raw, err := os.ReadFile(custom)
	if err != nil {
		t.Fatalf("read custom output: %v", err)
	}
	rows := parseJSONL(t, raw)
	if len(rows) != 1 {
		t.Errorf("row count = %d, want 1", len(rows))
	}
}

// TestHandoverSubtreeOnlyExcludesAncestors confirms we honour the
// "subtree only" decision: ancestors of <id> never appear.
func TestHandoverSubtreeOnlyExcludesAncestors(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	root := testutil.CreateBead(t, beadsDir, "handover-subtree-root")
	mid := testutil.CreateChildBead(t, beadsDir, "handover-subtree-mid", root)
	leaf := testutil.CreateChildBead(t, beadsDir, "handover-subtree-leaf", mid)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "handover", mid)
	if code != 0 {
		t.Fatalf("handover exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	jsonlPath := lines[len(lines)-1]

	raw, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	rows := parseJSONL(t, raw)
	got := map[string]bool{}
	for _, b := range rows {
		if id, _ := b["id"].(string); id != "" {
			got[id] = true
		}
	}
	if got[root] {
		t.Errorf("ancestor %s leaked into rows", root)
	}
	if !got[mid] || !got[leaf] {
		t.Errorf("expected mid+leaf in rows, got %v", got)
	}
}

// TestHandoverRejectsNoArchive covers the cleanup of the legacy flag:
// --no-archive must no longer be accepted (it was previously the toggle
// between tarball and staging-directory output).
func TestHandoverRejectsNoArchive(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	root := testutil.CreateBead(t, beadsDir, "handover-rejects-no-archive")
	_, _, code := testutil.RunTicketIn(t, beadsDir, "handover", root, "--no-archive")
	if code == 0 {
		t.Errorf("expected non-zero exit when --no-archive is passed")
	}
}

// --- helpers ---

func parseJSONL(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var rows []map[string]any
	for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("parse jsonl line %d: %v", i, err)
		}
		rows = append(rows, row)
	}
	return rows
}
