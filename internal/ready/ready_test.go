// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package ready_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"framework2/internal/ready"
	"framework2/internal/testutil"
)

// stubSpawn replaces ready.SpawnAgentWindow with a recorder for the duration
// of the test. Returns a function that yields the captured ids in launch
// order.
func stubSpawn(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var captured []string
	prev := ready.SpawnAgentWindow
	ready.SpawnAgentWindow = func(id, _ string) error {
		mu.Lock()
		defer mu.Unlock()
		captured = append(captured, id)
		return nil
	}
	t.Cleanup(func() { ready.SpawnAgentWindow = prev })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := make([]string, len(captured))
		copy(out, captured)
		return out
	}
}

func labelsOf(t *testing.T, beadsDir, ticketID string) []string {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "show", ticketID).Output()
	if err != nil {
		t.Fatalf("bd show %s: %v", ticketID, err)
	}
	var rows []struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse bd show %s: %v", ticketID, err)
	}
	if len(rows) == 0 {
		return nil
	}
	return rows[0].Labels
}

func TestSetAddsReadyLabel(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "set-ready")
	if err := ready.Set("", beadsDir, id); err != nil {
		t.Fatalf("ready.Set: %v", err)
	}
	if !slices.Contains(labelsOf(t, beadsDir, id), ready.Label) {
		t.Errorf("ready.Set did not add the %q label", ready.Label)
	}
}

func TestRemoveDropsReadyLabel(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "remove-ready")
	if err := ready.Set("", beadsDir, id); err != nil {
		t.Fatalf("seed Set: %v", err)
	}
	if err := ready.Remove("", beadsDir, id); err != nil {
		t.Fatalf("ready.Remove: %v", err)
	}
	if slices.Contains(labelsOf(t, beadsDir, id), ready.Label) {
		t.Errorf("ready.Remove did not drop the %q label", ready.Label)
	}
}

func TestHandleBlockedUnblockedReturnsFalse(t *testing.T) {
	// No t.Parallel: stubSpawn mutates the package-level
	// ready.SpawnAgentWindow var.
	beadsDir := testutil.TestDB(t)
	captured := stubSpawn(t)
	id := testutil.CreateBead(t, beadsDir, "unblocked")

	var buf bytes.Buffer
	handled, err := ready.HandleBlocked("", beadsDir, id, &buf)
	if err != nil {
		t.Fatalf("HandleBlocked: %v", err)
	}
	if handled {
		t.Errorf("HandleBlocked returned handled=true on a non-blocked ticket")
	}
	if got := captured(); len(got) != 0 {
		t.Errorf("expected no spawns, got %v", got)
	}
	if slices.Contains(labelsOf(t, beadsDir, id), ready.Label) {
		t.Errorf("HandleBlocked stamped ready on a non-blocked ticket")
	}
}

// addBlocker wires blocker → blocks → blocked via `bd dep add`.
func addBlocker(t *testing.T, beadsDir, blocked, blocker string) {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "dep", "add", blocked, blocker).CombinedOutput()
	if err != nil {
		t.Fatalf("bd dep add %s %s: %v\n%s", blocked, blocker, err, out)
	}
}

func TestHandleBlockedSingleBlockerCascades(t *testing.T) {
	// No t.Parallel: stubSpawn mutates the package-level
	// ready.SpawnAgentWindow var.
	beadsDir := testutil.TestDB(t)
	captured := stubSpawn(t)

	target := testutil.CreateBead(t, beadsDir, "target")
	blocker := testutil.CreateBead(t, beadsDir, "single-blocker")
	addBlocker(t, beadsDir, target, blocker)

	var buf bytes.Buffer
	handled, err := ready.HandleBlocked("", beadsDir, target, &buf)
	if err != nil {
		t.Fatalf("HandleBlocked: %v", err)
	}
	if !handled {
		t.Fatal("HandleBlocked returned handled=false on a blocked ticket")
	}
	// Both ticket and blocker are stamped.
	for _, id := range []string{target, blocker} {
		if !slices.Contains(labelsOf(t, beadsDir, id), ready.Label) {
			t.Errorf("%s missing %q label", id, ready.Label)
		}
	}
	// Single root spawned: the only blocker, which is itself unblocked.
	got := captured()
	if len(got) != 1 || got[0] != blocker {
		t.Errorf("spawns=%v want %v", got, []string{blocker})
	}
}

func TestHandleBlockedTransitiveLaunchesOnlyRoots(t *testing.T) {
	// No t.Parallel: stubSpawn mutates the package-level
	// ready.SpawnAgentWindow var.
	beadsDir := testutil.TestDB(t)
	captured := stubSpawn(t)

	// target ← mid ← root (root blocks mid, mid blocks target).
	target := testutil.CreateBead(t, beadsDir, "trans-target")
	mid := testutil.CreateBead(t, beadsDir, "trans-mid")
	root := testutil.CreateBead(t, beadsDir, "trans-root")
	addBlocker(t, beadsDir, target, mid)
	addBlocker(t, beadsDir, mid, root)

	var buf bytes.Buffer
	handled, err := ready.HandleBlocked("", beadsDir, target, &buf)
	if err != nil {
		t.Fatalf("HandleBlocked: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}
	// All three carry `ready`.
	for _, id := range []string{target, mid, root} {
		if !slices.Contains(labelsOf(t, beadsDir, id), ready.Label) {
			t.Errorf("%s missing %q label", id, ready.Label)
		}
	}
	// Only the unblocked root is spawned; mid is still blocked by root.
	got := captured()
	if len(got) != 1 || got[0] != root {
		t.Errorf("spawns=%v want %v", got, []string{root})
	}
}

func TestHandleBlockedDoubleRootLaunchesBoth(t *testing.T) {
	// No t.Parallel: stubSpawn mutates the package-level
	// ready.SpawnAgentWindow var.
	beadsDir := testutil.TestDB(t)
	captured := stubSpawn(t)

	target := testutil.CreateBead(t, beadsDir, "two-root-target")
	a := testutil.CreateBead(t, beadsDir, "two-root-a")
	b := testutil.CreateBead(t, beadsDir, "two-root-b")
	addBlocker(t, beadsDir, target, a)
	addBlocker(t, beadsDir, target, b)

	var buf bytes.Buffer
	handled, err := ready.HandleBlocked("", beadsDir, target, &buf)
	if err != nil {
		t.Fatalf("HandleBlocked: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true")
	}
	got := captured()
	slices.Sort(got)
	want := []string{a, b}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("spawns=%v want %v", got, want)
	}
}

func TestDefaultSpawnAgentWindowExportsBeadsDir(t *testing.T) {
	// No t.Parallel: stubs the package-level ready.ExecOsascript var.
	var captured string
	prev := ready.ExecOsascript
	ready.ExecOsascript = func(script string) (string, error) {
		captured = script
		return "", nil
	}
	t.Cleanup(func() { ready.ExecOsascript = prev })

	const beadsDir = "/some/other/beads dir"
	const ticketID = "ai-vrc.76"
	if err := ready.DefaultSpawnAgentWindow(ticketID, beadsDir); err != nil {
		// On non-darwin runners the function refuses to spawn. That's
		// not the path we're testing, so skip rather than fail.
		t.Skipf("DefaultSpawnAgentWindow: %v", err)
	}
	// The do-script must export BEADS_DIR before exec'ing ticket, so
	// the new shell doesn't fall back to the user-profile default.
	wantBeadsDir := "BEADS_DIR='/some/other/beads dir'"
	if !strings.Contains(captured, wantBeadsDir) {
		t.Errorf("osascript = %q\n  missing %q", captured, wantBeadsDir)
	}
	if !strings.Contains(captured, "agent '"+ticketID+"'") {
		t.Errorf("osascript = %q\n  missing agent %s", captured, ticketID)
	}
}

func TestLaunchReadyFollowupsPassesBeadsDirToSpawn(t *testing.T) {
	// No t.Parallel: stubs the package-level ready.SpawnAgentWindow var.
	beadsDir := testutil.TestDB(t)

	var mu sync.Mutex
	type call struct{ id, beadsDir string }
	var calls []call
	prev := ready.SpawnAgentWindow
	ready.SpawnAgentWindow = func(id, bd string) error {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, call{id, bd})
		return nil
	}
	t.Cleanup(func() { ready.SpawnAgentWindow = prev })

	finished := testutil.CreateBead(t, beadsDir, "spawn-arg-finished")
	follow := testutil.CreateBead(t, beadsDir, "spawn-arg-follow")
	addBlocker(t, beadsDir, follow, finished)
	if err := ready.Set("", beadsDir, follow); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if out, err := testutil.BdCmd(beadsDir, "close", finished).CombinedOutput(); err != nil {
		t.Fatalf("bd close: %v\n%s", err, out)
	}

	var buf bytes.Buffer
	if err := ready.LaunchReadyFollowups("", beadsDir, finished, &buf); err != nil {
		t.Fatalf("LaunchReadyFollowups: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0].id != follow || calls[0].beadsDir != beadsDir {
		t.Errorf("spawn calls = %+v; want one call with id=%s beadsDir=%s", calls, follow, beadsDir)
	}
}

func TestLaunchReadyFollowupsLaunchesEligibleOnly(t *testing.T) {
	// No t.Parallel: stubSpawn mutates the package-level
	// ready.SpawnAgentWindow var.
	beadsDir := testutil.TestDB(t)
	captured := stubSpawn(t)

	// Tree: finished blocks {readyFollow, notReadyFollow, stillBlocked}.
	// stillBlocked is also blocked by another open ticket so it stays
	// blocked even after `finished` closes.
	finished := testutil.CreateBead(t, beadsDir, "finished-root")
	readyFollow := testutil.CreateBead(t, beadsDir, "ready-follow")
	notReadyFollow := testutil.CreateBead(t, beadsDir, "not-ready-follow")
	stillBlocked := testutil.CreateBead(t, beadsDir, "still-blocked")
	otherBlocker := testutil.CreateBead(t, beadsDir, "other-blocker")

	addBlocker(t, beadsDir, readyFollow, finished)
	addBlocker(t, beadsDir, notReadyFollow, finished)
	addBlocker(t, beadsDir, stillBlocked, finished)
	addBlocker(t, beadsDir, stillBlocked, otherBlocker)

	// Only readyFollow and stillBlocked carry the label; notReadyFollow
	// does not.
	if err := ready.Set("", beadsDir, readyFollow); err != nil {
		t.Fatalf("Set readyFollow: %v", err)
	}
	if err := ready.Set("", beadsDir, stillBlocked); err != nil {
		t.Fatalf("Set stillBlocked: %v", err)
	}

	// Close `finished` so LaunchReadyFollowups sees an open dependent
	// graph with `finished` already gone.
	if out, err := testutil.BdCmd(beadsDir, "close", finished).CombinedOutput(); err != nil {
		t.Fatalf("bd close %s: %v\n%s", finished, err, out)
	}

	var buf bytes.Buffer
	if err := ready.LaunchReadyFollowups("", beadsDir, finished, &buf); err != nil {
		t.Fatalf("LaunchReadyFollowups: %v", err)
	}
	got := captured()
	want := []string{readyFollow}
	if !slices.Equal(got, want) {
		t.Errorf("spawns=%v want %v (notReadyFollow lacks the label; stillBlocked still has another blocker)", got, want)
	}
}
