package agent_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/dir"
	"framework2/internal/env"
	"framework2/internal/testutil"
)

func TestRegenerateWritesDescriptionMD(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-descmd-fields",
		"a short description for the agent regen test", "")
	envPath := mustEnv(t, beadsDir, ticket)

	prompt, err := agent.Regenerate("", beadsDir, envPath, ticket, agent.ModeAgent)
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
	t.Parallel()
	agentP, err := agent.BuildSystemPrompt("", "bd-1", "do the thing", agent.ModeAgent, nil, nil)
	if err != nil {
		t.Fatalf("BuildSystemPrompt agent: %v", err)
	}
	specP, err := agent.BuildSystemPrompt("", "bd-1", "do the thing", agent.ModeSpec, nil, nil)
	if err != nil {
		t.Fatalf("BuildSystemPrompt spec: %v", err)
	}

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
	// No dirs and no ancestor dirs -> no "Repos in this session" block.
	for _, p := range []string{agentP, specP} {
		if strings.Contains(p, "Repos in this session") {
			t.Errorf("empty dirs should not produce repos section:\n%s", p)
		}
	}
}

func TestBuildSystemPromptListsBareReposAndParentRepos(t *testing.T) {
	t.Parallel()
	current := []dir.Entry{
		{Path: "/wt/framework2-bd-1", BareRepo: "/bare/framework2.git"},
	}
	ancestors := []dir.Entry{
		// Same bare as current: should be excluded.
		{Path: "/wt/framework2-parent", BareRepo: "/bare/framework2.git"},
		// Different bare: should surface as actionable bullet.
		{Path: "/wt/other-parent", BareRepo: "/bare/other.git"},
		// No bareRepo: skipped entirely.
		{Path: "/wt/loose", LocalRepo: "/wt/loose"},
	}
	p, err := agent.BuildSystemPrompt("", "bd-1", "x", agent.ModeAgent, current, ancestors)
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}

	if !strings.Contains(p, "Repos linked into this session: framework2.") {
		t.Errorf("prompt missing current repo line:\n%s", p)
	}
	if !strings.Contains(p, "Parent tickets touch the following repos (add with `this dir worktree <path>`):") {
		t.Errorf("prompt missing parent-repos header:\n%s", p)
	}
	if !strings.Contains(p, "- other — `/bare/other.git`") {
		t.Errorf("prompt missing actionable bullet for `other`:\n%s", p)
	}
	if strings.Contains(p, "- framework2 — ") {
		t.Errorf("parent list should exclude repos already in current bead:\n%s", p)
	}
	if strings.Contains(p, "loose") {
		t.Errorf("entries without BareRepo should be omitted entirely:\n%s", p)
	}
}

func TestRegenerateSettingsMatchesLinkedDirs(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-settings-paths", "x", "")
	envPath := mustEnv(t, beadsDir, ticket)

	// Empty: settings.json must still be written with an empty array.
	if _, err := agent.Regenerate("", beadsDir, envPath, ticket, agent.ModeAgent); err != nil {
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
	if _, _, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, d1); code != 0 {
		t.Fatalf("link d1: exit=%d", code)
	}
	if _, _, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, d2); code != 0 {
		t.Fatalf("link d2: exit=%d", code)
	}
	if _, err := agent.Regenerate("", beadsDir, envPath, ticket, agent.ModeAgent); err != nil {
		t.Fatalf("Regenerate (two dirs): %v", err)
	}
	got = readSettings(t, envPath)
	want := []string{d1, d2}
	if !equalStringSlices(got, want) {
		t.Errorf("settings paths = %v, want %v", got, want)
	}

	// Remove one and re-regen — stale entry must drop, not merge.
	if _, _, code := testutil.RunTicketIn(t, beadsDir, "dir", "remove", ticket, d1); code != 0 {
		t.Fatalf("remove d1: exit=%d", code)
	}
	if _, err := agent.Regenerate("", beadsDir, envPath, ticket, agent.ModeAgent); err != nil {
		t.Fatalf("Regenerate (after remove): %v", err)
	}
	got = readSettings(t, envPath)
	want = []string{d2}
	if !equalStringSlices(got, want) {
		t.Errorf("after remove, settings paths = %v, want %v", got, want)
	}
}

func TestRegenerateOverwritesStaleDescriptionMD(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-stale-descmd", "real description", "")
	envPath := mustEnv(t, beadsDir, ticket)

	stale := "hand-written content that must vanish\n"
	if err := os.WriteFile(filepath.Join(envPath, agent.DescriptionFile), []byte(stale), 0o644); err != nil {
		t.Fatalf("plant stale: %v", err)
	}

	if _, err := agent.Regenerate("", beadsDir, envPath, ticket, agent.ModeAgent); err != nil {
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
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-no-claudemd", "x", "")
	envPath := mustEnv(t, beadsDir, ticket)

	if _, err := agent.Regenerate("", beadsDir, envPath, ticket, agent.ModeAgent); err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(envPath, "CLAUDE.md")); !os.IsNotExist(err) {
		t.Errorf("CLAUDE.md should not be written (err=%v)", err)
	}
}

func TestSyncbackPushesDescription(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-syncback", "original", "")
	envPath := mustEnv(t, beadsDir, ticket)

	if _, err := agent.Regenerate("", beadsDir, envPath, ticket, agent.ModeAgent); err != nil {
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
	out, err := testutil.BdCmd(beadsDir, "--json", "show", ticket).Output()
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
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-claim-sets", "x", "")

	if got := readStatus(t, beadsDir, ticket); got != "open" {
		t.Fatalf("precondition: status = %q, want open", got)
	}
	if err := agent.Claim("", beadsDir, ticket); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got := readStatus(t, beadsDir, ticket); got != "in_progress" {
		t.Errorf("after Claim, status = %q, want in_progress", got)
	}
}

func TestClaimErrorsWhenBlockedByDep(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	blocker := testutil.CreateBeadFull(t, beadsDir, "agent-claim-blocker", "x", "")
	blocked := testutil.CreateBeadFull(t, beadsDir, "agent-claim-blocked", "x", "")

	if out, err := testutil.BdCmd(beadsDir, "dep", "add", blocked, blocker).CombinedOutput(); err != nil {
		t.Fatalf("dep add: %v\n%s", err, out)
	}

	err := agent.Claim("", beadsDir, blocked)
	if err == nil {
		t.Fatalf("Claim on blocked ticket: want error, got nil")
	}
	if !strings.Contains(err.Error(), "blocked by "+blocker) {
		t.Errorf("error = %q, want mention of blocker %q", err.Error(), blocker)
	}
	if got := readStatus(t, beadsDir, blocked); got != "open" {
		t.Errorf("blocked ticket status = %q after refused claim, want open", got)
	}

	// Close the blocker — now the claim should succeed.
	if out, err := testutil.BdCmd(beadsDir, "close", blocker).CombinedOutput(); err != nil {
		t.Fatalf("close blocker: %v\n%s", err, out)
	}
	if err := agent.Claim("", beadsDir, blocked); err != nil {
		t.Fatalf("Claim after closing blocker: %v", err)
	}
	if got := readStatus(t, beadsDir, blocked); got != "in_progress" {
		t.Errorf("after Claim, status = %q, want in_progress", got)
	}
}

func TestClaimErrorsWhenAlreadyInProgress(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBeadFull(t, beadsDir, "agent-claim-already", "x", "")

	if err := agent.Claim("", beadsDir, ticket); err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	err := agent.Claim("", beadsDir, ticket)
	if err == nil {
		t.Fatalf("second Claim: want error, got nil")
	}
	if !strings.Contains(err.Error(), "in_progress") {
		t.Errorf("error message = %q, want mention of in_progress", err.Error())
	}
	if got := readStatus(t, beadsDir, ticket); got != "in_progress" {
		t.Errorf("status changed after refused claim: %q, want in_progress", got)
	}
}

// helpers

func readStatus(t *testing.T, beadsDir, ticket string) string {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "show", ticket).Output()
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
