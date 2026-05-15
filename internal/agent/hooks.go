package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// statusHookCommands maps each Claude Code hook event we install to the
// `this` invocation that should run when it fires. Stop fires when an
// assistant turn ends without a tool call (claude is waiting on the user);
// UserPromptSubmit fires when the user submits the next prompt. The hook
// payload is delivered as JSON on stdin — `this agent set-status` parses
// session_id from it when no --session flag is supplied.
//
// Exported as a var so tests can introspect/override the canonical command
// strings without re-deriving them from the installed config.
var statusHookCommands = map[string]string{
	"Stop":             "ticket agent set-status waiting",
	"UserPromptSubmit": "ticket agent set-status running",
}

// InstallStatusHooks ensures the Stop and UserPromptSubmit entries in
// ~/.claude/settings.json invoke `this agent set-status` so the agent's
// running/waiting state stays current. Idempotent: an existing hook block
// whose inner command matches what we'd install is left in place rather
// than duplicated, and unrelated keys (permissions, model, other hooks)
// are preserved by round-tripping through json.RawMessage.
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
	for event, command := range statusHookCommands {
		updated, dirty, err := ensureHookEvent(hooks[event], command)
		if err != nil {
			return fmt.Errorf("merge %s hook in %s: %w", event, path, err)
		}
		if dirty {
			hooks[event] = updated
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
// matcher block invoking command. dirty reports whether the array had to
// be modified — when an existing block already carries our command we
// leave the value unchanged so a re-run is a no-op.
//
// rawEvent may be nil/empty for an event we've never seen; in that case a
// single-entry array is returned. Unrecognised matcher fields inside
// existing entries are preserved via json.RawMessage.
func ensureHookEvent(rawEvent json.RawMessage, command string) (json.RawMessage, bool, error) {
	var entries []map[string]json.RawMessage
	if len(rawEvent) > 0 && string(rawEvent) != "null" {
		if err := json.Unmarshal(rawEvent, &entries); err != nil {
			return nil, false, fmt.Errorf("parse event array: %w", err)
		}
	}
	for _, entry := range entries {
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
	entries = append(entries, newEntry)
	out, err := json.Marshal(entries)
	if err != nil {
		return nil, false, err
	}
	return out, true, nil
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
