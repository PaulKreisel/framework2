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

func TestRegenerateWritesDescriptionMD(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket, _ := createBeadWithDescription(t, "agent-descmd-fields",
		"a short description for the agent regen test")
	envPath := mustEnv(t, beadsDir, ticket)

	prompt, err := agent.Regenerate("", envPath, ticket, agent.ModeAgent)
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}

	desc := readFile(t, filepath.Join(envPath, agent.DescriptionFile))
	if !strings.Contains(desc, "a short description for the agent regen test") {
		t.Errorf("description.md missing body\n--- desc ---\n%s", desc)
	}
	// description.md is the description verbatim; it should NOT carry the
	// ticket id, title, or auto-gen banners.
	if strings.Contains(desc, ticket) {
		t.Errorf("description.md should not contain ticket id %q\n--- desc ---\n%s", ticket, desc)
	}
	if !strings.Contains(prompt, ticket) {
		t.Errorf("system prompt missing ticket id %q\n--- prompt ---\n%s", ticket, prompt)
	}
	if !strings.Contains(prompt, "description.md") {
		t.Errorf("system prompt missing reference to description.md\n--- prompt ---\n%s", prompt)
	}
}

func TestBuildSystemPromptModes(t *testing.T) {
	agentP := agent.BuildSystemPrompt("bd-1", "do the thing", agent.ModeAgent)
	specP := agent.BuildSystemPrompt("bd-1", "do the thing", agent.ModeSpec)

	for _, p := range []string{agentP, specP} {
		if !strings.Contains(p, "bd-1") || !strings.Contains(p, "do the thing") {
			t.Errorf("prompt missing ticket id or title:\n%s", p)
		}
		if !strings.Contains(p, "description.md") {
			t.Errorf("prompt missing description.md reference:\n%s", p)
		}
		if !strings.Contains(p, "this create followup") {
			t.Errorf("prompt missing `this create followup` reference:\n%s", p)
		}
	}
	if !strings.Contains(specP, "spec'ing") {
		t.Errorf("spec-mode prompt missing spec-mode guidance:\n%s", specP)
	}
	if !strings.Contains(specP, "this create child") {
		t.Errorf("spec-mode prompt missing `this create child` reference:\n%s", specP)
	}
	if !strings.Contains(specP, "this env syncback") {
		t.Errorf("spec-mode prompt missing syncback reference:\n%s", specP)
	}
	if strings.Contains(agentP, "spec'ing") {
		t.Errorf("agent-mode prompt should not mention spec'ing:\n%s", agentP)
	}
	if strings.Contains(agentP, "this create child") {
		t.Errorf("agent-mode prompt should not advertise `this create child`:\n%s", agentP)
	}
	if strings.Contains(agentP, "this agent relaunch") {
		t.Errorf("agent-mode prompt should not mention `this agent relaunch` (auto-relaunch now wired into dir link/worktree):\n%s", agentP)
	}
	if strings.Contains(specP, "this agent relaunch") {
		t.Errorf("spec-mode prompt should not mention `this agent relaunch`:\n%s", specP)
	}
}

func TestRegenerateSettingsMatchesLinkedDirs(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket, _ := createBeadWithDescription(t, "agent-settings-paths", "x")
	envPath := mustEnv(t, beadsDir, ticket)

	// Empty: settings.json must still be written with an empty array.
	if _, err := agent.Regenerate("", envPath, ticket, agent.ModeAgent); err != nil {
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
	if _, err := agent.Regenerate("", envPath, ticket, agent.ModeAgent); err != nil {
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
	if _, err := agent.Regenerate("", envPath, ticket, agent.ModeAgent); err != nil {
		t.Fatalf("Regenerate (after remove): %v", err)
	}
	got = readSettings(t, envPath)
	want = []string{d2}
	if !equalStringSlices(got, want) {
		t.Errorf("after remove, settings paths = %v, want %v", got, want)
	}
}

func TestRegenerateOverwritesStaleDescriptionMD(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket, _ := createBeadWithDescription(t, "agent-stale-descmd", "real description")
	envPath := mustEnv(t, beadsDir, ticket)

	stale := "hand-written content that must vanish\n"
	if err := os.WriteFile(filepath.Join(envPath, agent.DescriptionFile), []byte(stale), 0o644); err != nil {
		t.Fatalf("plant stale: %v", err)
	}

	if _, err := agent.Regenerate("", envPath, ticket, agent.ModeAgent); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	body := readFile(t, filepath.Join(envPath, agent.DescriptionFile))
	if strings.Contains(body, "hand-written content that must vanish") {
		t.Errorf("stale content survived regen\n--- body ---\n%s", body)
	}
	if !strings.Contains(body, "real description") {
		t.Errorf("regenerated content missing description\n--- body ---\n%s", body)
	}
}

func TestRegenerateDoesNotWriteClaudeMD(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket, _ := createBeadWithDescription(t, "agent-no-claudemd", "x")
	envPath := mustEnv(t, beadsDir, ticket)

	if _, err := agent.Regenerate("", envPath, ticket, agent.ModeAgent); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(envPath, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("CLAUDE.md should not be written (err=%v)", err)
	}
}

func TestSyncbackPushesDescription(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket, _ := createBeadWithDescription(t, "agent-syncback", "original")
	envPath := mustEnv(t, beadsDir, ticket)

	if _, err := agent.Regenerate("", envPath, ticket, agent.ModeAgent); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	newBody := "edited via spec session\n\nwith a second paragraph\n"
	if err := os.WriteFile(filepath.Join(envPath, agent.DescriptionFile), []byte(newBody), 0o644); err != nil {
		t.Fatalf("write description.md: %v", err)
	}

	if err := agent.Syncback("", beadsDir, ticket); err != nil {
		t.Fatalf("Syncback: %v", err)
	}

	// Read bead description back via bd --json show.
	out, err := exec.Command("bd", "--json", "show", ticket).Output()
	if err != nil {
		t.Fatalf("bd show: %v", err)
	}
	var rows []struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("bd show returned no rows")
	}
	if !strings.Contains(rows[0].Description, "edited via spec session") {
		t.Errorf("description was not synced back. got:\n%s", rows[0].Description)
	}
}

func TestClaimSetsInProgress(t *testing.T) {
	testutil.SharedBdDB(t)
	ticket, _ := createBeadWithDescription(t, "agent-claim-sets", "x")

	if got := readStatus(t, ticket); got != "open" {
		t.Fatalf("precondition: status = %q, want open", got)
	}
	if err := agent.Claim("", ticket); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got := readStatus(t, ticket); got != "in_progress" {
		t.Errorf("after Claim, status = %q, want in_progress", got)
	}
}

func TestClaimErrorsWhenBlockedByDep(t *testing.T) {
	testutil.SharedBdDB(t)
	blocker, _ := createBeadWithDescription(t, "agent-claim-blocker", "x")
	blocked, _ := createBeadWithDescription(t, "agent-claim-blocked", "x")

	if out, err := exec.Command("bd", "dep", "add", blocked, blocker).CombinedOutput(); err != nil {
		t.Fatalf("dep add: %v\n%s", err, out)
	}

	err := agent.Claim("", blocked)
	if err == nil {
		t.Fatalf("Claim on blocked ticket: want error, got nil")
	}
	if !strings.Contains(err.Error(), "blocked by "+blocker) {
		t.Errorf("error = %q, want mention of blocker %q", err.Error(), blocker)
	}
	if got := readStatus(t, blocked); got != "open" {
		t.Errorf("blocked ticket status = %q after refused claim, want open", got)
	}

	// Close the blocker — now the claim should succeed.
	if out, err := exec.Command("bd", "close", blocker).CombinedOutput(); err != nil {
		t.Fatalf("close blocker: %v\n%s", err, out)
	}
	if err := agent.Claim("", blocked); err != nil {
		t.Fatalf("Claim after closing blocker: %v", err)
	}
	if got := readStatus(t, blocked); got != "in_progress" {
		t.Errorf("after Claim, status = %q, want in_progress", got)
	}
}

func TestClaimErrorsWhenAlreadyInProgress(t *testing.T) {
	testutil.SharedBdDB(t)
	ticket, _ := createBeadWithDescription(t, "agent-claim-already", "x")

	if err := agent.Claim("", ticket); err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	err := agent.Claim("", ticket)
	if err == nil {
		t.Fatalf("second Claim: want error, got nil")
	}
	if !strings.Contains(err.Error(), "in_progress") {
		t.Errorf("error message = %q, want mention of in_progress", err.Error())
	}
	if got := readStatus(t, ticket); got != "in_progress" {
		t.Errorf("status changed after refused claim: %q, want in_progress", got)
	}
}

// helpers

func readStatus(t *testing.T, ticket string) string {
	t.Helper()
	out, err := exec.Command("bd", "--json", "show", ticket).Output()
	if err != nil {
		t.Fatalf("bd show %s: %v", ticket, err)
	}
	var rows []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse bd show %s: %v\n%s", ticket, err, out)
	}
	if len(rows) == 0 {
		t.Fatalf("bd show %s: no rows", ticket)
	}
	return rows[0].Status
}

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

// readSettings reads <env>/.claude/settings.json and returns
// additionalDirectories. The file is strict JSON (Claude Code's settings
// parser rejects JSONC), so parse it as such.
func readSettings(t *testing.T, envPath string) []string {
	t.Helper()
	raw := readFile(t, filepath.Join(envPath, ".claude", "settings.json"))
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
