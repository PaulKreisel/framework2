// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

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

// TestAppendConversationUserEntryFromUserPromptSubmit covers the
// UserPromptSubmit hook signature: stdin carries session_id and prompt; the
// agent record gains a `user` entry whose Prompt field round-trips.
func TestAppendConversationUserEntryFromUserPromptSubmit(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "conv-user-prompt", "x", "")

	sid := "11111111-1111-4aaa-8aaa-conv11111111"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusWaiting,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	payload := `{"session_id":"` + sid + `","hook_event_name":"UserPromptSubmit","prompt":"Refactor the auth flow."}`
	pipeStdin(t, payload)

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "set-status", "running")
	if code != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", code, stderr)
	}

	got := loadOne(t, beadsDir, ticket)
	if got.Status != agent.StatusRunning {
		t.Errorf("Status = %q, want %q", got.Status, agent.StatusRunning)
	}
	if len(got.Conversation) != 1 {
		t.Fatalf("Conversation length = %d, want 1: %+v", len(got.Conversation), got.Conversation)
	}
	e := got.Conversation[0]
	if e.Type != agent.ConvTypeUser {
		t.Errorf("entry type = %q, want %q", e.Type, agent.ConvTypeUser)
	}
	if e.Prompt != "Refactor the auth flow." {
		t.Errorf("entry prompt = %q, want %q", e.Prompt, "Refactor the auth flow.")
	}
}

// TestAppendConversationSpecModeAlsoAccumulates: spec-mode records use the
// same Conversation array as agent-mode records. A UserPromptSubmit hook
// firing for a spec-mode session must append a `user` entry just like in
// agent mode.
func TestAppendConversationSpecModeAlsoAccumulates(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "conv-spec-accumulates", "x", "")

	sid := "22222222-2222-4aaa-8aaa-conv22222222"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "spec",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusWaiting,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	payload := `{"session_id":"` + sid + `","hook_event_name":"UserPromptSubmit","prompt":"please refactor"}`
	pipeStdin(t, payload)

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "set-status", "running")
	if code != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", code, stderr)
	}
	got := loadOne(t, beadsDir, ticket)
	if len(got.Conversation) != 1 {
		t.Fatalf("Conversation length = %d, want 1: %+v", len(got.Conversation), got.Conversation)
	}
	if got.Conversation[0].Type != agent.ConvTypeUser {
		t.Errorf("entry type = %q, want %q", got.Conversation[0].Type, agent.ConvTypeUser)
	}
	if got.Conversation[0].Prompt != "please refactor" {
		t.Errorf("entry prompt = %q, want %q", got.Conversation[0].Prompt, "please refactor")
	}
}

// TestAppendConversationStopAppendsAssistantSegments: the Stop hook reads
// the transcript and appends one assistant_comment per intermediate text
// segment plus an assistant_reply for the final segment. Each entry carries
// the per-message contextTokens from the transcript usage block.
func TestAppendConversationStopAppendsAssistantSegments(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "conv-stop-segments", "x", "")

	sid := "33333333-3333-4aaa-8aaa-conv33333333"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	transcriptPath := filepath.Join(t.TempDir(), "session.jsonl")
	transcript := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"do the thing"}}`,
		// First assistant text segment.
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Let me think."}],"usage":{"input_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":10}}}`,
		// Tool call: skipped.
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read"}],"usage":{"input_tokens":110,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":5}}}`,
		// Tool result: skipped.
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`,
		// Second (final) assistant text segment.
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done."}],"usage":{"input_tokens":200,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":20}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	payload := `{"session_id":"` + sid + `","hook_event_name":"Stop","transcript_path":"` + transcriptPath + `"}`
	pipeStdin(t, payload)

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "set-status", "waiting")
	if code != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", code, stderr)
	}

	got := loadOne(t, beadsDir, ticket)
	if len(got.Conversation) != 2 {
		t.Fatalf("Conversation length = %d, want 2: %+v", len(got.Conversation), got.Conversation)
	}
	if got.Conversation[0].Type != agent.ConvTypeAssistantComment {
		t.Errorf("entry[0] type = %q, want %q", got.Conversation[0].Type, agent.ConvTypeAssistantComment)
	}
	if got.Conversation[0].Text != "Let me think." {
		t.Errorf("entry[0] text = %q, want %q", got.Conversation[0].Text, "Let me think.")
	}
	if got.Conversation[0].ContextTokens != 110 { // 100+0+0+10
		t.Errorf("entry[0] tokens = %d, want 110", got.Conversation[0].ContextTokens)
	}
	if got.Conversation[1].Type != agent.ConvTypeAssistantReply {
		t.Errorf("entry[1] type = %q, want %q", got.Conversation[1].Type, agent.ConvTypeAssistantReply)
	}
	if got.Conversation[1].Text != "Done." {
		t.Errorf("entry[1] text = %q, want %q", got.Conversation[1].Text, "Done.")
	}
	if got.Conversation[1].ContextTokens != 220 { // 200+0+0+20
		t.Errorf("entry[1] tokens = %d, want 220", got.Conversation[1].ContextTokens)
	}
	// Global contextTokens reflects the latest reading (the second
	// assistant message's usage).
	if got.ContextTokens != 220 {
		t.Errorf("global ContextTokens = %d, want 220", got.ContextTokens)
	}
}

// TestAppendConversationAskUserQuestionEntry: PreToolUse:AskUserQuestion
// appends an `ask_user_question` entry whose Input field carries the raw
// tool_input JSON verbatim. The summary walk derives the table cell.
func TestAppendConversationAskUserQuestionEntry(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "conv-ask-entry", "x", "")

	sid := "44444444-4444-4aaa-8aaa-conv44444444"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	toolInput := `{"questions":[{"question":"Which library should we use?","header":"Lib","options":[{"label":"a"},{"label":"b"}]}]}`
	payload := `{"session_id":"` + sid + `","hook_event_name":"PreToolUse","tool_input":` + toolInput + `}`
	pipeStdin(t, payload)

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "set-status", "waiting", "--source", "ask-user-question")
	if code != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", code, stderr)
	}

	got := loadOne(t, beadsDir, ticket)
	if len(got.Conversation) != 1 {
		t.Fatalf("Conversation length = %d, want 1: %+v", len(got.Conversation), got.Conversation)
	}
	e := got.Conversation[0]
	if e.Type != agent.ConvTypeAskUserQuestion {
		t.Errorf("entry type = %q, want %q", e.Type, agent.ConvTypeAskUserQuestion)
	}
	// The bd metadata round-trip re-emits JSON with different whitespace;
	// compare semantically by decoding both sides.
	var gotJSON, wantJSON any
	if err := json.Unmarshal(e.Input, &gotJSON); err != nil {
		t.Fatalf("decode stored Input: %v", err)
	}
	if err := json.Unmarshal([]byte(toolInput), &wantJSON); err != nil {
		t.Fatalf("decode want toolInput: %v", err)
	}
	gotBytes, _ := json.Marshal(gotJSON)
	wantBytes, _ := json.Marshal(wantJSON)
	if string(gotBytes) != string(wantBytes) {
		t.Errorf("entry Input = %s, want %s", string(gotBytes), string(wantBytes))
	}
	// Display summary derives from the entry.
	summary := agent.FormatWaitingReason(got)
	if !strings.HasPrefix(summary, "asking: ") || !strings.Contains(summary, "Which library") {
		t.Errorf("FormatWaitingReason = %q, want \"asking: Which library…\" form", summary)
	}
}

// TestAppendConversationStopFailureEntry: StopFailure appends a
// `stop_failure` entry carrying the matcher and message.
func TestAppendConversationStopFailureEntry(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "conv-stop-failure", "x", "")

	sid := "55555555-5555-4aaa-8aaa-conv55555555"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusRunning,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	payload := `{"session_id":"` + sid + `","hook_event_name":"StopFailure","matcher":"rate_limit","message":"API rate limit reached"}`
	pipeStdin(t, payload)
	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "set-status", "waiting", "--source", "stop-failure")
	if code != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", code, stderr)
	}
	got := loadOne(t, beadsDir, ticket)
	if len(got.Conversation) != 1 {
		t.Fatalf("Conversation length = %d, want 1: %+v", len(got.Conversation), got.Conversation)
	}
	e := got.Conversation[0]
	if e.Type != agent.ConvTypeStopFailure {
		t.Errorf("entry type = %q, want %q", e.Type, agent.ConvTypeStopFailure)
	}
	if e.Matcher != "rate_limit" || e.Message != "API rate limit reached" {
		t.Errorf("entry = %+v, want matcher=rate_limit message=\"API rate limit reached\"", e)
	}
	summary := agent.FormatWaitingReason(got)
	if !strings.HasPrefix(summary, "rate_limit") {
		t.Errorf("FormatWaitingReason = %q, want it to start with \"rate_limit\"", summary)
	}
}

// TestAppendConversationOrderUserAskStopReply pins a small turn ordering:
// UserPromptSubmit → PreToolUse:AskUserQuestion (after one mid-turn comment)
// → Stop (after the final reply). Verifies entries are appended in
// chronological order with assistant_comments interspersed.
func TestAppendConversationOrderUserAskStopReply(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "conv-order", "x", "")

	sid := "66666666-6666-4aaa-8aaa-conv66666666"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusWaiting,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	transcriptPath := filepath.Join(t.TempDir(), "session.jsonl")
	// Stage 1: user prompt submitted. Transcript so far carries just the
	// user line.
	stage1 := `{"type":"user","message":{"role":"user","content":"go"}}` + "\n"
	if err := os.WriteFile(transcriptPath, []byte(stage1), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	pipeStdin(t, `{"session_id":"`+sid+`","hook_event_name":"UserPromptSubmit","prompt":"go","transcript_path":"`+transcriptPath+`"}`)
	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "set-status", "running")
	if code != 0 {
		t.Fatalf("set-status running: exit=%d stderr=%q", code, stderr)
	}

	// Stage 2: assistant emits text "Let me think.", then invokes
	// AskUserQuestion. Transcript now carries both the text segment and
	// the tool_use row.
	stage2 := stage1 +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Let me think."}],"usage":{"input_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":10}}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"q","name":"AskUserQuestion"}],"usage":{"input_tokens":55,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":5}}}` + "\n"
	if err := os.WriteFile(transcriptPath, []byte(stage2), 0o644); err != nil {
		t.Fatalf("update transcript: %v", err)
	}
	toolInput := `{"questions":[{"question":"Which?"}]}`
	pipeStdin(t, `{"session_id":"`+sid+`","hook_event_name":"PreToolUse","tool_input":`+toolInput+`,"transcript_path":"`+transcriptPath+`"}`)
	_, stderr, code = testutil.RunTicketIn(t, beadsDir, "agent", "set-status", "waiting", "--source", "ask-user-question")
	if code != 0 {
		t.Fatalf("set-status waiting (ask): exit=%d stderr=%q", code, stderr)
	}

	// Stage 3: user answers, assistant emits final "Done." and stops.
	stage3 := stage2 +
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"b"}]}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Done."}],"usage":{"input_tokens":80,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":20}}}` + "\n"
	if err := os.WriteFile(transcriptPath, []byte(stage3), 0o644); err != nil {
		t.Fatalf("update transcript stage3: %v", err)
	}
	pipeStdin(t, `{"session_id":"`+sid+`","hook_event_name":"Stop","transcript_path":"`+transcriptPath+`"}`)
	_, stderr, code = testutil.RunTicketIn(t, beadsDir, "agent", "set-status", "waiting")
	if code != 0 {
		t.Fatalf("set-status waiting (stop): exit=%d stderr=%q", code, stderr)
	}

	got := loadOne(t, beadsDir, ticket)
	wantTypes := []string{
		agent.ConvTypeUser,
		agent.ConvTypeAssistantComment,
		agent.ConvTypeAskUserQuestion,
		agent.ConvTypeAssistantReply,
	}
	if len(got.Conversation) != len(wantTypes) {
		t.Fatalf("Conversation length = %d, want %d: %+v",
			len(got.Conversation), len(wantTypes), got.Conversation)
	}
	for i, want := range wantTypes {
		if got.Conversation[i].Type != want {
			t.Errorf("entry[%d] type = %q, want %q", i, got.Conversation[i].Type, want)
		}
	}
	if got.Conversation[0].Prompt != "go" {
		t.Errorf("entry[0] prompt = %q, want %q", got.Conversation[0].Prompt, "go")
	}
	if got.Conversation[1].Text != "Let me think." {
		t.Errorf("entry[1] text = %q, want %q", got.Conversation[1].Text, "Let me think.")
	}
	if got.Conversation[3].Text != "Done." {
		t.Errorf("entry[3] text = %q, want %q", got.Conversation[3].Text, "Done.")
	}
}

// TestAppendConversationListJSONShape: agent list --json round-trips the
// new Conversation field via the AgentEntry shape.
func TestAppendConversationListJSONShape(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "conv-list-json", "x", "")

	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: "77777777-7777-4aaa-8aaa-conv77777777",
		PID:       1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status:    agent.StatusWaiting,
		Conversation: []agent.ConvEntry{
			{Type: agent.ConvTypeUser, Prompt: "do it"},
			{Type: agent.ConvTypeAssistantReply, Text: "done", ContextTokens: 200},
		},
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
	if len(entries) != 1 || len(entries[0].Conversation) != 2 {
		t.Fatalf("entries=%+v, want one row with 2 conversation entries", entries)
	}
	if entries[0].Conversation[0].Prompt != "do it" {
		t.Errorf("entry[0].Prompt = %q, want \"do it\"", entries[0].Conversation[0].Prompt)
	}
	if entries[0].Conversation[1].ContextTokens != 200 {
		t.Errorf("entry[1].ContextTokens = %d, want 200", entries[0].Conversation[1].ContextTokens)
	}
}

// TestAppendConversationSkipsSubagent: hook payloads from a subagent
// (carrying agent_id) must not append to the parent's Conversation. The
// scope of this ticket is main-session-only.
func TestAppendConversationSkipsSubagent(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "conv-subagent-skip", "x", "")

	sid := "88888888-8888-4aaa-8aaa-conv88888888"
	if err := agent.RegisterAgent("", beadsDir, ticket, agent.AgentEntry{
		SessionID: sid, PID: 1, Mode: "agent",
		StartedAt: "2026-05-15T14:00:00Z", Env: "/env",
		Status: agent.StatusWaiting,
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// agent_id present → subagent payload.
	pipeStdin(t, `{"session_id":"`+sid+`","hook_event_name":"UserPromptSubmit","prompt":"sub","agent_id":"ab12cd34"}`)
	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "agent", "set-status", "running")
	if code != 0 {
		t.Fatalf("set-status: exit=%d stderr=%q", code, stderr)
	}
	got := loadOne(t, beadsDir, ticket)
	if len(got.Conversation) != 0 {
		t.Errorf("subagent payload polluted Conversation: %+v", got.Conversation)
	}
}
