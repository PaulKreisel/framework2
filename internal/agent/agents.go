package agent

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"framework2/internal/bdcli"
)

// AgentEntry is one row in the ticket's metadata.agents array: a record of a
// Claude session that was launched against the ticket. Only the wrapper
// process writes these; `agent list` reads them back.
//
// PID is the wrapper process pid (the Go process running [Run]), which is
// stable across the wrapper's relaunch loop — unlike the claude pid recorded
// in [PIDFile], which changes each iteration.
//
// Status is a free-form string driven by Claude Code hooks via
// `this agent set-status`. Today's writers only emit "running" and "waiting";
// the field is intentionally not an enum so finer waiting states can be
// added later without a schema migration. Empty Status on a legacy entry is
// treated as "running" by [StateOf].
//
// Conversation is the append-only history of user prompts, assistant text
// segments, AskUserQuestion / ExitPlanMode invocations, and StopFailure
// events. Only [Mode] == "agent" sessions accumulate entries; spec-mode
// sessions leave it empty and continue to use [WaitingReason] /
// [WaitingReasonSource] for the waiting-reason render path.
//
// WaitingReason / WaitingReasonSource are the legacy single-value fields the
// Stop / PreToolUse / StopFailure hooks used to overwrite each turn. They are
// retained for spec-mode sessions. Agent-mode sessions leave these empty and
// the display layer derives the waiting-reason summary from the tail of
// [Conversation] instead.
type AgentEntry struct {
	SessionID              string      `json:"sessionId"`
	PID                    int         `json:"pid"`
	Mode                   string      `json:"mode"`
	StartedAt              string      `json:"startedAt"`
	Env                    string      `json:"env"`
	Status                 string      `json:"status,omitempty"`
	StatusUpdatedAt        string      `json:"statusUpdatedAt,omitempty"`
	WaitingReason          string      `json:"waitingReason,omitempty"`
	WaitingReasonSource    string      `json:"waitingReasonSource,omitempty"`
	ContextTokens          int         `json:"contextTokens,omitempty"`
	ContextTokensUpdatedAt string      `json:"contextTokensUpdatedAt,omitempty"`
	Conversation           []ConvEntry `json:"conversation,omitempty"`
}

// ConvEntry is one item in [AgentEntry.Conversation]. The shape is a union:
// the populated payload fields depend on Type. ContextTokens is the
// context-window size at the time the entry was produced — see the package
// docs on [AgentEntry] for the per-event-vs-global semantics.
type ConvEntry struct {
	Type          string          `json:"type"`
	Prompt        string          `json:"prompt,omitempty"`
	Text          string          `json:"text,omitempty"`
	Input         json.RawMessage `json:"input,omitempty"`
	Plan          string          `json:"plan,omitempty"`
	Matcher       string          `json:"matcher,omitempty"`
	Message       string          `json:"message,omitempty"`
	ContextTokens int             `json:"contextTokens,omitempty"`
}

// Conversation entry types.
const (
	ConvTypeUser             = "user"
	ConvTypeAssistantComment = "assistant_comment"
	ConvTypeAssistantReply   = "assistant_reply"
	ConvTypeAskUserQuestion  = "ask_user_question"
	ConvTypeExitPlanMode     = "exit_plan_mode"
	ConvTypeStopFailure      = "stop_failure"
)

// WaitingReason source tags. Empty means the legacy form — the Stop hook's
// captured assistant message — and is rendered as-is. The named sources
// flag richer payloads (raw tool_input JSON, plan markdown, or a
// matcher[:message] string) and route through source-aware formatting.
const (
	WaitingReasonSourceAskUserQuestion = "ask-user-question"
	WaitingReasonSourceExitPlanMode    = "exit-plan-mode"
	WaitingReasonSourceStopFailure     = "stop-failure"
)

// StatusRunning is the value the UserPromptSubmit hook writes when a turn
// starts (including the auto-submitted kickoff prompt in agent mode).
const StatusRunning = "running"

// StatusWaiting is the initial status assigned when an agent is registered
// (claude hasn't received any turn yet) and the value the Stop hook writes
// when an assistant turn ends and claude is waiting for the next user input.
const StatusWaiting = "waiting"

// String returns "agent" or "spec".
func (m Mode) String() string {
	switch m {
	case ModeSpec:
		return "spec"
	default:
		return "agent"
	}
}

// NewSessionID returns a freshly generated UUIDv4 suitable for passing to
// claude via `--session-id`. Exposed so callers that need the id before
// invoking [Run] (e.g. tests) can pre-generate it.
func NewSessionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("read random bytes: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// RegisterAgent appends entry to the ticket's metadata.agents array via a
// read-modify-write on the bead's metadata blob. Concurrent writes to other
// metadata keys (e.g. `dirs`) are preserved because `bd update --metadata`
// merges at the key level; concurrent writes to `agents` itself last-write-win,
// matching how [dir] handles its own array.
func RegisterAgent(db, beadsDir, ticketID string, entry AgentEntry) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	entries, err := LoadAgents(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	entries = append(entries, entry)
	return saveAgents(db, beadsDir, ticketID, entries)
}

// UpsertAgent replaces the existing entry with the same SessionID, or appends
// entry if no match exists. Used by the resume path so a re-launched wrapper
// updates the previous (exited) record in place rather than producing a
// duplicate that would shadow the live wrapper's status updates.
func UpsertAgent(db, beadsDir, ticketID string, entry AgentEntry) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	entries, err := LoadAgents(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	for i, e := range entries {
		if e.SessionID == entry.SessionID {
			entries[i] = entry
			return saveAgents(db, beadsDir, ticketID, entries)
		}
	}
	entries = append(entries, entry)
	return saveAgents(db, beadsDir, ticketID, entries)
}

// LoadAgents returns the agent records stored under the ticket's
// metadata.agents key, oldest first (insertion order). Nil when no agent has
// ever been launched against the ticket.
func LoadAgents(db, beadsDir, ticketID string) ([]AgentEntry, error) {
	row, err := showAgentsRow(db, beadsDir, ticketID)
	if err != nil {
		return nil, err
	}
	raw, ok := row.Metadata["agents"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var entries []AgentEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse agents entries: %w", err)
	}
	return entries, nil
}

func saveAgents(db, beadsDir, ticketID string, entries []AgentEntry) error {
	if entries == nil {
		entries = []AgentEntry{}
	}
	payload, err := json.Marshal(map[string]any{"agents": entries})
	if err != nil {
		return err
	}
	cmd := bdcli.Cmd(beadsDir, db, "update", ticketID, "--metadata", string(payload))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("update ticket %s agents: %w: %s", ticketID, err, out)
	}
	return nil
}

type agentsRow struct {
	Metadata map[string]json.RawMessage `json:"metadata"`
}

func showAgentsRow(db, beadsDir, ticketID string) (*agentsRow, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "show", ticketID).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("look up ticket %s: %v: %s", ticketID, err, ee.Stderr)
		}
		return nil, fmt.Errorf("look up ticket %s: %w", ticketID, err)
	}
	var rows []agentsRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse ticket store output: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("ticket not found: %s", ticketID)
	}
	return &rows[0], nil
}

// FormatContextTokens renders a token count for the compact list view.
// Values below 1000 print as-is; 1k–999k use a one-decimal "k" suffix;
// 1_000_000 and up use "M". Zero (i.e. the field was never populated,
// since omitempty drops it from JSON) renders as the empty string so the
// column is blank for sessions that have never reported a usage block.
func FormatContextTokens(n int) string {
	switch {
	case n <= 0:
		return ""
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
}

// StateOf returns the hook-driven Status for an agent record (defaulting to
// "running" for legacy entries that predate the status field). It does not
// probe wrapper-pid liveness: a wrapper killed mid-flight (e.g. terminal
// closed) can't reliably write a final status, and silently demoting such
// records to "exited" hides them from `next agent` so the user loses the
// ability to find and resume a waiting agent whose terminal died. The
// trade-off is that dead-wrapper records stay in their last hook-recorded
// state until manually cleaned via `agent delete`.
func StateOf(e AgentEntry) string {
	if e.Status == "" {
		return StatusRunning
	}
	return e.Status
}

// ErrSessionNotFound is returned by SetStatus when no agent record matches
// the supplied session id. Callers route this differently depending on how
// the lookup was initiated: a manual `set-status --session <id>` exits with
// the error (it's a typo backstop), but a hook-driven invocation suppresses
// it (the hook fires for every claude session, including ones that weren't
// launched via `ticket agent` and never registered an entry).
var ErrSessionNotFound = errors.New("no agent record found for session")

// SetStatus updates the matching agent record's Status and StatusUpdatedAt,
// and optionally its ContextTokens / ContextTokensUpdatedAt when
// contextTokens > 0. Lookup is by sessionID; ticketID, if non-empty, scopes
// the search to a single ticket. With ticketID empty all open tickets are
// scanned. A no-op (Status already equal to status, reason already equal to
// the stored value, contextTokens either 0 or already equal to the stored
// value) is fast-pathed without touching the bead. Returns
// [ErrSessionNotFound] when no record matches.
//
// reason and source describe what claude is waiting on. reason is either
// the assistant's last message (Stop hook, source == "") or a richer
// payload chosen by the caller — raw tool_input JSON, plan markdown, or
// a stop-failure matcher line. Both are only meaningful when
// status == [StatusWaiting]; on a transition back to [StatusRunning] they
// are cleared so a stale value can't survive a UserPromptSubmit.
//
// contextTokens is treated as an absolute snapshot, not a delta — a hook
// retry passing the same value is idempotent. 0 means "no token information
// available, leave ContextTokens unchanged"; callers that have a real zero
// reading shouldn't happen in practice (every Claude turn has at least
// output_tokens > 0).
func SetStatus(db, beadsDir, sessionID, status, reason, source, ticketID string, contextTokens int) error {
	if sessionID == "" {
		return errors.New("session id required")
	}
	if status == "" {
		return errors.New("status required")
	}
	if status != StatusWaiting {
		reason = ""
		source = ""
	}
	tickets := []string{ticketID}
	if ticketID == "" {
		all, err := ticketsWithAgents(db, beadsDir)
		if err != nil {
			return err
		}
		tickets = all
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, tid := range tickets {
		entries, err := LoadAgents(db, beadsDir, tid)
		if err != nil {
			return err
		}
		for i, e := range entries {
			if e.SessionID != sessionID {
				continue
			}
			// Agent-mode records have moved to the Conversation array;
			// WaitingReason / WaitingReasonSource are not written for them
			// (and any pre-existing value from a legacy entry is cleared on
			// the next status transition so a stale string can't render).
			effReason, effSource := reason, source
			if e.Mode == "agent" {
				effReason, effSource = "", ""
			}
			statusChanged := e.Status != status
			reasonChanged := e.WaitingReason != effReason
			sourceChanged := e.WaitingReasonSource != effSource
			tokensChanged := contextTokens > 0 && contextTokens != e.ContextTokens
			if !statusChanged && !reasonChanged && !sourceChanged && !tokensChanged {
				return nil
			}
			if statusChanged || reasonChanged || sourceChanged {
				entries[i].Status = status
				entries[i].StatusUpdatedAt = now
			}
			if reasonChanged {
				entries[i].WaitingReason = effReason
			}
			if sourceChanged {
				entries[i].WaitingReasonSource = effSource
			}
			if tokensChanged {
				entries[i].ContextTokens = contextTokens
				entries[i].ContextTokensUpdatedAt = now
			}
			return saveAgents(db, beadsDir, tid, entries)
		}
	}
	return fmt.Errorf("%w %s", ErrSessionNotFound, sessionID)
}

// AppendConversation appends entries to the matching agent record's
// [AgentEntry.Conversation] array and refreshes [AgentEntry.ContextTokens]
// when contextTokens > 0. Only agent-mode records are touched: spec-mode
// records keep the legacy waiting-reason mechanism, so the call is a silent
// no-op there. Returns [ErrSessionNotFound] when no record matches.
//
// Lookup is by sessionID; ticketID, if non-empty, scopes the search to one
// ticket. With ticketID empty all non-closed tickets are scanned (same shape
// as [SetStatus]).
//
// transcriptAssistantText, if non-nil, lists assistant text segments observed
// in the transcript. The function compares it against the existing run of
// `assistant_comment` / `assistant_reply` entries at the tail of
// Conversation, and appends entries for any segments not yet captured —
// each as `assistant_comment` except the final one when finalAsReply is true
// (the Stop hook calls this with finalAsReply=true so the last text segment
// of the turn is recorded as `assistant_reply`). Pass nil or an empty slice
// to skip the transcript reconciliation step.
//
// Empty entries (nil and len(entries) == 0) are tolerated so callers can use
// the function purely to update ContextTokens or to reconcile transcript
// segments without an explicit per-source append.
func AppendConversation(db, beadsDir, sessionID, ticketID string, transcriptAssistantText []TranscriptText, finalAsReply bool, entries []ConvEntry, contextTokens int) error {
	if sessionID == "" {
		return errors.New("session id required")
	}
	tickets := []string{ticketID}
	if ticketID == "" {
		all, err := ticketsWithAgents(db, beadsDir)
		if err != nil {
			return err
		}
		tickets = all
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for _, tid := range tickets {
		records, err := LoadAgents(db, beadsDir, tid)
		if err != nil {
			return err
		}
		for i, e := range records {
			if e.SessionID != sessionID {
				continue
			}
			if e.Mode != "" && e.Mode != "agent" {
				// Spec mode keeps the legacy WaitingReason path. Nothing to
				// append; ContextTokens still flows through SetStatus.
				return nil
			}
			updated := records[i]
			changed := false
			// Reconcile assistant text segments observed in the transcript
			// against the entries already stored at the tail of Conversation.
			newSegs := newAssistantSegments(updated.Conversation, transcriptAssistantText)
			for idx, seg := range newSegs {
				entryType := ConvTypeAssistantComment
				if finalAsReply && idx == len(newSegs)-1 {
					entryType = ConvTypeAssistantReply
				}
				updated.Conversation = append(updated.Conversation, ConvEntry{
					Type:          entryType,
					Text:          seg.Text,
					ContextTokens: seg.ContextTokens,
				})
				changed = true
			}
			for _, ent := range entries {
				updated.Conversation = append(updated.Conversation, ent)
				changed = true
			}
			if contextTokens > 0 && contextTokens != updated.ContextTokens {
				updated.ContextTokens = contextTokens
				updated.ContextTokensUpdatedAt = now
				changed = true
			}
			if !changed {
				return nil
			}
			records[i] = updated
			return saveAgents(db, beadsDir, tid, records)
		}
	}
	return fmt.Errorf("%w %s", ErrSessionNotFound, sessionID)
}

// newAssistantSegments returns the slice of transcript-observed assistant
// text segments that are not yet represented in conv. Captured count is the
// total number of `assistant_comment` / `assistant_reply` entries already in
// conv — since segments are always appended in chronological transcript
// order, the new tail is segs[captured:].
//
// We use a count-and-skip heuristic rather than text equality so minor
// whitespace differences between the live transcript and the stored snapshot
// don't cause duplicates. The transcript is the authoritative order; once
// stored, entries are immutable.
func newAssistantSegments(conv []ConvEntry, segs []TranscriptText) []TranscriptText {
	if len(segs) == 0 {
		return nil
	}
	captured := 0
	for _, e := range conv {
		if e.Type == ConvTypeAssistantComment || e.Type == ConvTypeAssistantReply {
			captured++
		}
	}
	if captured >= len(segs) {
		return nil
	}
	return segs[captured:]
}

// ticketsWithAgents lists every bead id whose metadata carries a non-empty
// agents array. Used by SetStatus when the caller didn't pin a ticket — hooks
// only have a session id, so we scan to locate the owning bead.
func ticketsWithAgents(db, beadsDir string) ([]string, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "list", "--all").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("list tickets: %v: %s", err, ee.Stderr)
		}
		return nil, fmt.Errorf("list tickets: %w", err)
	}
	var rows []struct {
		ID       string                     `json:"id"`
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse ticket list: %w", err)
	}
	var ids []string
	for _, r := range rows {
		raw, ok := r.Metadata["agents"]
		if !ok || len(raw) == 0 || string(raw) == "null" {
			continue
		}
		// Cheap presence check: an empty array marshals as "[]".
		if string(raw) == "[]" {
			continue
		}
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// waitingReasonTableMax caps the number of runes shown in the table view's
// WAITING REASON cell. Wide enough to read a short sentence, narrow enough
// to keep the row roughly one terminal width when other columns are filled.
const waitingReasonTableMax = 80

// FormatWaitingReason returns the single-line, truncated form of an entry's
// waiting reason for the list table. Empty unless the agent is in
// [StatusWaiting]: only waiting agents own a meaningful reason, and a stale
// reason from a previous turn must not bleed into a "running"/"exited" row.
//
// Agent-mode entries derive the summary from the tail of
// [AgentEntry.Conversation] — the most recent wait-representing entry
// (`assistant_reply`, `ask_user_question`, `exit_plan_mode`, `stop_failure`)
// supplies the value. Spec-mode entries (and pre-Conversation legacy
// records with an empty array) fall back to the
// [AgentEntry.WaitingReasonSource] / [AgentEntry.WaitingReason] pair.
func FormatWaitingReason(e AgentEntry) string {
	if StateOf(e) != StatusWaiting {
		return ""
	}
	summary := WaitingReasonSummary(e)
	return truncateSingleLine(summary, waitingReasonTableMax)
}

// WaitingReasonSummary returns the untruncated, single-source-of-truth
// waiting-reason summary for e. It is the value that
// [FormatWaitingReason] truncates for the table cell, and that
// [Status] (agent status --json / human form) emits verbatim. Empty when
// no wait-representing entry exists.
//
// Agent-mode lookup: walk Conversation from the tail, pick the first entry
// of a wait-representing type, summarise according to that type. Spec mode
// (and legacy records that predate the array): use WaitingReasonSource /
// WaitingReason as before.
func WaitingReasonSummary(e AgentEntry) string {
	if e.Mode == "agent" {
		return waitingReasonFromConversation(e.Conversation)
	}
	return summarizeWaitingReason(e.WaitingReasonSource, e.WaitingReason)
}

// WaitingReasonFull returns the untruncated raw payload for the most recent
// wait-representing conversation entry (agent mode) or the stored
// [AgentEntry.WaitingReason] (spec mode). Used by `agent status` to print a
// full-fidelity block under the metadata.
func WaitingReasonFull(e AgentEntry) string {
	if e.Mode != "agent" {
		return e.WaitingReason
	}
	for i := len(e.Conversation) - 1; i >= 0; i-- {
		c := e.Conversation[i]
		switch c.Type {
		case ConvTypeAssistantReply:
			return c.Text
		case ConvTypeAskUserQuestion:
			return string(c.Input)
		case ConvTypeExitPlanMode:
			return c.Plan
		case ConvTypeStopFailure:
			if c.Matcher != "" && c.Message != "" {
				return c.Matcher + ": " + c.Message
			}
			if c.Matcher != "" {
				return c.Matcher
			}
			return c.Message
		}
	}
	return ""
}

// waitingReasonFromConversation walks conv from the tail and renders the
// short table summary for the most recent wait-representing entry. Returns
// "" when no such entry exists (e.g. a brand-new session that has only
// captured a `user` entry so far).
func waitingReasonFromConversation(conv []ConvEntry) string {
	for i := len(conv) - 1; i >= 0; i-- {
		c := conv[i]
		switch c.Type {
		case ConvTypeAssistantReply:
			return c.Text
		case ConvTypeAskUserQuestion:
			if q := firstAskQuestion(string(c.Input)); q != "" {
				return "asking: " + q
			}
			return "asking"
		case ConvTypeExitPlanMode:
			if line := firstNonEmptyLine(c.Plan); line != "" {
				return "plan: " + line
			}
			return "plan"
		case ConvTypeStopFailure:
			if c.Matcher != "" && c.Message != "" {
				return c.Matcher + ": " + c.Message
			}
			if c.Matcher != "" {
				return c.Matcher
			}
			return c.Message
		}
	}
	return ""
}

// summarizeWaitingReason maps a (source, raw payload) pair to the short
// human-readable form shown in the table. The raw payload stays in
// [AgentEntry.WaitingReason] so `agent status` keeps full fidelity.
func summarizeWaitingReason(source, raw string) string {
	switch source {
	case WaitingReasonSourceAskUserQuestion:
		if q := firstAskQuestion(raw); q != "" {
			return "asking: " + q
		}
		return "asking"
	case WaitingReasonSourceExitPlanMode:
		if line := firstNonEmptyLine(raw); line != "" {
			return "plan: " + line
		}
		return "plan"
	case WaitingReasonSourceStopFailure:
		return raw
	default:
		return raw
	}
}

// firstAskQuestion parses the AskUserQuestion tool_input JSON and returns
// the text of the first question. Returns "" if the JSON doesn't carry a
// recognisable questions[].question field — better to show "asking" alone
// than crash the table render.
func firstAskQuestion(raw string) string {
	var payload struct {
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	for _, q := range payload.Questions {
		if q.Question != "" {
			return q.Question
		}
	}
	return ""
}

// firstNonEmptyLine returns the first non-blank line of s, trimmed of
// surrounding whitespace. Used to derive the table summary from a plan's
// markdown body.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

func truncateSingleLine(s string, max int) string {
	if s == "" {
		return ""
	}
	// Collapse all whitespace (including newlines) to single spaces and trim
	// leading/trailing space so the cell is one tidy line.
	var b []rune
	prevSpace := true // start at "space" so leading whitespace is dropped
	for _, r := range s {
		isSpace := r == ' ' || r == '\t' || r == '\n' || r == '\r'
		if isSpace {
			if !prevSpace {
				b = append(b, ' ')
			}
			prevSpace = true
			continue
		}
		b = append(b, r)
		prevSpace = false
	}
	// Drop trailing space if any.
	if len(b) > 0 && b[len(b)-1] == ' ' {
		b = b[:len(b)-1]
	}
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}

// FormatStarted re-renders an RFC 3339 timestamp as "YYYY-MM-DD HH:MM" in
// local time for the table view. Invalid input is passed through verbatim so
// no agent record ever silently disappears.
func FormatStarted(s string) string {
	if s == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04")
}

