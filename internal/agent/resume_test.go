package agent_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"framework2/internal/agent"
	"framework2/internal/testutil"
)

func TestLatestSessionEmptyIsNil(t *testing.T) {
	t.Parallel()
	if got := agent.LatestSession(nil); got != nil {
		t.Errorf("LatestSession(nil) = %+v, want nil", got)
	}
	if got := agent.LatestSession([]agent.AgentEntry{}); got != nil {
		t.Errorf("LatestSession([]) = %+v, want nil", got)
	}
}

func TestLatestSessionPicksMostRecentActivity(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		entries []agent.AgentEntry
		want    string
	}{
		{
			name: "newest StartedAt wins over insertion order",
			entries: []agent.AgentEntry{
				{SessionID: "old", StartedAt: "2026-08-10T09:00:00Z"},
				{SessionID: "new", StartedAt: "2026-08-11T09:00:00Z"},
			},
			want: "new",
		},
		{
			name: "an older record with newer activity wins",
			entries: []agent.AgentEntry{
				{SessionID: "active", StartedAt: "2026-08-10T09:00:00Z", StatusUpdatedAt: "2026-08-12T18:00:00Z"},
				{SessionID: "idle", StartedAt: "2026-08-11T09:00:00Z", StatusUpdatedAt: "2026-08-11T09:30:00Z"},
			},
			want: "active",
		},
		{
			name: "ties go to the later record",
			entries: []agent.AgentEntry{
				{SessionID: "first", StartedAt: "2026-08-10T09:00:00Z"},
				{SessionID: "second", StartedAt: "2026-08-10T09:00:00Z"},
			},
			want: "second",
		},
		{
			name: "records without timestamps still resolve",
			entries: []agent.AgentEntry{
				{SessionID: "a"},
				{SessionID: "b"},
			},
			want: "b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := agent.LatestSession(tc.entries)
			if got == nil {
				t.Fatalf("LatestSession = nil, want %q", tc.want)
			}
			if got.SessionID != tc.want {
				t.Errorf("LatestSession = %q, want %q", got.SessionID, tc.want)
			}
		})
	}
}

func TestParseModeRoundTrip(t *testing.T) {
	t.Parallel()
	if agent.ParseMode("spec") != agent.ModeSpec {
		t.Error(`ParseMode("spec") should be ModeSpec`)
	}
	if agent.ParseMode("agent") != agent.ModeAgent {
		t.Error(`ParseMode("agent") should be ModeAgent`)
	}
	// Legacy records predate the mode field.
	if agent.ParseMode("") != agent.ModeAgent {
		t.Error(`ParseMode("") should default to ModeAgent`)
	}
}

func TestContinueLatestWithoutRecordsErrors(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "resume-no-records", "x", "")

	err := agent.ContinueLatest("", beadsDir, ticket)
	if err == nil {
		t.Fatal("ContinueLatest on a bead with no records succeeded; want error")
	}
	if !strings.Contains(err.Error(), "no recorded agent sessions") {
		t.Errorf("error = %q, want to mention 'no recorded agent sessions'", err.Error())
	}
}

func TestResumeSessionUnknownSessionReportsKnownIDs(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "resume-unknown-sid", "x", "")

	known := "11111111-1111-4111-8111-111111111111"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: known, PID: 1, Mode: "agent",
		StartedAt: "2026-08-11T09:00:00Z", Env: t.TempDir(),
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	err := agent.ResumeSession("", beadsDir, ticket, "22222222-2222-4222-8222-222222222222")
	if err == nil {
		t.Fatal("ResumeSession with unknown session id succeeded; want error")
	}
	if !strings.Contains(err.Error(), known) {
		t.Errorf("error = %q, want to list known session id %q", err.Error(), known)
	}
}

// stubPicker redirects ExecPicker for the lifetime of t and records the table
// it was fed. Global state — tests using it must not run in parallel.
func stubPicker(t *testing.T, reply func(table string) (string, error)) *[]string {
	t.Helper()
	prev := agent.ExecPicker
	var tables []string
	agent.ExecPicker = func(table string, args []string) (string, error) {
		tables = append(tables, table)
		return reply(table)
	}
	t.Cleanup(func() { agent.ExecPicker = prev })
	return &tables
}

func TestPickAndResumeWithoutRowsSkipsPicker(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "resume-pick-empty", "x", "")

	tables := stubPicker(t, func(string) (string, error) {
		t.Error("picker invoked for a ticket with no sessions")
		return "", nil
	})
	// Header-only table: what `agent list` prints for a ticket with no records.
	err := agent.PickAndResume("", beadsDir, ticket, "SESSION ID  MODE  PID\n")
	if err == nil {
		t.Fatal("PickAndResume on a header-only table succeeded; want error")
	}
	if !strings.Contains(err.Error(), "no recorded agent sessions") {
		t.Errorf("error = %q, want to mention 'no recorded agent sessions'", err.Error())
	}
	if len(*tables) != 0 {
		t.Errorf("picker invoked %d times, want 0", len(*tables))
	}
}

func TestPickAndResumeCancelledIsReported(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "resume-pick-cancel", "x", "")

	stubPicker(t, func(string) (string, error) { return "", agent.ErrPickCancelled })

	err := agent.PickAndResume("", beadsDir, ticket, "SESSION ID  MODE\nsid-1  agent\n")
	if !errors.Is(err, agent.ErrPickCancelled) {
		t.Errorf("error = %v, want ErrPickCancelled", err)
	}
}

func TestPickAndResumeParsesSessionIDFromRow(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "resume-pick-parse", "x", "")

	sid := "33333333-3333-4333-8333-333333333333"
	// The picked row carries every `agent list` column; only the first field is
	// the session id.
	row := sid + "  agent  1234  2026-08-12 10:00  waiting  12.3k  /tmp/env  waiting on you"
	stubPicker(t, func(string) (string, error) { return row, nil })

	err := agent.PickAndResume("", beadsDir, ticket, "SESSION ID  MODE\n"+row+"\n")
	if err == nil {
		t.Fatal("PickAndResume with an unregistered session succeeded; want error")
	}
	// The session id was extracted from the row and looked up — an
	// unregistered id lands in the strict-lookup error path.
	if !strings.Contains(err.Error(), sid) {
		t.Errorf("error = %q, want to mention the picked session id %q", err.Error(), sid)
	}
}

// TestPickAndResumeResumesRecordedMode drives the whole resume path with a
// fake claude: a spec-mode record must resume through the spec surface.
// RunWithOptions refuses a mode it wasn't recorded with, so reaching the fake
// claude at all is the assertion that the recorded mode was honoured.
func TestPickAndResumeResumesRecordedMode(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "resume-pick-spec-mode", "x", "")
	envPath := mustEnv(t, beadsDir, ticket)

	sid := "44444444-4444-4444-8444-444444444444"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "spec",
		StartedAt: "2026-08-11T09:00:00Z", Env: envPath,
		Status: agent.StatusWaiting, StatusUpdatedAt: "2026-08-11T09:00:00Z",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	argvLog := filepath.Join(t.TempDir(), "argv")
	script := fmt.Sprintf(`printf "%%s\n" "$@" > %q`, argvLog)
	prevCmd, prevArgs := agent.AgentCommand, agent.AgentDefaultArgs
	t.Cleanup(func() { agent.AgentCommand, agent.AgentDefaultArgs = prevCmd, prevArgs })
	agent.AgentCommand = "/bin/sh"
	agent.AgentDefaultArgs = []string{"-c", script, "fake-claude"}

	row := sid + "  spec  1  2026-08-11 09:00  waiting"
	stubPicker(t, func(string) (string, error) { return row, nil })

	done := make(chan error, 1)
	go func() {
		done <- agent.PickAndResume("", beadsDir, ticket, "SESSION ID  MODE\n"+row+"\n")
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("PickAndResume: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("PickAndResume hung")
	}

	body, err := os.ReadFile(argvLog)
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	argv := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
	if !containsPair(argv, "--resume", sid) {
		t.Errorf("argv = %v, want `--resume %s`", argv, sid)
	}
}

// containsPair reports whether argv carries flag immediately followed by value.
func containsPair(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}
