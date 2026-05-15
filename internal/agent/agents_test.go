package agent_test

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/testutil"
)

func TestNewSessionIDIsRFC4122UUIDv4(t *testing.T) {
	t.Parallel()
	id, err := agent.NewSessionID()
	if err != nil {
		t.Fatalf("NewSessionID: %v", err)
	}
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !re.MatchString(id) {
		t.Errorf("NewSessionID = %q; not a v4 UUID", id)
	}
	// Two calls must not collide.
	id2, _ := agent.NewSessionID()
	if id == id2 {
		t.Errorf("NewSessionID returned same value twice: %q", id)
	}
}

func TestRegisterAgentAppendsAndLoadsBack(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agents-register", "x", "")

	if got, err := agent.LoadAgents("", beadsDir, ticket); err != nil || len(got) != 0 {
		t.Fatalf("initial LoadAgents: got=%v err=%v, want empty", got, err)
	}

	e1 := agent.AgentEntry{
		SessionID: "00000000-0000-4000-8000-000000000001",
		PID:       os.Getpid(),
		Mode:      "agent",
		StartedAt: "2026-05-15T14:56:00Z",
		Env:       "/path/to/env-1",
	}
	if err := agent.RegisterAgent("", beadsDir, ticket, e1); err != nil {
		t.Fatalf("Register first: %v", err)
	}
	e2 := agent.AgentEntry{
		SessionID: "00000000-0000-4000-8000-000000000002",
		PID:       1,
		Mode:      "spec",
		StartedAt: "2026-05-15T15:00:00Z",
		Env:       "/path/to/env-2",
	}
	if err := agent.RegisterAgent("", beadsDir, ticket, e2); err != nil {
		t.Fatalf("Register second: %v", err)
	}

	got, err := agent.LoadAgents("", beadsDir, ticket)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("LoadAgents returned %d entries, want 2: %+v", len(got), got)
	}
	if got[0] != e1 || got[1] != e2 {
		t.Errorf("entries = %+v, want [%+v %+v]", got, e1, e2)
	}
}

func TestRegisterAgentPreservesDirsKey(t *testing.T) {
	// The agents writer must do a per-key merge: bd update --metadata
	// preserves other keys, but if a future implementation accidentally
	// rewrites the whole blob, dirs would vanish. Pin that here.
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agents-preserves-dirs", "x", "")

	tmp := t.TempDir()
	if _, _, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, tmp); code != 0 {
		t.Fatalf("dir link: exit=%d", code)
	}
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: "00000000-0000-4000-8000-000000000001",
		PID:       1, Mode: "agent", StartedAt: "2026-05-15T14:56:00Z", Env: tmp,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// dirs key must survive.
	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "list", ticket)
	if code != 0 {
		t.Fatalf("dir list: exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, tmp) {
		t.Errorf("dir list missing path %q after RegisterAgent:\n%s", tmp, stdout)
	}
}

func TestListAgentsJSONNewestFirst(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agents-list-json", "x", "")

	for i, mode := range []string{"agent", "spec", "agent"} {
		if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
			SessionID: fmt.Sprintf("00000000-0000-4000-8000-00000000000%d", i+1),
			PID:       100 + i,
			Mode:      mode,
			StartedAt: fmt.Sprintf("2026-05-15T14:0%d:00Z", i),
			Env:       fmt.Sprintf("/env-%d", i),
		}); err != nil {
			t.Fatalf("Register %d: %v", i, err)
		}
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket, "--json")
	if code != 0 {
		t.Fatalf("agent list --json: exit=%d stderr=%q", code, stderr)
	}
	var entries []agent.AgentEntry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("parse json: %v\n%s", err, stdout)
	}
	if len(entries) != 3 {
		t.Fatalf("entries=%d, want 3: %+v", len(entries), entries)
	}
	// Newest first: index 2 was registered last, so it should come first.
	if entries[0].SessionID != "00000000-0000-4000-8000-000000000003" {
		t.Errorf("first entry SessionID = %q, want most-recent record", entries[0].SessionID)
	}
	if entries[2].SessionID != "00000000-0000-4000-8000-000000000001" {
		t.Errorf("last entry SessionID = %q, want oldest record", entries[2].SessionID)
	}
}

func TestListAgentsTableLayoutAndStatus(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agents-list-table", "x", "")

	// PID 1 (init) is always alive on a unix system → running.
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: "11111111-1111-4111-8111-111111111111",
		PID:       1,
		Mode:      "agent",
		StartedAt: "2026-05-15T14:56:00Z",
		Env:       "/env-running",
		Status:    agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register running: %v", err)
	}
	// A pid that's almost certainly not in use → exited.
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: "22222222-2222-4222-8222-222222222222",
		PID:       999999,
		Mode:      "spec",
		StartedAt: "2026-05-15T13:15:00Z",
		Env:       "/env-exited",
		Status:    agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register exited: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket)
	if code != 0 {
		t.Fatalf("agent list: exit=%d stderr=%q", code, stderr)
	}

	if !strings.Contains(stdout, "SESSION ID") || !strings.Contains(stdout, "STATE") {
		t.Errorf("table header missing:\n%s", stdout)
	}
	if !strings.Contains(stdout, "running") {
		t.Errorf("alive pid not reported running:\n%s", stdout)
	}
	if !strings.Contains(stdout, "exited") {
		t.Errorf("dead pid not reported exited:\n%s", stdout)
	}
	// Newest first: the running record (registered first) should appear AFTER
	// the exited one in textual order.
	idxExited := strings.Index(stdout, "22222222")
	idxRunning := strings.Index(stdout, "11111111")
	if idxExited == -1 || idxRunning == -1 {
		t.Fatalf("table missing session ids:\n%s", stdout)
	}
	if idxExited > idxRunning {
		t.Errorf("ordering not newest-first; running came before exited:\n%s", stdout)
	}
}

func TestListAgentsEmpty(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agents-list-empty", "x", "")

	stdout, _, code := testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket, "--json")
	if code != 0 {
		t.Fatalf("agent list --json: exit=%d", code)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Errorf("empty json output = %q, want %q", strings.TrimSpace(stdout), "[]")
	}

	stdout, _, code = testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket)
	if code != 0 {
		t.Fatalf("agent list: exit=%d", code)
	}
	// Header always present; no data rows.
	if !strings.Contains(stdout, "SESSION ID") {
		t.Errorf("empty table missing header:\n%s", stdout)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 1 {
		t.Errorf("empty table expected 1 line, got %d:\n%s", len(lines), stdout)
	}
}

func TestModeString(t *testing.T) {
	t.Parallel()
	if got := agent.ModeAgent.String(); got != "agent" {
		t.Errorf("ModeAgent.String() = %q, want %q", got, "agent")
	}
	if got := agent.ModeSpec.String(); got != "spec" {
		t.Errorf("ModeSpec.String() = %q, want %q", got, "spec")
	}
}
