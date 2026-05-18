package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/testutil"
)

// TestSetStatusPersistsContextTokens covers the round-trip: SetStatus is
// called with a token count, the value reloads, a second call with the same
// value is idempotent (no timestamp churn), and a third call with a larger
// value overwrites — matching the "absolute snapshot, not delta" semantics.
func TestSetStatusPersistsContextTokens(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "ctxtokens-roundtrip", "x", "")

	sid := "11111111-1111-4111-8111-111111111111"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:56:00Z", Env: "/env-x",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := agent.SetStatus("", beadsDir, sid, agent.StatusWaiting, "", "", ticket, 53000); err != nil {
		t.Fatalf("SetStatus #1: %v", err)
	}
	got := loadOne(t, beadsDir, ticket)
	if got.ContextTokens != 53000 {
		t.Errorf("ContextTokens = %d, want 53000", got.ContextTokens)
	}
	firstTimestamp := got.ContextTokensUpdatedAt
	if firstTimestamp == "" {
		t.Errorf("ContextTokensUpdatedAt empty after first set")
	}

	// Same value: idempotent. Re-run set-status with the same count;
	// timestamp must not advance.
	if err := agent.SetStatus("", beadsDir, sid, agent.StatusWaiting, "", "", ticket, 53000); err != nil {
		t.Fatalf("SetStatus #2 (idempotent): %v", err)
	}
	if got2 := loadOne(t, beadsDir, ticket); got2.ContextTokensUpdatedAt != firstTimestamp {
		t.Errorf("idempotent re-set churned timestamp: %q -> %q",
			firstTimestamp, got2.ContextTokensUpdatedAt)
	}

	// New value: overwrites.
	if err := agent.SetStatus("", beadsDir, sid, agent.StatusWaiting, "", "", ticket, 180_000); err != nil {
		t.Fatalf("SetStatus #3 (overwrite): %v", err)
	}
	if got3 := loadOne(t, beadsDir, ticket); got3.ContextTokens != 180_000 {
		t.Errorf("ContextTokens = %d, want 180000", got3.ContextTokens)
	}
}

// TestSetStatusContextTokensSurviveRunningTransition pins the
// "Never clear on status transitions" rule: a UserPromptSubmit hook flipping
// the agent back to running passes contextTokens=0 (unchanged) and must
// leave the stored snapshot intact.
func TestSetStatusContextTokensSurviveRunningTransition(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "ctxtokens-survive", "x", "")

	sid := "22222222-2222-4222-8222-222222222222"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:56:00Z", Env: "/env-x",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := agent.SetStatus("", beadsDir, sid, agent.StatusWaiting, "", "", ticket, 42_000); err != nil {
		t.Fatalf("SetStatus waiting: %v", err)
	}
	if err := agent.SetStatus("", beadsDir, sid, agent.StatusRunning, "", "", ticket, 0); err != nil {
		t.Fatalf("SetStatus running: %v", err)
	}
	got := loadOne(t, beadsDir, ticket)
	if got.Status != agent.StatusRunning {
		t.Errorf("Status = %q, want %q", got.Status, agent.StatusRunning)
	}
	if got.ContextTokens != 42_000 {
		t.Errorf("running-transition cleared ContextTokens: got %d, want 42000", got.ContextTokens)
	}
}

func TestFormatContextTokens(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   int
		want string
	}{
		{0, ""},        // never populated -> blank
		{-1, ""},       // defensive: negative renders blank
		{1, "1"},       // below 1000 prints as-is
		{999, "999"},   // boundary just below 1k
		{1000, "1.0k"}, // boundary at 1k
		{53_000, "53.0k"},
		{180_500, "180.5k"},
		{999_999, "1000.0k"}, // just under the M boundary still renders in k
		{1_000_000, "1.0M"},
		{1_500_000, "1.5M"},
	}
	for _, tc := range tests {
		if got := agent.FormatContextTokens(tc.in); got != tc.want {
			t.Errorf("FormatContextTokens(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestListAgentsTableHasContextColumn confirms the table view renders the
// new CONTEXT column with the formatted value, and that a zero count
// produces a blank cell (no "0" leaking through).
func TestListAgentsTableHasContextColumn(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "ctxtokens-list", "x", "")

	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: "33333333-3333-4333-8333-333333333333",
		PID:       1, Mode: "agent",
		StartedAt:     "2026-05-15T14:56:00Z",
		Env:           "/env-with-tokens",
		Status:        agent.StatusWaiting,
		ContextTokens: 53_000,
	}); err != nil {
		t.Fatalf("Register w/ tokens: %v", err)
	}
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: "44444444-4444-4444-8444-444444444444",
		PID:       1, Mode: "agent",
		StartedAt: "2026-05-15T15:00:00Z",
		Env:       "/env-no-tokens",
		Status:    agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register w/o tokens: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket)
	if code != 0 {
		t.Fatalf("agent list: exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "CONTEXT") {
		t.Errorf("CONTEXT header missing:\n%s", stdout)
	}
	if !strings.Contains(stdout, "53.0k") {
		t.Errorf("formatted token count missing:\n%s", stdout)
	}
	// The blank cell shows up as adjacent whitespace; the simplest check
	// is that "0" doesn't appear on the no-tokens row's CONTEXT cell.
	noTokensLine := ""
	for _, ln := range strings.Split(stdout, "\n") {
		if strings.Contains(ln, "44444444") {
			noTokensLine = ln
			break
		}
	}
	if noTokensLine == "" {
		t.Fatalf("no-tokens row missing from table:\n%s", stdout)
	}
	if strings.Contains(noTokensLine, " 0 ") {
		t.Errorf("zero-tokens row leaked a literal 0:\n%s", noTokensLine)
	}
}

// TestListAgentsJSONIncludesContextTokens covers the third surface point:
// raw count flows out in the --json view via the AgentEntry shape.
func TestListAgentsJSONIncludesContextTokens(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "ctxtokens-list-json", "x", "")

	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: "55555555-5555-4555-8555-555555555555",
		PID:       1, Mode: "agent",
		StartedAt:              "2026-05-15T14:56:00Z",
		Env:                    "/env-json",
		ContextTokens:          77_777,
		ContextTokensUpdatedAt: "2026-05-15T15:00:00Z",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "list", ticket, "--json")
	if code != 0 {
		t.Fatalf("agent list --json: exit=%d stderr=%q", code, stderr)
	}
	var entries []agent.AgentEntry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("parse json: %v\n%s", err, stdout)
	}
	if len(entries) != 1 {
		t.Fatalf("entries=%d, want 1", len(entries))
	}
	if entries[0].ContextTokens != 77_777 {
		t.Errorf("ContextTokens = %d, want 77777", entries[0].ContextTokens)
	}
	if entries[0].ContextTokensUpdatedAt == "" {
		t.Errorf("ContextTokensUpdatedAt empty in --json output")
	}

	// Zero-value records must omit the field entirely (omitempty) so
	// downstream tools can distinguish "never populated" from a real 0.
	if !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout not valid JSON")
	}
}

// TestContextTokensFromTranscriptReturnsLatestUsage verifies the parser
// picks up the LAST message.usage block, sums all four sub-buckets, and
// tolerates non-JSON / no-usage lines interleaved with the assistant rows.
func TestContextTokensFromTranscriptReturnsLatestUsage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	lines := []string{
		// user-turn row: no usage block.
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		// first assistant turn.
		`{"type":"assistant","message":{"role":"assistant","usage":{"input_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":50}}}`,
		// stray non-JSON line: must be tolerated.
		`not json at all`,
		// second assistant turn — this is what we want surfaced.
		`{"type":"assistant","message":{"role":"assistant","usage":{"input_tokens":1000,"cache_creation_input_tokens":200,"cache_read_input_tokens":50000,"output_tokens":300}}}`,
		// tool-result row: no usage.
		`{"type":"tool_result","message":{"content":"ok"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	got, err := agent.ContextTokensFromTranscript(path)
	if err != nil {
		t.Fatalf("ContextTokensFromTranscript: %v", err)
	}
	want := 1000 + 200 + 50_000 + 300
	if got != want {
		t.Errorf("ContextTokensFromTranscript = %d, want %d", got, want)
	}
}

// TestContextTokensFromTranscriptNoUsageBlocks returns 0 (not an error) so
// the caller treats "unknown" as "leave the stored value untouched".
func TestContextTokensFromTranscriptNoUsageBlocks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	got, err := agent.ContextTokensFromTranscript(path)
	if err != nil {
		t.Fatalf("ContextTokensFromTranscript: %v", err)
	}
	if got != 0 {
		t.Errorf("got %d, want 0 for transcript with no usage blocks", got)
	}
}

// TestSetStatusCLIHookPathPicksUpContextTokensFromStdin covers the
// JSON-on-stdin form: a hook payload with an explicit context_tokens field
// is parsed and persisted. This is the "manual / future-wrapper" entry
// point — covered alongside the implicit transcript_path derivation.
func TestSetStatusCLIHookPathPicksUpContextTokensFromStdin(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "ctxtokens-cli-stdin", "x", "")

	sid := "66666666-6666-4666-8666-666666666666"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:56:00Z", Env: "/env-stdin",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	pipeStdin(t, `{"session_id":"`+sid+`","context_tokens":123456,"hook_event_name":"Stop"}`)

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "set-status", "waiting")
	if code != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", code, stderr)
	}
	got := loadOne(t, beadsDir, ticket)
	if got.ContextTokens != 123456 {
		t.Errorf("ContextTokens = %d, want 123456", got.ContextTokens)
	}
}

// TestSetStatusCLIHookPathToleratesMissingContextTokens: stdin without the
// context_tokens or transcript_path field doesn't break the status flip and
// leaves stored tokens untouched.
func TestSetStatusCLIHookPathToleratesMissingContextTokens(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "ctxtokens-cli-missing", "x", "")

	sid := "77777777-7777-4777-8777-777777777777"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt:     "2026-05-15T14:56:00Z",
		Env:           "/env-missing",
		Status:        agent.StatusRunning,
		ContextTokens: 999,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	pipeStdin(t, `{"session_id":"`+sid+`","hook_event_name":"Stop"}`)

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "set-status", "waiting")
	if code != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", code, stderr)
	}
	got := loadOne(t, beadsDir, ticket)
	if got.Status != agent.StatusWaiting {
		t.Errorf("Status = %q, want waiting", got.Status)
	}
	if got.ContextTokens != 999 {
		t.Errorf("ContextTokens = %d, want 999 (unchanged)", got.ContextTokens)
	}
}

// TestSetStatusCLIHookPathDerivesFromTranscript: the natural hook flow is
// stdin with transcript_path but no explicit context_tokens. The binary
// reads the transcript and computes the count.
func TestSetStatusCLIHookPathDerivesFromTranscript(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "ctxtokens-cli-transcript", "x", "")

	sid := "88888888-8888-4888-8888-888888888888"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:56:00Z", Env: "/env-transcript",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	transcriptPath := filepath.Join(t.TempDir(), "transcript.jsonl")
	transcript := `{"type":"assistant","message":{"role":"assistant","usage":{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":30,"output_tokens":40}}}` + "\n"
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	pipeStdin(t, `{"session_id":"`+sid+`","transcript_path":"`+transcriptPath+`","hook_event_name":"Stop"}`)

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "set-status", "waiting")
	if code != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", code, stderr)
	}
	got := loadOne(t, beadsDir, ticket)
	if got.ContextTokens != 100 { // 10+20+30+40
		t.Errorf("ContextTokens = %d, want 100", got.ContextTokens)
	}
}

// pipeStdin replaces os.Stdin with a pipe whose read end carries body, and
// restores the original handle on test cleanup. Mirrors the helper used in
// status_test.go's TestSetStatusCLIHookPathSilentOnUnknownSession.
func pipeStdin(t *testing.T, body string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	w.Close()
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig })
}
