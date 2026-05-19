package ticketcli_test

import (
	"encoding/json"
	"slices"
	"sync"
	"testing"

	"framework2/internal/ready"
	"framework2/internal/testutil"
)

// TestAgentOnBlockedTicketCascades exercises the routing in `runAgent`: when
// the target is currently blocked, the dispatcher must hand off to
// [ready.HandleBlocked] and exit cleanly without attempting to claim the
// bead or exec claude.
func TestAgentOnBlockedTicketCascades(t *testing.T) {
	// No t.Parallel: stubs the package-level ready.SpawnAgentWindow var.
	beadsDir := testutil.TestDB(t)

	var mu sync.Mutex
	var captured []string
	prev := ready.SpawnAgentWindow
	ready.SpawnAgentWindow = func(id string) error {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, id)
		return nil
	}
	t.Cleanup(func() { ready.SpawnAgentWindow = prev })

	target := testutil.CreateBead(t, beadsDir, "agent-blocked-target")
	blocker := testutil.CreateBead(t, beadsDir, "agent-blocked-blocker")
	if out, err := testutil.BdCmd(beadsDir, "dep", "add", target, blocker).CombinedOutput(); err != nil {
		t.Fatalf("bd dep add: %v\n%s", err, out)
	}

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", target)
	if code != 0 {
		t.Fatalf("ticket agent on blocked target exit=%d stderr=%q", code, stderr)
	}

	mu.Lock()
	got := append([]string(nil), captured...)
	mu.Unlock()
	if len(got) != 1 || got[0] != blocker {
		t.Errorf("spawns = %v, want [%s]", got, blocker)
	}

	// Target keeps `open` status (no claim) and gains the ready label
	// alongside the blocker.
	for _, id := range []string{target, blocker} {
		labels := bdLabels(t, beadsDir, id)
		if !slices.Contains(labels, ready.Label) {
			t.Errorf("%s labels = %v; missing %q", id, labels, ready.Label)
		}
	}
	if status := beadStatus(t, beadsDir, target); status == "in_progress" {
		t.Errorf("target unexpectedly claimed (status=%s); the blocked path must not call Claim", status)
	}
}

func bdLabels(t *testing.T, beadsDir, id string) []string {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "show", id).Output()
	if err != nil {
		t.Fatalf("bd show %s: %v", id, err)
	}
	var rows []struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse labels: %v", err)
	}
	if len(rows) == 0 {
		return nil
	}
	return rows[0].Labels
}

func beadStatus(t *testing.T, beadsDir, id string) string {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "show", id).Output()
	if err != nil {
		t.Fatalf("bd show %s: %v", id, err)
	}
	var rows []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse status: %v", err)
	}
	if len(rows) == 0 {
		return ""
	}
	return rows[0].Status
}
