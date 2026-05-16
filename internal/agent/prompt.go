package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"framework2/internal/dir"
)

// PromptContext is the data available to operator templates and to
// framework-provided fragments. The same context is passed to every fragment;
// fragments pick whichever fields they need.
type PromptContext struct {
	TicketID     string
	Title        string
	Mode         Mode
	Dirs         []dir.Entry
	AncestorDirs []dir.Entry
	BeadsDir     string
}

// AgentPromptFile is the operator-owned coding-mode prompt file, relative to
// the beads directory.
const AgentPromptFile = "agent-prompt.md"

// SpecPromptFile is the operator-owned spec-mode prompt file, relative to the
// beads directory.
const SpecPromptFile = "spec-prompt.md"

// fragmentRenderer renders a single named fragment against a PromptContext.
type fragmentRenderer func(PromptContext) string

// fragments is the registry of framework-provided template fragments. Each
// entry can be referenced from an operator template (or the built-in default)
// as `{{name}}`.
var fragments = map[string]fragmentRenderer{
	"ticket_id":               func(c PromptContext) string { return c.TicketID },
	"title":                   func(c PromptContext) string { return c.Title },
	"session_header":          renderSessionHeader,
	"repos":                   renderRepos,
	"agent_intro":             renderAgentIntro,
	"agent_followup":          renderAgentFollowup,
	"agent_dir_session":       renderAgentDirSession,
	"syncback_note":           renderSyncbackNote,
	"spec_intro":              renderSpecIntro,
	"spec_update_description": renderSpecUpdateDescription,
	"spec_delegation":         renderSpecDelegation,
}

// FragmentNames returns the registered fragment names in alphabetical order.
// Exposed for `--list-prompt-fragments` and tests.
func FragmentNames() []string {
	names := make([]string, 0, len(fragments))
	for name := range fragments {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DefaultPromptTemplate returns the framework-shipped default template for the
// given mode. Used when no operator file is present and surfaced via
// `--print-default-prompt` so operators have a starting point to copy.
func DefaultPromptTemplate(mode Mode) string {
	if mode == ModeSpec {
		return defaultSpecTemplate
	}
	return defaultAgentTemplate
}

// defaultAgentTemplate is the implicit operator file used in coding mode when
// no <beadsDir>/agent-prompt.md is present. It is constructed so that the
// rendered output is byte-identical to the prior hard-coded BuildSystemPrompt.
const defaultAgentTemplate = `{{session_header}}

{{agent_intro}}

{{agent_followup}}

{{agent_dir_session}}

{{syncback_note}}
{{repos}}`

// defaultSpecTemplate is the implicit operator file used in spec mode when no
// <beadsDir>/spec-prompt.md is present. Byte-identical output to the prior
// hard-coded spec-mode prompt.
const defaultSpecTemplate = `{{session_header}}

{{spec_intro}}

{{spec_update_description}}

{{spec_delegation}}
{{repos}}`

// RenderPrompt expands a template against ctx, resolving `{{fragment_name}}`
// tokens via the registered fragments. Returns an error for unknown fragment
// names, template parse errors, or execution errors. Used by BuildSystemPrompt
// for both the operator file and the built-in default.
func RenderPrompt(ctx PromptContext, templateText string) (string, error) {
	funcs := template.FuncMap{}
	for name, r := range fragments {
		funcs[name] = func() string { return r(ctx) }
	}
	t, err := template.New("prompt").Funcs(funcs).Option("missingkey=error").Parse(templateText)
	if err != nil {
		return "", fmt.Errorf("parse prompt template: %w", err)
	}
	var b strings.Builder
	if err := t.Execute(&b, ctx); err != nil {
		return "", fmt.Errorf("execute prompt template: %w", err)
	}
	return b.String(), nil
}

// LoadPromptTemplate returns the prompt template for the given mode. If an
// operator file exists at <beadsDir>/<file>, its contents are returned; if
// the file is absent (and beadsDir is non-empty), the built-in default is
// returned. A read error other than "not exist" is surfaced.
func LoadPromptTemplate(beadsDir string, mode Mode) (string, error) {
	if beadsDir == "" {
		return DefaultPromptTemplate(mode), nil
	}
	name := AgentPromptFile
	if mode == ModeSpec {
		name = SpecPromptFile
	}
	path := filepath.Join(beadsDir, name)
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultPromptTemplate(mode), nil
		}
		return "", fmt.Errorf("read prompt file %s: %w", path, err)
	}
	return string(body), nil
}

// renderSessionHeader returns the one-line "Ticket <id> — <title>" header,
// without a trailing newline so the template controls surrounding whitespace.
func renderSessionHeader(c PromptContext) string {
	return fmt.Sprintf("Ticket %s — %s", c.TicketID, c.Title)
}

const agentIntroText = "Execute the task described in description.md. Read it first."

func renderAgentIntro(PromptContext) string { return agentIntroText }

const agentFollowupText = "## Delegating work for later\n" +
	"`this create followup <title>` — sibling of this ticket, blocked by it\n" +
	"`this create followup --not-blocked <title>` — sibling of this ticket, not blocked"

func renderAgentFollowup(PromptContext) string { return agentFollowupText }

const agentDirSessionText = "## Adding a directory to this session\n" +
	"`this dir worktree <bare-repo>` (or `this dir link <path>`). The wrapper auto-relaunches claude so the new dir is in scope."

func renderAgentDirSession(PromptContext) string { return agentDirSessionText }

const syncbackNoteText = "(Side note: if you intentionally edit description.md, run `this env syncback` to persist it back to the bead.)"

func renderSyncbackNote(PromptContext) string { return syncbackNoteText }

const specIntroText = "You are spec'ing this ticket, not coding. Do not edit source files.\n" +
	"Read description.md first."

func renderSpecIntro(PromptContext) string { return specIntroText }

const specUpdateDescriptionText = "## Update the description of this ticket\n" +
	"* Modify the description.md file\n" +
	"* Run `this env syncback`"

func renderSpecUpdateDescription(PromptContext) string { return specUpdateDescriptionText }

const specDelegationText = "## Delegating work\n" +
	"### Within this ticket: create a child\n" +
	"`this create child <title>`\n" +
	"\n" +
	"### For later\n" +
	"`this create followup <title>` — sibling of this ticket, blocked by it\n" +
	"`this create followup --not-blocked <title>` — sibling of this ticket, not blocked"

func renderSpecDelegation(PromptContext) string { return specDelegationText }

// renderRepos returns the "Repos in this session" block, including a leading
// newline (so it appends cleanly to a preceding line) and a trailing newline
// on the last bullet. Returns the empty string when neither the current bead
// nor any ancestor has a bare-repo-backed dir — in that case the template
// produces no repos block at all.
func renderRepos(c PromptContext) string {
	currentRepos := bareReposFrom(c.Dirs)
	currentBares := make(map[string]struct{}, len(currentRepos))
	for _, r := range currentRepos {
		currentBares[r.bare] = struct{}{}
	}
	parentRepos := bareReposFromExcluding(c.AncestorDirs, currentBares)
	if len(currentRepos) == 0 && len(parentRepos) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Repos in this session\n\n")
	if len(currentRepos) == 0 {
		b.WriteString("No repos linked yet.\n")
	} else {
		names := make([]string, len(currentRepos))
		for i, r := range currentRepos {
			names[i] = r.name
		}
		fmt.Fprintf(&b, "Repos linked into this session: %s.\n", strings.Join(names, ", "))
	}
	if len(parentRepos) > 0 {
		b.WriteString("\nParent tickets touch the following repos (add with `this dir worktree <path>`):\n\n")
		for _, r := range parentRepos {
			fmt.Fprintf(&b, "- %s — `%s`\n", r.name, r.bare)
		}
	}
	return b.String()
}
