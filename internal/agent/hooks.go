package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// statusHook describes one Claude Code hook block we install. Event is the
// top-level hook key (e.g. "Stop", "PreToolUse", "StopFailure"). Matcher
// filters which invocation triggers the hook — empty means "matcher-less",
// matching every invocation of Event. Command is the `this` invocation that
// runs when the hook fires.
//
// PreToolUse hooks scope to a specific tool name. The `--source` argument
// distinguishes which pathway invoked set-status so the reason builder picks
// the right field off the stdin payload.
//
// Exported as a var so tests can introspect/override the canonical list
// without re-deriving it from the installed config.
type statusHook struct {
	Event   string
	Matcher string
	Command string
}

var statusHooks = []statusHook{
	{Event: "Stop", Command: "ticket agent set-status waiting"},
	{Event: "UserPromptSubmit", Command: "ticket agent set-status running"},
	{Event: "PreToolUse", Matcher: "AskUserQuestion", Command: "ticket agent set-status waiting --source ask-user-question"},
	{Event: "PreToolUse", Matcher: "ExitPlanMode", Command: "ticket agent set-status waiting --source exit-plan-mode"},
	{Event: "StopFailure", Command: "ticket agent set-status waiting --source stop-failure"},
	// Generic PostToolUse hook: tool calls don't add a conversation entry
	// (per ai-vrc.47 they're out of scope), but they're the most frequent
	// event in a coding session, so we use them to refresh the agent
	// record's global contextTokens snapshot and to reconcile any new
	// assistant_comment text segments emitted before the tool call. The
	// status flip running→running is a no-op (or, for the
	// AskUserQuestion/ExitPlanMode case where status was just flipped to
	// waiting and the user has now answered, it correctly re-enters
	// running). No explicit --source: there's no header entry to add.
	{Event: "PostToolUse", Command: "ticket agent set-status running"},
}

// InstallStatusHooks ensures the entries in ~/.claude/settings.json invoke
// `this agent set-status` for every hook event in [statusHooks] so the
// agent's running/waiting state stays current. Idempotent: an existing block
// whose matcher and inner command already match what we'd install is left
// in place rather than duplicated. Unrelated keys (permissions, model) and
// unrelated matcher blocks under the same event are preserved by
// round-tripping through json.RawMessage.
//
// A missing settings.json is created on demand. A settings.json that exists
// but is not valid JSON is left untouched and the error surfaced — we never
// rewrite a file we couldn't parse, since that would risk clobbering a
// hand-edited user config.
func InstallStatusHooks() error {
	path, err := claudeSettingsPath()
	if err != nil {
		return err
	}
	return installStatusHooksAt(path)
}

func installStatusHooksAt(path string) error {
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		raw = nil
	case err != nil:
		return fmt.Errorf("read %s: %w", path, err)
	}

	// Decode into a generic map so we can round-trip unknown keys.
	settings := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("parse %s: %w (refusing to rewrite a malformed settings file)", path, err)
		}
	}

	hooks := map[string]json.RawMessage{}
	if rawHooks, ok := settings["hooks"]; ok && len(rawHooks) > 0 && string(rawHooks) != "null" {
		if err := json.Unmarshal(rawHooks, &hooks); err != nil {
			return fmt.Errorf("parse hooks in %s: %w (refusing to rewrite a malformed settings file)", path, err)
		}
	}

	changed := false
	for _, h := range statusHooks {
		updated, dirty, err := ensureHookEvent(hooks[h.Event], h.Matcher, h.Command)
		if err != nil {
			return fmt.Errorf("merge %s hook in %s: %w", h.Event, path, err)
		}
		if dirty {
			hooks[h.Event] = updated
			changed = true
		}
	}
	if !changed {
		return nil
	}

	mergedHooks, err := json.Marshal(hooks)
	if err != nil {
		return fmt.Errorf("marshal hooks: %w", err)
	}
	settings["hooks"] = mergedHooks

	body, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	body = append(body, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create settings dir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// ensureHookEvent inspects the raw JSON array stored under one hook event
// (e.g. settings.hooks.Stop) and returns an updated array that contains a
// block whose "matcher" field equals matcher (or is absent, when matcher is
// empty) and that invokes command. dirty reports whether the array had to
// be modified — when an existing matching block already carries our command
// we leave the value unchanged so a re-run is a no-op.
//
// rawEvent may be nil/empty for an event we've never seen; in that case a
// single-entry array is returned. Blocks whose matcher differs from ours
// (including user-defined matcher blocks for unrelated tools/errors) are
// preserved unchanged via json.RawMessage.
func ensureHookEvent(rawEvent json.RawMessage, matcher, command string) (json.RawMessage, bool, error) {
	var entries []map[string]json.RawMessage
	if len(rawEvent) > 0 && string(rawEvent) != "null" {
		if err := json.Unmarshal(rawEvent, &entries); err != nil {
			return nil, false, fmt.Errorf("parse event array: %w", err)
		}
	}
	for _, entry := range entries {
		if entryMatcher(entry) != matcher {
			continue
		}
		if hookEntryContainsCommand(entry["hooks"], command) {
			return rawEvent, false, nil
		}
	}
	newEntry := map[string]json.RawMessage{
		"hooks": mustMarshal([]map[string]string{{
			"type":    "command",
			"command": command,
		}}),
	}
	if matcher != "" {
		newEntry["matcher"] = mustMarshal(matcher)
	}
	entries = append(entries, newEntry)
	out, err := json.Marshal(entries)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
}

// entryMatcher returns the "matcher" field of one event-array entry. Missing
// or non-string matcher decodes as "" — the matcher-less form.
func entryMatcher(entry map[string]json.RawMessage) string {
	raw, ok := entry["matcher"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}

// hookEntryContainsCommand decodes the inner "hooks" array of one matcher
// block and returns true when any element is a {"type":"command", ...}
// invocation whose command matches.
func hookEntryContainsCommand(rawHooks json.RawMessage, command string) bool {
	if len(rawHooks) == 0 || string(rawHooks) == "null" {
		return false
	}
	var inner []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal(rawHooks, &inner); err != nil {
		return false
	}
	for _, h := range inner {
		if h.Type == "command" && h.Command == command {
			return true
		}
	}
	return false
}

func mustMarshal(v any) json.RawMessage {
	out, err := json.Marshal(v)
	if err != nil {
		// Marshaling a fixed-shape map[string]string can't fail in practice.
		panic(fmt.Sprintf("agent: marshal hook payload: %v", err))
	}
	return out
}

// claudeSettingsPath returns ~/.claude/settings.json. Overridable via
// CLAUDE_SETTINGS_PATH so tests can target a tmpdir.
func claudeSettingsPath() (string, error) {
	if override := os.Getenv("CLAUDE_SETTINGS_PATH"); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}
