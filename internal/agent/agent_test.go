package agent_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/env"
	"framework2/internal/testutil"
)

func TestRegenerateClaudeMDContainsTicketFields(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket, title := createBeadWithDescription(t, "agent-claudemd-fields",
		"a short description for the agent regen test")
	envPath := mustEnv(t, beadsDir, ticket)

	if err := agent.Regenerate("", envPath, ticket); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}

	body := readFile(t, filepath.Join(envPath, "CLAUDE.md"))
	if !strings.Contains(body, ticket) {
		t.Errorf("CLAUDE.md missing ticket id %q\n--- body ---\n%s", ticket, body)
	}
	if !strings.Contains(body, title) {
		t.Errorf("CLAUDE.md missing title %q\n--- body ---\n%s", title, body)
	}
	if !strings.Contains(body, "a short description for the agent regen test") {
		t.Errorf("CLAUDE.md missing description\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, "## Linked directories") {
		t.Errorf("CLAUDE.md missing Linked directories section\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, "## Linking more directories into this session") {
		t.Errorf("CLAUDE.md missing 'Linking more directories' placeholder section\n--- body ---\n%s", body)
	}
}

func TestRegenerateSettingsMatchesLinkedDirs(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket, _ := createBeadWithDescription(t, "agent-settings-paths", "x")
	envPath := mustEnv(t, beadsDir, ticket)

	// Empty: settings.json must still be written with an empty array.
	if err := agent.Regenerate("", envPath, ticket); err != nil {
		t.Fatalf("Regenerate (empty): %v", err)
	}
	got := readSettings(t, envPath)
	if got == nil {
		t.Fatal("settings.permissions.additionalDirectories must be [], got nil")
	}
	if len(got) != 0 {
		t.Errorf("empty linked-dir list produced %v, want []", got)
	}

	// Link two dirs and re-regen — settings must list exactly those paths.
	d1 := t.TempDir()
	d2 := t.TempDir()
	if _, _, code := testutil.RunTicket(t, "dir", "link", ticket, d1); code != 0 {
		t.Fatalf("link d1: exit=%d", code)
	}
	if _, _, code := testutil.RunTicket(t, "dir", "link", ticket, d2); code != 0 {
		t.Fatalf("link d2: exit=%d", code)
	}
	if err := agent.Regenerate("", envPath, ticket); err != nil {
		t.Fatalf("Regenerate (two dirs): %v", err)
	}
	got = readSettings(t, envPath)
	want := []string{d1, d2}
	if !equalStringSlices(got, want) {
		t.Errorf("settings paths = %v, want %v", got, want)
	}

	// Remove one and re-regen — stale entry must drop, not merge.
	if _, _, code := testutil.RunTicket(t, "dir", "remove", ticket, d1); code != 0 {
		t.Fatalf("remove d1: exit=%d", code)
	}
	if err := agent.Regenerate("", envPath, ticket); err != nil {
		t.Fatalf("Regenerate (after remove): %v", err)
	}
	got = readSettings(t, envPath)
	want = []string{d2}
	if !equalStringSlices(got, want) {
		t.Errorf("after remove, settings paths = %v, want %v", got, want)
	}
}

func TestRegenerateOverwritesStaleClaudeMD(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket, _ := createBeadWithDescription(t, "agent-stale-claudemd", "real description")
	envPath := mustEnv(t, beadsDir, ticket)

	// Plant stale content.
	stale := "# stale stale stale\n\nhand-written content that must vanish\n"
	if err := os.WriteFile(filepath.Join(envPath, "CLAUDE.md"), []byte(stale), 0o644); err != nil {
		t.Fatalf("plant stale: %v", err)
	}

	if err := agent.Regenerate("", envPath, ticket); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	body := readFile(t, filepath.Join(envPath, "CLAUDE.md"))
	if strings.Contains(body, "hand-written content that must vanish") {
		t.Errorf("stale content survived regen\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, "real description") {
		t.Errorf("regenerated content missing description\n--- body ---\n%s", body)
	}
}

// helpers

func createBeadWithDescription(t *testing.T, title, description string) (id, gotTitle string) {
	t.Helper()
	out, err := exec.Command("bd", "create", title, "-d", description, "--silent").Output()
	if err != nil {
		t.Fatalf("bd create: %v", err)
	}
	id = strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("bd", "close", id).Run() })
	return id, title
}

func mustEnv(t *testing.T, beadsDir, ticket string) string {
	t.Helper()
	envPath, err := env.Create("", beadsDir, ticket)
	if err != nil {
		t.Fatalf("env.Create: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(beadsDir, "envs", ticket)) })
	return envPath
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

// readSettings reads <env>/.claude/settings.json, strips the leading JSONC
// header comment, and returns additionalDirectories.
func readSettings(t *testing.T, envPath string) []string {
	t.Helper()
	raw := readFile(t, filepath.Join(envPath, ".claude", "settings.json"))
	// Strip a leading `// ...\n` JSONC header if present.
	if strings.HasPrefix(raw, "//") {
		if i := strings.IndexByte(raw, '\n'); i >= 0 {
			raw = raw[i+1:]
		}
	}
	var payload agent.SettingsPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("parse settings.json: %v\n--- body ---\n%s", err, raw)
	}
	return payload.Permissions.AdditionalDirectories
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
