package agent

import (
	_ "embed"
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
// as `{{name}}`. The set is intentionally minimal: dynamic values, one
// fragment per duplicated command line, and data-backed computed content.
// Static prose lives in the default templates so operators can reword it
// without losing framework-maintained command syntax.
var fragments = map[string]fragmentRenderer{
	"ticket_id":           func(c PromptContext) string { return c.TicketID },
	"title":               func(c PromptContext) string { return c.Title },
	"cmd_create_followup": renderCmdCreateFollowup,
	"cmd_create_child":    renderCmdCreateChild,
	"cmd_dir_add":         renderCmdDirAdd,
	"cmd_env_syncback":    renderCmdEnvSyncback,
	"repos_current":       renderReposCurrent,
	"repos_parents":       renderReposParents,
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
// no <beadsDir>/agent-prompt.md is present.
//
//go:embed defaults/agent-prompt.md
var defaultAgentTemplate string

// defaultSpecTemplate is the implicit operator file used in spec mode when no
// <beadsDir>/spec-prompt.md is present.
//
//go:embed defaults/spec-prompt.md
var defaultSpecTemplate string

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

const cmdCreateFollowupText = "`this create followup <title>` — sibling of this ticket, blocked by it\n" +
	"`this create followup --not-blocked <title>` — sibling of this ticket, not blocked"

func renderCmdCreateFollowup(PromptContext) string { return cmdCreateFollowupText }

const cmdCreateChildText = "`this create child <title>`"

func renderCmdCreateChild(PromptContext) string { return cmdCreateChildText }

const cmdDirAddText = "`this dir worktree <bare-repo>` (or `this dir link <path>`). The wrapper auto-relaunches claude so the new dir is in scope."

func renderCmdDirAdd(PromptContext) string { return cmdDirAddText }

const cmdEnvSyncbackText = "`this env syncback`"

func renderCmdEnvSyncback(PromptContext) string { return cmdEnvSyncbackText }

// renderReposCurrent returns the "Repos in this session" subsection — the
// heading + the one-line summary of repos linked to the current bead — with a
// leading `\n` so it joins cleanly to the line above. Returns "" when there
// are no current repos AND no ancestor repos either (whole block disappears);
// returns the "No repos linked yet." variant only when there are ancestor
// repos to display below (so the parents subsection isn't orphaned).
func renderReposCurrent(c PromptContext) string {
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
	return b.String()
}

// renderReposParents returns the "Parent tickets touch the following repos"
// subsection (heading + bullet list of ancestor-only bare repos), with a
// leading `\n` so it joins cleanly to the subsection above. Returns "" when
// there are no ancestor-only repos.
func renderReposParents(c PromptContext) string {
	currentRepos := bareReposFrom(c.Dirs)
	currentBares := make(map[string]struct{}, len(currentRepos))
	for _, r := range currentRepos {
		currentBares[r.bare] = struct{}{}
	}
	parentRepos := bareReposFromExcluding(c.AncestorDirs, currentBares)
	if len(parentRepos) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nParent tickets touch the following repos (add with `this dir worktree <path>`):\n\n")
	for _, r := range parentRepos {
		fmt.Fprintf(&b, "- %s — `%s`\n", r.name, r.bare)
	}
	return b.String()
}
