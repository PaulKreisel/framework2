package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/agent"
)

// TestBuildSystemPromptOperatorFileOmitsFragment exercises the design point
// that an operator file can drop a fragment by simply not referencing it.
func TestBuildSystemPromptOperatorFileOmitsFragment(t *testing.T) {
	t.Parallel()
	beadsDir := t.TempDir()
	// Operator file with no `{{cmd_env_syncback}}` and no repos fragments.
	tmpl := "# Ticket {{ticket_id}}: {{title}}\n\nExecute the task described in description.md. Read it first.\n"
	if err := os.WriteFile(filepath.Join(beadsDir, agent.AgentPromptFile), []byte(tmpl), 0o644); err != nil {
		t.Fatalf("write operator file: %v", err)
	}

	got, err := agent.BuildSystemPrompt(beadsDir, "bd-1", "title", agent.ModeAgent, nil, nil)
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	want := "# Ticket bd-1: title\n\nExecute the task described in description.md. Read it first.\n"
	if got != want {
		t.Errorf("operator file omitting fragments did not produce trimmed output:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
	if strings.Contains(got, "Side note") {
		t.Errorf("omitted syncback note still appeared:\n%s", got)
	}
	if strings.Contains(got, "env syncback") {
		t.Errorf("omitted cmd_env_syncback still appeared:\n%s", got)
	}
}

// TestBuildSystemPromptOperatorFileAddsProse exercises the worked example
// from the spec: operator file slots in local instructions (the `/code` skill
// hint) around the framework-provided fragments.
func TestBuildSystemPromptOperatorFileAddsProse(t *testing.T) {
	t.Parallel()
	beadsDir := t.TempDir()
	tmpl := "# Ticket {{ticket_id}}: {{title}}\n\n" +
		"Execute the task described in description.md. Read it first.\n\n" +
		"## Finding code in other knime repos\n\n" +
		"If a class, file, or symbol referenced by this ticket is not present in the\n" +
		"repos already linked to this session, invoke the `/code` skill.\n"
	if err := os.WriteFile(filepath.Join(beadsDir, agent.AgentPromptFile), []byte(tmpl), 0o644); err != nil {
		t.Fatalf("write operator file: %v", err)
	}

	got, err := agent.BuildSystemPrompt(beadsDir, "bd-1", "x", agent.ModeAgent, nil, nil)
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	if !strings.Contains(got, "## Finding code in other knime repos") {
		t.Errorf("operator-added prose missing:\n%s", got)
	}
	if !strings.Contains(got, "invoke the `/code` skill.") {
		t.Errorf("operator-added prose body missing:\n%s", got)
	}
	if !strings.Contains(got, "# Ticket bd-1: x") {
		t.Errorf("framework-provided fragments did not expand alongside operator prose:\n%s", got)
	}
	if !strings.Contains(got, "Execute the task described in description.md. Read it first.") {
		t.Errorf("operator prose body missing:\n%s", got)
	}
}

// TestBuildSystemPromptSpecOperatorFile pins that the spec mode honors its
// own operator file (spec-prompt.md), not agent-prompt.md.
func TestBuildSystemPromptSpecOperatorFile(t *testing.T) {
	t.Parallel()
	beadsDir := t.TempDir()
	// Agent file present but unused for spec mode — proves wrong-mode files
	// don't leak in.
	if err := os.WriteFile(filepath.Join(beadsDir, agent.AgentPromptFile), []byte("WRONG MODE\n"), 0o644); err != nil {
		t.Fatalf("write agent file: %v", err)
	}
	specTmpl := "SPEC MODE\n# Ticket {{ticket_id}}: {{title}}\n"
	if err := os.WriteFile(filepath.Join(beadsDir, agent.SpecPromptFile), []byte(specTmpl), 0o644); err != nil {
		t.Fatalf("write spec file: %v", err)
	}

	got, err := agent.BuildSystemPrompt(beadsDir, "bd-1", "x", agent.ModeSpec, nil, nil)
	if err != nil {
		t.Fatalf("BuildSystemPrompt spec: %v", err)
	}
	if !strings.HasPrefix(got, "SPEC MODE\n# Ticket bd-1: x\n") {
		t.Errorf("spec mode did not pick spec-prompt.md:\n%s", got)
	}
	if strings.Contains(got, "WRONG MODE") {
		t.Errorf("spec mode picked agent-prompt.md by mistake:\n%s", got)
	}
}

// TestBuildSystemPromptUnknownFragmentErrors verifies that a typo in a
// fragment name fails loudly at launch instead of silently expanding to the
// empty string or falling back to the default template.
func TestBuildSystemPromptUnknownFragmentErrors(t *testing.T) {
	t.Parallel()
	beadsDir := t.TempDir()
	tmpl := "# Ticket {{ticket_id}}: {{title}}\n{{not_a_real_fragment}}\n"
	if err := os.WriteFile(filepath.Join(beadsDir, agent.AgentPromptFile), []byte(tmpl), 0o644); err != nil {
		t.Fatalf("write operator file: %v", err)
	}
	_, err := agent.BuildSystemPrompt(beadsDir, "bd-1", "x", agent.ModeAgent, nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown fragment, got nil")
	}
	if !strings.Contains(err.Error(), "not_a_real_fragment") {
		t.Errorf("error did not mention the unknown fragment name: %v", err)
	}
}

// TestBuildSystemPromptParseErrorSurfaced verifies that a malformed template
// (unclosed action) errors out at launch.
func TestBuildSystemPromptParseErrorSurfaced(t *testing.T) {
	t.Parallel()
	beadsDir := t.TempDir()
	// Unclosed action: `{{ticket_id` without a closing `}}`.
	tmpl := "{{ticket_id\n"
	if err := os.WriteFile(filepath.Join(beadsDir, agent.AgentPromptFile), []byte(tmpl), 0o644); err != nil {
		t.Fatalf("write operator file: %v", err)
	}
	_, err := agent.BuildSystemPrompt(beadsDir, "bd-1", "x", agent.ModeAgent, nil, nil)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error did not mention parse failure: %v", err)
	}
}

// TestBuildSystemPromptUnreadableFileErrors verifies that an unreadable
// operator file surfaces an error rather than silently falling back to the
// default — per the acceptance criteria.
func TestBuildSystemPromptUnreadableFileErrors(t *testing.T) {
	t.Parallel()
	beadsDir := t.TempDir()
	path := filepath.Join(beadsDir, agent.AgentPromptFile)
	if err := os.WriteFile(path, []byte("ok\n"), 0o644); err != nil {
		t.Fatalf("write operator file: %v", err)
	}
	// Make the file unreadable by removing read permission. Skip on root
	// (where chmod 000 still allows reads).
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })

	if _, err := os.ReadFile(path); err == nil {
		t.Skip("running as root or filesystem ignores mode bits; cannot exercise unreadable-file path")
	}
	_, err := agent.BuildSystemPrompt(beadsDir, "bd-1", "x", agent.ModeAgent, nil, nil)
	if err == nil {
		t.Fatal("expected error for unreadable operator file, got nil")
	}
	if !strings.Contains(err.Error(), "agent-prompt.md") {
		t.Errorf("error did not mention the offending file: %v", err)
	}
}

