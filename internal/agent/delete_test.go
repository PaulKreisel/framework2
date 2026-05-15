package agent_test

import (
	"strings"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/testutil"
)

func TestDeleteAgentsRemovesNamedRecords(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "delete-removes", "x", "")

	sids := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333",
	}
	for i, sid := range sids {
		// PID 999999 → already exited, no --force needed.
		if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
			SessionID: sid, PID: 999999, Mode: "agent",
			StartedAt: "2026-05-15T14:0" + string(rune('0'+i)) + ":00Z",
			Env:       "/env",
		}); err != nil {
			t.Fatalf("Register %s: %v", sid, err)
		}
	}

	if err := agent.DeleteAgents("", beadsDir, ticket, []string{sids[0], sids[2]}, false); err != nil {
		t.Fatalf("DeleteAgents: %v", err)
	}
	got, err := agent.LoadAgents("", beadsDir, ticket)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != sids[1] {
		t.Fatalf("after delete entries = %+v, want only %s", got, sids[1])
	}
}

func TestDeleteAgentsRejectsUnknownSession(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "delete-unknown", "x", "")

	known := "11111111-1111-4111-8111-111111111111"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: known, PID: 999999, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	bogus := "99999999-9999-4999-8999-999999999999"
	err := agent.DeleteAgents("", beadsDir, ticket, []string{bogus}, false)
	if err == nil {
		t.Fatal("DeleteAgents with unknown id succeeded; want error")
	}
	if !strings.Contains(err.Error(), "not found") || !strings.Contains(err.Error(), known) {
		t.Errorf("error = %q, want 'not found' + known id %q", err.Error(), known)
	}

	// Original list must be untouched.
	got, err := agent.LoadAgents("", beadsDir, ticket)
	if err != nil {
		t.Fatalf("LoadAgents: %v", err)
	}
	if len(got) != 1 || got[0].SessionID != known {
		t.Errorf("list mutated after failed delete: %+v", got)
	}
}

func TestDeleteAgentsAtomicAcrossUnknownInBatch(t *testing.T) {
	// A typo in one id of a multi-arg call must not delete the others.
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "delete-atomic", "x", "")

	good := "11111111-1111-4111-8111-111111111111"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: good, PID: 999999, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	bogus := "99999999-9999-4999-8999-999999999999"
	err := agent.DeleteAgents("", beadsDir, ticket, []string{good, bogus}, false)
	if err == nil {
		t.Fatal("partial-typo DeleteAgents succeeded; want error")
	}
	got, _ := agent.LoadAgents("", beadsDir, ticket)
	if len(got) != 1 || got[0].SessionID != good {
		t.Errorf("good id wrongly deleted alongside bad id: %+v", got)
	}
}

func TestDeleteAgentsRefusesLiveWithoutForce(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "delete-live", "x", "")

	live := "11111111-1111-4111-8111-111111111111"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		// PID 1 (init) is always alive — stateOf reports "running".
		SessionID: live, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	err := agent.DeleteAgents("", beadsDir, ticket, []string{live}, false)
	if err == nil {
		t.Fatal("DeleteAgents on live record without --force succeeded; want refusal")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %q, want to mention --force", err.Error())
	}

	got, _ := agent.LoadAgents("", beadsDir, ticket)
	if len(got) != 1 {
		t.Errorf("live record removed despite refusal: %+v", got)
	}
}

func TestDeleteAgentsForceDeletesLive(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "delete-force", "x", "")

	live := "11111111-1111-4111-8111-111111111111"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: live, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := agent.DeleteAgents("", beadsDir, ticket, []string{live}, true); err != nil {
		t.Fatalf("DeleteAgents force: %v", err)
	}
	got, _ := agent.LoadAgents("", beadsDir, ticket)
	if len(got) != 0 {
		t.Errorf("live record survived --force delete: %+v", got)
	}
}

func TestDeleteAgentsCLIRequiresSessionID(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "delete-cli-no-sid", "x", "")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "delete", ticket)
	if code == 0 {
		t.Fatalf("agent delete <id> (no session) succeeded; want error")
	}
	if !strings.Contains(stderr, "usage:") {
		t.Errorf("stderr = %q, want usage line", stderr)
	}
}

func TestDeleteAgentsCLIMultipleIDs(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "delete-cli-multi", "x", "")

	sids := []string{
		"11111111-1111-4111-8111-111111111111",
		"22222222-2222-4222-8222-222222222222",
	}
	for _, sid := range sids {
		if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
			SessionID: sid, PID: 999999, Mode: "agent",
			StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		}); err != nil {
			t.Fatalf("Register %s: %v", sid, err)
		}
	}

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "delete", ticket, sids[0], sids[1])
	if code != 0 {
		t.Fatalf("agent delete: exit=%d stderr=%q", code, stderr)
	}
	got, _ := agent.LoadAgents("", beadsDir, ticket)
	if len(got) != 0 {
		t.Errorf("expected empty list after deleting both; got %+v", got)
	}
}

func TestDeleteAgentsRejectsDuplicateIDs(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "delete-dup", "x", "")

	sid := "11111111-1111-4111-8111-111111111111"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 999999, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	err := agent.DeleteAgents("", beadsDir, ticket, []string{sid, sid}, false)
	if err == nil {
		t.Fatal("duplicate ids accepted; want error")
	}
	if !strings.Contains(err.Error(), "more than once") {
		t.Errorf("error = %q, want to mention 'more than once'", err.Error())
	}
}
