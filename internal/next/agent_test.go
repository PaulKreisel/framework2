package next_test

import (
	"strings"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/testutil"
)

// registerAgent inserts an agent record on ticket. PID 1 is "init" — always
// alive on macOS — so the [agent.StateOf] liveness check reports the
// recorded Status. PID 999999 stands in for a dead wrapper and collapses to
// "exited" regardless of Status.
func registerAgent(t *testing.T, beadsDir, ticket, sessionID, mode, status string, pid int) {
	t.Helper()
	err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID:       sessionID,
		PID:             pid,
		Mode:            mode,
		StartedAt:       "2026-05-15T14:56:00Z",
		Env:             "/env/" + ticket,
		Status:          status,
		StatusUpdatedAt: "2026-05-15T14:56:00Z",
	})
	if err != nil {
		t.Fatalf("RegisterAgent on %s: %v", ticket, err)
	}
}

func TestNextAgentEmptyWhenNoWaitingDescendants(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-empty-root")
	child := testutil.CreateChildBead(t, beadsDir, "nextagent-empty-child", root)
	// Only a running agent — must not appear.
	registerAgent(t, beadsDir, child, "11111111-1111-4111-8111-111111111111", "agent", agent.StatusRunning, 1)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("want empty stdout, got %q", stdout)
	}
}

func TestNextAgentReturnsWaitingMixedDepths(t *testing.T) {
	// Tree:  root
	//          ├── childA (mode=spec, waiting)
	//          │     └── grandA1 (mode=agent, waiting)
	//          └── childB (mode=agent, running)   <- skipped
	//                └── grandB1 (mode=agent, waiting)
	//
	// Expected rows (agent before spec, ticket id sort within mode):
	//   grandA1 (agent, waiting)
	//   grandB1 (agent, waiting)
	//   childA  (spec,  waiting)
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-mixed-root")
	childA := testutil.CreateChildBead(t, beadsDir, "nextagent-mixed-child-a", root)
	childB := testutil.CreateChildBead(t, beadsDir, "nextagent-mixed-child-b", root)
	grandA1 := testutil.CreateChildBead(t, beadsDir, "nextagent-mixed-grand-a1", childA)
	grandB1 := testutil.CreateChildBead(t, beadsDir, "nextagent-mixed-grand-b1", childB)

	registerAgent(t, beadsDir, childA, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "spec", agent.StatusWaiting, 1)
	registerAgent(t, beadsDir, childB, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "agent", agent.StatusRunning, 1)
	registerAgent(t, beadsDir, grandA1, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", "agent", agent.StatusWaiting, 1)
	registerAgent(t, beadsDir, grandB1, "dddddddd-dddd-4ddd-8ddd-dddddddddddd", "agent", agent.StatusWaiting, 1)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}

	lines := splitNonEmpty(stdout)
	if len(lines) != 4 {
		t.Fatalf("want header + 3 rows, got %d lines:\n%s", len(lines), stdout)
	}
	if !strings.HasPrefix(lines[0], "TICKET") {
		t.Errorf("first line should be header, got %q", lines[0])
	}
	// running agent on childB must be absent.
	if strings.Contains(stdout, childB+" ") {
		t.Errorf("running-state agent on %s leaked into output:\n%s", childB, stdout)
	}
	// Two agent rows then one spec row.
	if !strings.Contains(lines[1], "agent") || !strings.Contains(lines[2], "agent") {
		t.Errorf("rows 1+2 should be mode=agent, got:\n%s", stdout)
	}
	if !strings.Contains(lines[3], "spec") {
		t.Errorf("row 3 should be mode=spec, got %q", lines[3])
	}
	// Within the agent rows, ticket-id sort: grandA1 < grandB1 only holds if
	// bd's id generator preserved that order. Assert order by comparing to
	// the actual ids the test created rather than hard-coding a string sort.
	first, second := grandA1, grandB1
	if first > second {
		first, second = second, first
	}
	if !strings.HasPrefix(lines[1], first) {
		t.Errorf("row 1 should start with %q (ticket-id sorted first), got %q", first, lines[1])
	}
	if !strings.HasPrefix(lines[2], second) {
		t.Errorf("row 2 should start with %q, got %q", second, lines[2])
	}
}

func TestNextAgentSkipsDeadWrapperEvenIfStatusWaiting(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-deadpid-root")
	child := testutil.CreateChildBead(t, beadsDir, "nextagent-deadpid-child", root)
	// PID 999999 is dead → StateOf collapses to "exited" — must not appear.
	registerAgent(t, beadsDir, child, "ffffffff-ffff-4fff-8fff-ffffffffffff", "agent", agent.StatusWaiting, 999999)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("dead-pid waiting agent leaked into output: %q", stdout)
	}
}

func TestNextAgentRecursionDepth1ExcludesGrandchildren(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-depth1-root")
	child := testutil.CreateChildBead(t, beadsDir, "nextagent-depth1-child", root)
	grand := testutil.CreateChildBead(t, beadsDir, "nextagent-depth1-grand", child)

	registerAgent(t, beadsDir, child, "11111111-1111-4111-8111-111111111111", "agent", agent.StatusWaiting, 1)
	registerAgent(t, beadsDir, grand, "22222222-2222-4222-8222-222222222222", "agent", agent.StatusWaiting, 1)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root, "--recursion-depth", "1")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, child) {
		t.Errorf("depth=1 output missing direct child %s:\n%s", child, stdout)
	}
	if strings.Contains(stdout, grand) {
		t.Errorf("depth=1 output unexpectedly contains grandchild %s:\n%s", grand, stdout)
	}
}

func TestNextAgentRecursionDepth2IncludesGrandchildrenOnly(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-depth2-root")
	child := testutil.CreateChildBead(t, beadsDir, "nextagent-depth2-child", root)
	grand := testutil.CreateChildBead(t, beadsDir, "nextagent-depth2-grand", child)
	greatGrand := testutil.CreateChildBead(t, beadsDir, "nextagent-depth2-greatgrand", grand)

	registerAgent(t, beadsDir, child, "11111111-1111-4111-8111-111111111111", "agent", agent.StatusWaiting, 1)
	registerAgent(t, beadsDir, grand, "22222222-2222-4222-8222-222222222222", "agent", agent.StatusWaiting, 1)
	registerAgent(t, beadsDir, greatGrand, "33333333-3333-4333-8333-333333333333", "agent", agent.StatusWaiting, 1)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root, "--recursion-depth", "2")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, child) {
		t.Errorf("depth=2 output missing child %s:\n%s", child, stdout)
	}
	if !strings.Contains(stdout, grand) {
		t.Errorf("depth=2 output missing grandchild %s:\n%s", grand, stdout)
	}
	if strings.Contains(stdout, greatGrand) {
		t.Errorf("depth=2 output unexpectedly contains great-grandchild %s:\n%s", greatGrand, stdout)
	}
}

func TestNextAgentExcludesRoot(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-rootexcluded")
	registerAgent(t, beadsDir, root, "99999999-9999-4999-8999-999999999999", "agent", agent.StatusWaiting, 1)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "99999999") {
		t.Errorf("root's own agent should not be listed:\n%s", stdout)
	}
}

// Pins the post-ai-vrc.28 "skip closed" semantics: a `waiting` agent
// recorded on a closed bead must not appear in `ticket next agent` output.
// (The agent is still visible via `ticket agent list <closed-id>` for
// recovery — see the comment in agent.go.)
func TestNextAgentSkipsClosedDescendants(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-closed-root")
	child := testutil.CreateChildBead(t, beadsDir, "nextagent-closed-child", root)
	registerAgent(t, beadsDir, child, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "agent", agent.StatusWaiting, 1)

	if err := testutil.BdCmd(beadsDir, "close", child).Run(); err != nil {
		t.Fatalf("bd close %s: %v", child, err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("waiting agent on closed child leaked into output: %q", stdout)
	}
}

// --all includes both running and waiting agents in the subtree.
func TestNextAgentAllIncludesRunning(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-all-root")
	childRunning := testutil.CreateChildBead(t, beadsDir, "nextagent-all-child-running", root)
	childWaiting := testutil.CreateChildBead(t, beadsDir, "nextagent-all-child-waiting", root)
	registerAgent(t, beadsDir, childRunning, "11111111-1111-4111-8111-111111111111", "agent", agent.StatusRunning, 1)
	registerAgent(t, beadsDir, childWaiting, "22222222-2222-4222-8222-222222222222", "agent", agent.StatusWaiting, 1)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root, "--all")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, childRunning) {
		t.Errorf("--all output missing running agent on %s:\n%s", childRunning, stdout)
	}
	if !strings.Contains(stdout, childWaiting) {
		t.Errorf("--all output missing waiting agent on %s:\n%s", childWaiting, stdout)
	}
}

// --all must not surface exited (dead-PID) records — they're not "active".
func TestNextAgentAllStillHidesDeadWrapper(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-all-deadpid-root")
	child := testutil.CreateChildBead(t, beadsDir, "nextagent-all-deadpid-child", root)
	// PID 999999 is dead → StateOf collapses to "exited" — must not appear.
	registerAgent(t, beadsDir, child, "ffffffff-ffff-4fff-8fff-ffffffffffff", "agent", agent.StatusRunning, 999999)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root, "--all")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("--all output should hide exited agent, got: %q", stdout)
	}
}

func splitNonEmpty(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
