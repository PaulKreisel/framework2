package next_test

import (
	"strings"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/testutil"
)

// registerAgent inserts an agent record on ticket. PID is recorded but not
// probed: [agent.StateOf] returns the recorded Status verbatim so callers can
// pass any int (the field stays meaningful for `agent attach`).
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

// A waiting agent whose terminal was killed (dead PID) must still show up in
// `next agent` — otherwise the user loses the ability to find and resume it.
// StateOf no longer probes the pid; it returns the last hook-recorded Status.
func TestNextAgentShowsWaitingEvenWithDeadWrapper(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-deadpid-root")
	child := testutil.CreateChildBead(t, beadsDir, "nextagent-deadpid-child", root)
	sid := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	registerAgent(t, beadsDir, child, sid, "agent", agent.StatusWaiting, 999999)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, sid) {
		t.Errorf("dead-wrapper waiting agent missing from output:\n%s", stdout)
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

// With --min-recursion-depth 1, the root bead's own waiting agents must not
// appear — only descendants are walked. (Ports the original ai-vrc.22
// "excludes root by default" assertion to the explicit M=1 path.)
func TestNextAgentMinRecursionDepth1ExcludesRoot(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-rootexcluded")
	registerAgent(t, beadsDir, root, "99999999-9999-4999-8999-999999999999", "agent", agent.StatusWaiting, 1)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root, "--min-recursion-depth", "1")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "99999999") {
		t.Errorf("root's own agent should not be listed:\n%s", stdout)
	}
}

// The post-ai-vrc.26 default (--min-recursion-depth 0) surfaces the root's
// own waiting agents alongside descendants.
func TestNextAgentDefaultIncludesRoot(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-rootincluded")
	registerAgent(t, beadsDir, root, "99999999-9999-4999-8999-999999999999", "agent", agent.StatusWaiting, 1)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, root) {
		t.Errorf("root's own waiting agent should be listed by default:\n%s", stdout)
	}
}

// --min-recursion-depth 0 --recursion-depth 0 limits the walk to the root
// bead alone — descendants must not appear even if they have waiting agents.
func TestNextAgentMin0Max0RootOnly(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	rootSession := "11111111-1111-4111-8111-111111111111"
	childSession := "22222222-2222-4222-8222-222222222222"

	root := testutil.CreateBead(t, beadsDir, "nextagent-min0max0-root")
	child := testutil.CreateChildBead(t, beadsDir, "nextagent-min0max0-child", root)
	registerAgent(t, beadsDir, root, rootSession, "agent", agent.StatusWaiting, 1)
	registerAgent(t, beadsDir, child, childSession, "agent", agent.StatusWaiting, 1)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root, "--min-recursion-depth", "0", "--recursion-depth", "0")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, rootSession) {
		t.Errorf("root's own waiting agent missing:\n%s", stdout)
	}
	if strings.Contains(stdout, childSession) {
		t.Errorf("child should not appear with N=M=0:\n%s", stdout)
	}
}

// --recursion-depth 0 with the default min (0) is the same as M=0 N=0:
// just the root's waiting agents.
func TestNextAgentRecursionDepth0RootOnly(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	rootSession := "11111111-1111-4111-8111-111111111111"
	childSession := "22222222-2222-4222-8222-222222222222"

	root := testutil.CreateBead(t, beadsDir, "nextagent-max0-root")
	child := testutil.CreateChildBead(t, beadsDir, "nextagent-max0-child", root)
	registerAgent(t, beadsDir, root, rootSession, "agent", agent.StatusWaiting, 1)
	registerAgent(t, beadsDir, child, childSession, "agent", agent.StatusWaiting, 1)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root, "--recursion-depth", "0")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, rootSession) {
		t.Errorf("root's own waiting agent missing:\n%s", stdout)
	}
	if strings.Contains(stdout, childSession) {
		t.Errorf("child should not appear with N=0:\n%s", stdout)
	}
}

// --min-recursion-depth 2 skips both root and direct children.
func TestNextAgentMinRecursionDepth2SkipsRootAndChildren(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	rootSession := "11111111-1111-4111-8111-111111111111"
	childSession := "22222222-2222-4222-8222-222222222222"
	grandSession := "33333333-3333-4333-8333-333333333333"

	root := testutil.CreateBead(t, beadsDir, "nextagent-min2-root")
	child := testutil.CreateChildBead(t, beadsDir, "nextagent-min2-child", root)
	grand := testutil.CreateChildBead(t, beadsDir, "nextagent-min2-grand", child)
	registerAgent(t, beadsDir, root, rootSession, "agent", agent.StatusWaiting, 1)
	registerAgent(t, beadsDir, child, childSession, "agent", agent.StatusWaiting, 1)
	registerAgent(t, beadsDir, grand, grandSession, "agent", agent.StatusWaiting, 1)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root, "--min-recursion-depth", "2")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, rootSession) {
		t.Errorf("root should not appear with M=2:\n%s", stdout)
	}
	if strings.Contains(stdout, childSession) {
		t.Errorf("direct child should not appear with M=2:\n%s", stdout)
	}
	if !strings.Contains(stdout, grandSession) {
		t.Errorf("grandchild missing with M=2:\n%s", stdout)
	}
}

// N < M is rejected up front with a non-zero exit and no table output.
func TestNextAgentMaxLessThanMinRejected(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-nlm-root")
	registerAgent(t, beadsDir, root, "11111111-1111-4111-8111-111111111111", "agent", agent.StatusWaiting, 1)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root, "--min-recursion-depth", "2", "--recursion-depth", "1")
	if code == 0 {
		t.Fatalf("expected non-zero exit when N < M, got 0; stdout=%q stderr=%q", stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("expected no stdout on validation error, got %q", stdout)
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

// --all surfaces running records as well as waiting ones. A dead-wrapper
// record stays at its last hook-recorded Status (no liveness probe), so a
// terminal-killed "running" agent must still appear under --all.
func TestNextAgentAllShowsDeadWrapperRunning(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-all-deadpid-root")
	child := testutil.CreateChildBead(t, beadsDir, "nextagent-all-deadpid-child", root)
	sid := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	registerAgent(t, beadsDir, child, sid, "agent", agent.StatusRunning, 999999)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root, "--all")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, sid) {
		t.Errorf("--all output missing dead-wrapper running agent:\n%s", stdout)
	}
}

// In single-ticket mode (depth 0..0 with a concrete id) the unified renderer
// drops the TICKET column — `agent list` shares the same path and expects
// the legacy column shape.
func TestNextAgentDepth0HidesTicketColumn(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-depth0-hide-ticket")
	registerAgent(t, beadsDir, root, "11111111-1111-4111-8111-111111111111", "agent", agent.StatusWaiting, 1)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root, "--recursion-depth", "0")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	lines := splitNonEmpty(stdout)
	if len(lines) == 0 {
		t.Fatalf("expected header + row, got empty stdout")
	}
	if strings.HasPrefix(lines[0], "TICKET") {
		t.Errorf("depth=0 header still carries TICKET column:\n%s", stdout)
	}
	if !strings.HasPrefix(lines[0], "SESSION ID") {
		t.Errorf("depth=0 header should start with SESSION ID, got %q", lines[0])
	}
}

// Subtree mode (the default) keeps the TICKET column on the header even if
// only one row matches.
func TestNextAgentSubtreeKeepsTicketColumn(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	root := testutil.CreateBead(t, beadsDir, "nextagent-subtree-keeps-ticket")
	child := testutil.CreateChildBead(t, beadsDir, "nextagent-subtree-child", root)
	registerAgent(t, beadsDir, child, "11111111-1111-4111-8111-111111111111", "agent", agent.StatusWaiting, 1)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "next", "agent", root)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	lines := splitNonEmpty(stdout)
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "TICKET") {
		t.Errorf("subtree header should start with TICKET, got:\n%s", stdout)
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
