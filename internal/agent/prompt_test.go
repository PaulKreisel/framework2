package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/dir"
)

// TestBuildSystemPromptDefaultByteIdentical pins the output of the default
// (no-operator-file) path against a literal expected string. This is the
// regression test for the design goal that adding the template machinery
// produces byte-identical output to the prior hard-coded prompt.
func TestBuildSystemPromptDefaultByteIdentical(t *testing.T) {
	t.Parallel()

	// Empty beadsDir -> no operator-file lookup -> built-in default template.
	// No dirs/ancestors -> no `Repos in this session` block.
	wantAgent := "Ticket bd-1 — do the thing\n" +
		"\n" +
		"Execute the task described in description.md. Read it first.\n" +
		"\n" +
		"## Delegating work for later\n" +
		"`this create followup <title>` — sibling of this ticket, blocked by it\n" +
		"`this create followup --not-blocked <title>` — sibling of this ticket, not blocked\n" +
		"\n" +
		"## Adding a directory to this session\n" +
		"`this dir worktree <bare-repo>` (or `this dir link <path>`). The wrapper auto-relaunches claude so the new dir is in scope.\n" +
		"\n" +
		"(Side note: if you intentionally edit description.md, run `this env syncback` to persist it back to the bead.)\n"

	gotAgent, err := agent.BuildSystemPrompt("", "bd-1", "do the thing", agent.ModeAgent, nil, nil)
	if err != nil {
		t.Fatalf("BuildSystemPrompt agent: %v", err)
	}
	if gotAgent != wantAgent {
		t.Errorf("agent-mode default prompt drift:\n--- got ---\n%q\n--- want ---\n%q", gotAgent, wantAgent)
	}

	wantSpec := "Ticket bd-1 — do the thing\n" +
		"\n" +
		"You are spec'ing this ticket, not coding. Do not edit source files.\n" +
		"Read description.md first.\n" +
		"\n" +
		"## Update the description of this ticket\n" +
		"* Modify the description.md file\n" +
		"* Run `this env syncback`\n" +
		"\n" +
		"## Delegating work\n" +
		"### Within this ticket: create a child\n" +
		"`this create child <title>`\n" +
		"\n" +
		"### For later\n" +
		"`this create followup <title>` — sibling of this ticket, blocked by it\n" +
		"`this create followup --not-blocked <title>` — sibling of this ticket, not blocked\n"

	gotSpec, err := agent.BuildSystemPrompt("", "bd-1", "do the thing", agent.ModeSpec, nil, nil)
	if err != nil {
		t.Fatalf("BuildSystemPrompt spec: %v", err)
	}
	if gotSpec != wantSpec {
		t.Errorf("spec-mode default prompt drift:\n--- got ---\n%q\n--- want ---\n%q", gotSpec, wantSpec)
	}
}

// TestBuildSystemPromptDefaultByteIdenticalWithRepos pins the with-repos
// variant — same default template, dirs/ancestors populate the `{{repos}}`
// fragment with a leading blank line.
func TestBuildSystemPromptDefaultByteIdenticalWithRepos(t *testing.T) {
	t.Parallel()
	current := []dir.Entry{{Path: "/wt/framework2", BareRepo: "/bare/framework2.git"}}
	ancestors := []dir.Entry{{Path: "/wt/other", BareRepo: "/bare/other.git"}}

	got, err := agent.BuildSystemPrompt("", "bd-1", "x", agent.ModeAgent, current, ancestors)
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	if !strings.HasSuffix(got, "## Repos in this session\n\nRepos linked into this session: framework2.\n\nParent tickets touch the following repos (add with `this dir worktree <path>`):\n\n- other — `/bare/other.git`\n") {
		t.Errorf("repos block did not render at the tail as expected:\n%q", got)
	}
	// The repos block must be preceded by the side-note line + one blank.
	if !strings.Contains(got, "back to the bead.)\n\n## Repos in this session\n") {
		t.Errorf("repos block not joined to syncback note with a single blank line:\n%q", got)
	}
}

// TestBuildSystemPromptOperatorFileOmitsFragment exercises the design point
// that an operator file can drop a fragment by simply not referencing it.
func TestBuildSystemPromptOperatorFileOmitsFragment(t *testing.T) {
	t.Parallel()
	beadsDir := t.TempDir()
	// Operator file with no `{{syncback_note}}` and no `{{repos}}`.
	tmpl := "{{session_header}}\n\n{{agent_intro}}\n"
	if err := os.WriteFile(filepath.Join(beadsDir, agent.AgentPromptFile), []byte(tmpl), 0o644); err != nil {
		t.Fatalf("write operator file: %v", err)
	}

	got, err := agent.BuildSystemPrompt(beadsDir, "bd-1", "title", agent.ModeAgent, nil, nil)
	if err != nil {
		t.Fatalf("BuildSystemPrompt: %v", err)
	}
	want := "Ticket bd-1 — title\n\nExecute the task described in description.md. Read it first.\n"
	if got != want {
		t.Errorf("operator file omitting fragments did not produce trimmed output:\n--- got ---\n%q\n--- want ---\n%q", got, want)
	}
	if strings.Contains(got, "Side note") {
		t.Errorf("omitted syncback_note still appeared:\n%s", got)
	}
}

// TestBuildSystemPromptOperatorFileAddsProse exercises the worked example
// from the spec: operator file slots in local instructions (the `/code` skill
// hint) around the framework-provided fragments.
func TestBuildSystemPromptOperatorFileAddsProse(t *testing.T) {
	t.Parallel()
	beadsDir := t.TempDir()
	tmpl := "{{session_header}}\n\n" +
		"{{agent_intro}}\n\n" +
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
	if !strings.Contains(got, "Execute the task described in description.md. Read it first.") {
		t.Errorf("framework-provided fragment did not expand alongside operator prose:\n%s", got)
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
	specTmpl := "SPEC MODE\n{{session_header}}\n"
	if err := os.WriteFile(filepath.Join(beadsDir, agent.SpecPromptFile), []byte(specTmpl), 0o644); err != nil {
		t.Fatalf("write spec file: %v", err)
	}

	got, err := agent.BuildSystemPrompt(beadsDir, "bd-1", "x", agent.ModeSpec, nil, nil)
	if err != nil {
		t.Fatalf("BuildSystemPrompt spec: %v", err)
	}
	if !strings.HasPrefix(got, "SPEC MODE\nTicket bd-1 — x\n") {
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
	tmpl := "{{session_header}}\n{{not_a_real_fragment}}\n"
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
	// Unclosed action: `{{session_header` without a closing `}}`.
	tmpl := "{{session_header\n"
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

// TestFragmentNamesCoversDefault asserts that every fragment referenced by
// the built-in default templates is exposed via FragmentNames — i.e. the
// public fragment list is at least as expressive as the default.
func TestFragmentNamesCoversDefault(t *testing.T) {
	t.Parallel()
	registered := make(map[string]struct{}, len(agent.FragmentNames()))
	for _, n := range agent.FragmentNames() {
		registered[n] = struct{}{}
	}
	for _, mode := range []agent.Mode{agent.ModeAgent, agent.ModeSpec} {
		tmpl := agent.DefaultPromptTemplate(mode)
		// Crude scan: anything between `{{` and `}}` is treated as a single
		// fragment reference. Good enough — the default templates only use
		// bare `{{name}}` invocations.
		rest := tmpl
		for {
			i := strings.Index(rest, "{{")
			if i < 0 {
				break
			}
			rest = rest[i+2:]
			j := strings.Index(rest, "}}")
			if j < 0 {
				t.Fatalf("malformed default template (mode=%v): unclosed action in %q", mode, tmpl)
			}
			name := strings.TrimSpace(rest[:j])
			rest = rest[j+2:]
			if _, ok := registered[name]; !ok {
				t.Errorf("default template (mode=%v) references unregistered fragment %q", mode, name)
			}
		}
	}
}
