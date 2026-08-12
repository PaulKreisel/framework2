package agent

import (
	_ "embed"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/template"

	"framework2/internal/dir"
	"framework2/internal/project"
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
	"ticket_id":                       func(c PromptContext) string { return c.TicketID },
	"title":                           func(c PromptContext) string { return c.Title },
	"cmd_create_followup_blocking":    renderCmdCreateFollowupBlocking,
	"cmd_create_followup_not_blocked": renderCmdCreateFollowupNotBlocked,
	"cmd_create_child":                renderCmdCreateChild,
	"cmd_dir_add":                     renderCmdDirAdd,
	"cmd_pde_setup":                   renderCmdPdeSetup,
	"cmd_env_syncback":                renderCmdEnvSyncback,
	"cmd_finish_ticket":               renderCmdFinishTicket,
	"list_of_linked_repos":            renderListOfLinkedRepos,
	"repos_parents":                   renderReposParents,
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
	path := project.Path(beadsDir, name)
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultPromptTemplate(mode), nil
		}
		return "", fmt.Errorf("read prompt file %s: %w", path, err)
	}
	return string(body), nil
}

// The cmd_* fragments return raw command syntax with no surrounding
// formatting. Templates wrap them as needed — inline backticks when used in
// running prose, a fenced code block when used as a standalone command.
const cmdCreateFollowupBlockingText = "this create followup <title> --description <text>"

func renderCmdCreateFollowupBlocking(PromptContext) string { return cmdCreateFollowupBlockingText }

const cmdCreateFollowupNotBlockedText = "this create followup --not-blocked <title> --description <text>"

func renderCmdCreateFollowupNotBlocked(PromptContext) string { return cmdCreateFollowupNotBlockedText }

const cmdCreateChildText = "this create child <title> --description <text>"

func renderCmdCreateChild(PromptContext) string { return cmdCreateChildText }

const cmdDirAddText = "this dir worktree <path-to-bare-repo> [<path-to-bare-repo> ...]"

func renderCmdDirAdd(PromptContext) string { return cmdDirAddText }

const cmdPdeSetupText = "this pde"

func renderCmdPdeSetup(PromptContext) string { return cmdPdeSetupText }

const cmdEnvSyncbackText = "this env syncback"

func renderCmdEnvSyncback(PromptContext) string { return cmdEnvSyncbackText }

const cmdFinishTicketText = "this finish"

func renderCmdFinishTicket(PromptContext) string { return cmdFinishTicketText }

// renderListOfLinkedRepos returns the body of the "Repos in this session"
// subsection — one bullet per bare-repo-backed dir linked to the current
// bead, including the worktree and bare paths. Heading is intentionally left
// to the template (so operators control its level). Returns "" when neither
// the current bead nor its ancestors have bare-repo-backed dirs (so the
// surrounding `{{if}}` collapses the whole block); returns the "No repos
// linked yet." stub when only the ancestors have repos (so the parents
// subsection below isn't orphaned under an empty heading).
func renderListOfLinkedRepos(c PromptContext) string {
	currentBares := map[string]struct{}{}
	var current []dir.Entry
	for _, e := range c.Dirs {
		if e.BareRepo == "" {
			continue
		}
		if _, dup := currentBares[e.BareRepo]; dup {
			continue
		}
		currentBares[e.BareRepo] = struct{}{}
		current = append(current, e)
	}
	parentRepos := bareReposFromExcluding(c.AncestorDirs, currentBares)
	if len(current) == 0 && len(parentRepos) == 0 {
		return ""
	}
	if len(current) == 0 {
		return "No repos linked yet.\n"
	}
	var b strings.Builder
	for _, e := range current {
		fmt.Fprintf(&b, "- %s — `%s` (bare: `%s`)\n", repoNameFromBarePath(e.BareRepo), e.Path, e.BareRepo)
	}
	return b.String()
}

// renderReposParents returns the bullet list of ancestor-only bare repos —
// no heading, no surrounding whitespace. Returns "" when there are no
// ancestor-only repos, so a surrounding `{{if repos_parents}}` block in the
// template collapses cleanly. The heading and the "add via" command reference
// live in the operator templates so operators can reword them.
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
	for _, r := range parentRepos {
		fmt.Fprintf(&b, "- %s — `%s`\n", r.name, r.bare)
	}
	return b.String()
}
