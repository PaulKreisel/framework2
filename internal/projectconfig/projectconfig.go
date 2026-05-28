// Package projectconfig loads the optional per-project config that customizes
// worktree path and branch naming, and renders those templates against a
// bead's metadata.
//
// The config file lives at `<beadsDir>/project.yaml`. When absent, callers get
// a Config initialized with [Defaults] — preserving the historical behavior of
// `ticket dir worktree`.
//
// Schema:
//
//	worktree:
//	  branch: "{type-prefix}/{id}-{slug}"
//	  path:   "{bare-repo-dir}/{bare-repo-name}-{id}-{slug}"
//	type-prefix:
//	  feature: enh
//	  bug:     fix
//	  # unmapped types fall back to "todo"
//
// Placeholders supported in templates:
//
//	{id}            "<jira-id>-<bead-id>" when a jira-id is available
//	                (inherited from the nearest ancestor, including the bead
//	                itself), else just the bead-id
//	{bead-id}       bead id
//	{jira-id}       jira issue key inherited from the nearest ancestor with
//	                an external_ref pointing at /browse/<KEY>, or empty
//	{slug}          slug from bead metadata, possibly empty
//	{type}          raw bead issue_type ("feature", "task", "bug", ...)
//	{type-prefix}   bead type mapped through type-prefix (default: enh/fix/todo)
//	{bare-repo-dir}  filepath.Dir of the bare repo path
//	{bare-repo-name} filepath.Base of the bare repo, minus trailing ".git"
//
// Empty-substitution rule: when a placeholder evaluates to the empty string,
// a single `-` immediately preceding the placeholder is consumed. This keeps
// `{type-prefix}/{bead-id}-{slug}` rendering as `todo/ai-42` for legacy beads
// that have no slug, without forcing the template author to write an optional
// segment syntax.
package projectconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"framework2/internal/project"
	"framework2/internal/slug"
	"gopkg.in/yaml.v3"
)

// Filename is the basename of the per-project config inside beadsDir.
const Filename = project.ConfigFilename

// Config is the parsed per-project configuration.
type Config struct {
	Worktree     WorktreeConfig    `yaml:"worktree"`
	TypePrefixes map[string]string `yaml:"type-prefix"`
	// BareRepo is a template that resolves a GitHub `owner/repo` to an
	// absolute local bare-repo path. Placeholders: {owner}, {repo-name}.
	// A leading `~/` is expanded to the user's home directory.
	BareRepo string `yaml:"bare-repo"`
	// JiraTypes maps Jira issue type names (case-insensitive) to bead types,
	// layered on top of the hardcoded jiracli.MapType defaults.
	JiraTypes map[string]string `yaml:"jira-type"`
	// SetupScript is the path to an executable invoked once, immediately
	// after a bare repo is freshly cloned by the auto-clone path. Passed
	// the new bare repo's path as its sole argument. A leading `~/` is
	// expanded to the user's home directory. Empty means: no post-clone
	// step.
	SetupScript string `yaml:"setup-script"`
}

// WorktreeConfig holds the two templates that control `ticket dir worktree`.
type WorktreeConfig struct {
	Branch string `yaml:"branch"`
	Path   string `yaml:"path"`
}

// DefaultBranchTemplate uses {id} so the Jira issue key (inherited from any
// ancestor) is included whenever one is available — the prepare-commit-msg
// hook in repos like knime-core-ui needs that key in the branch name.
const DefaultBranchTemplate = "{type-prefix}/{id}-{slug}"

// DefaultPathTemplate mirrors DefaultBranchTemplate's use of {id}: sibling of
// the bare repo, named `<bare-basename-without-.git>-<id>[-<slug>]`.
const DefaultPathTemplate = "{bare-repo-dir}/{bare-repo-name}-{id}-{slug}"

// Defaults returns a Config populated with the built-in templates and an
// empty TypePrefix map (which means: fall through to slug.BranchPrefix).
func Defaults() Config {
	return Config{
		Worktree: WorktreeConfig{
			Branch: DefaultBranchTemplate,
			Path:   DefaultPathTemplate,
		},
	}
}

// Load reads `<beadsDir>/project.yaml` and returns the parsed config merged
// onto [Defaults]. A missing file is not an error — callers always get a
// usable Config. An empty / unset template field falls back to its default.
func Load(beadsDir string) (Config, error) {
	cfg := Defaults()
	path := project.Path(beadsDir, Filename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	var loaded Config
	if err := yaml.Unmarshal(data, &loaded); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if loaded.Worktree.Branch != "" {
		cfg.Worktree.Branch = loaded.Worktree.Branch
	}
	if loaded.Worktree.Path != "" {
		cfg.Worktree.Path = loaded.Worktree.Path
	}
	if len(loaded.TypePrefixes) > 0 {
		cfg.TypePrefixes = loaded.TypePrefixes
	}
	if loaded.BareRepo != "" {
		cfg.BareRepo = loaded.BareRepo
	}
	if len(loaded.JiraTypes) > 0 {
		cfg.JiraTypes = loaded.JiraTypes
	}
	if loaded.SetupScript != "" {
		cfg.SetupScript = loaded.SetupScript
	}
	return cfg, nil
}

// RenderBareRepo renders the configured bare-repo template against an
// (owner, repo) pair. Returns an error when no template is configured, when
// the template contains an unknown placeholder, or when home expansion fails.
func (c Config) RenderBareRepo(owner, repo string) (string, error) {
	if c.BareRepo == "" {
		return "", fmt.Errorf("project.yaml has no `bare-repo:` template")
	}
	rendered, err := substitute(c.BareRepo, map[string]string{
		"owner":     owner,
		"repo-name": repo,
	})
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rendered, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expand ~ in bare-repo: %w", err)
		}
		rendered = filepath.Join(home, rendered[2:])
	}
	return rendered, nil
}

// MapJiraType returns the bead type for a Jira issue-type name configured via
// jira-type in project.yaml. The lookup is case-insensitive on the configured
// keys. Returns ("", false) when no override applies, so callers can fall
// back to their default map.
func (c Config) MapJiraType(jiraType string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(jiraType))
	for k, v := range c.JiraTypes {
		if strings.ToLower(strings.TrimSpace(k)) == key {
			return v, true
		}
	}
	return "", false
}

// Vars is the input to template rendering. Fields map 1:1 to placeholders
// (modulo the {id} / {type-prefix} derivations performed by the renderer).
type Vars struct {
	BeadID       string
	JiraID       string
	Slug         string
	Type         string
	BareRepoPath string
	// Branch is the rendered branch name, populated by the caller before
	// rendering the path template so `{branch}` resolves there.
	Branch string
}

// TypePrefix returns the type-prefix configured for issueType, falling back
// to slug.BranchPrefix when there is no override. The lookup is
// case-insensitive on the configured keys.
func (c Config) TypePrefix(issueType string) string {
	key := strings.ToLower(strings.TrimSpace(issueType))
	for k, v := range c.TypePrefixes {
		if strings.ToLower(strings.TrimSpace(k)) == key {
			return v
		}
	}
	return slug.BranchPrefix(issueType)
}

// RenderBranch renders the branch-name template. If v.Slug would push the
// rendered branch past [slug.MaxBranchLen], the slug is shortened via
// [slug.Derive] to the per-branch budget first. This is the last line of
// defence against branches that violate GitHub's 70-char branch-name rule.
func (c Config) RenderBranch(v Vars) (string, error) {
	if v.Slug != "" {
		budget := c.BranchSlugBudget(v)
		if budget > 0 && len(v.Slug) > budget {
			v.Slug = slug.Derive(v.Slug, budget)
		}
	}
	return c.render(c.Worktree.Branch, v)
}

// BranchSlugBudget returns the maximum slug length such that rendering the
// branch template against v stays within [slug.MaxBranchLen] characters. The
// budget is probed by rendering the template with two short stand-in slugs
// and comparing the resulting lengths, so it works regardless of where (or
// how many times) `{slug}` appears in the template, and regardless of the
// `-` drop rule on empty substitutions.
//
// Returns [slug.MaxLen] when the template does not reference {slug} (any
// slug length is safe) or when the computed budget would exceed [slug.MaxLen]
// (nothing to gain by going higher). Returns 0 when even an empty slug would
// already overflow the budget.
func (c Config) BranchSlugBudget(v Vars) int {
	v1 := v
	v1.Slug = "x"
	r1, err := c.render(c.Worktree.Branch, v1)
	if err != nil {
		return slug.MaxLen
	}
	v2 := v
	v2.Slug = "xx"
	r2, err := c.render(c.Worktree.Branch, v2)
	if err != nil {
		return slug.MaxLen
	}
	slope := len(r2) - len(r1)
	if slope <= 0 {
		return slug.MaxLen
	}
	// len(r1) corresponds to a 1-char slug; each additional slug char adds
	// `slope` to the rendered length. Solve len(r1) + (S-1)*slope <= MaxBranchLen.
	budget := 1 + (slug.MaxBranchLen-len(r1))/slope
	if budget < 0 {
		return 0
	}
	if budget > slug.MaxLen {
		return slug.MaxLen
	}
	return budget
}

// RenderPath renders the worktree-path template. The result is absolute when
// the template starts with `{bare-repo-dir}` or a slash; relative results are
// returned as-is (the worktree call will then create them relative to cwd).
func (c Config) RenderPath(v Vars) (string, error) {
	return c.render(c.Worktree.Path, v)
}

func (c Config) render(tmpl string, v Vars) (string, error) {
	bareDir, bareName := splitBare(v.BareRepoPath)
	values := map[string]string{
		"id":             combinedID(v.JiraID, v.BeadID),
		"bead-id":        v.BeadID,
		"jira-id":        v.JiraID,
		"slug":           v.Slug,
		"type":           v.Type,
		"type-prefix":    c.TypePrefix(v.Type),
		"bare-repo-dir":  bareDir,
		"bare-repo-name": bareName,
		"branch":         v.Branch,
	}
	return substitute(tmpl, values)
}

// combinedID returns "<jiraID>-<beadID>" when jiraID is non-empty, else just
// beadID. The Jira id is always included alongside the bead id so prepare-
// commit-msg hooks that scan branch names for an issue key keep working even
// when a bead inherits its Jira id from an ancestor.
func combinedID(jiraID, beadID string) string {
	if jiraID == "" {
		return beadID
	}
	if beadID == "" {
		return jiraID
	}
	return jiraID + "-" + beadID
}

// substitute walks tmpl and replaces every `{name}` with values[name]. If a
// placeholder substitutes to the empty string, a single `-` immediately
// preceding the `{...}` is dropped. Unknown placeholders return an error so
// typos fail loudly instead of silently leaking into branch names.
func substitute(tmpl string, values map[string]string) (string, error) {
	var out strings.Builder
	out.Grow(len(tmpl))
	for i := 0; i < len(tmpl); {
		c := tmpl[i]
		if c != '{' {
			out.WriteByte(c)
			i++
			continue
		}
		end := strings.IndexByte(tmpl[i+1:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated `{` at offset %d in %q", i, tmpl)
		}
		name := tmpl[i+1 : i+1+end]
		val, ok := values[name]
		if !ok {
			return "", fmt.Errorf("unknown placeholder {%s} in %q", name, tmpl)
		}
		if val == "" {
			// Drop one preceding '-' so `foo-{slug}` becomes `foo` for
			// legacy beads, while `foo/{slug}` stays `foo/`.
			cur := out.String()
			if strings.HasSuffix(cur, "-") {
				out.Reset()
				out.WriteString(cur[:len(cur)-1])
			}
		} else {
			out.WriteString(val)
		}
		i += end + 2 // skip past `}`
	}
	return out.String(), nil
}

// splitBare returns (dir, basename-without-.git) for a bare repo path.
func splitBare(bare string) (string, string) {
	if bare == "" {
		return "", ""
	}
	dir := filepath.Dir(bare)
	base := strings.TrimSuffix(filepath.Base(bare), ".git")
	return dir, base
}

// JiraIDFromExternalRef extracts the issue key from a Jira browse URL such as
// `https://jira.example.com/browse/UIEXT-1234`. Returns "" when the URL does
// not contain `/browse/<key>`.
func JiraIDFromExternalRef(ref string) string {
	const marker = "/browse/"
	idx := strings.LastIndex(ref, marker)
	if idx < 0 {
		return ""
	}
	rest := ref[idx+len(marker):]
	if rest == "" {
		return ""
	}
	if slash := strings.IndexAny(rest, "/?#"); slash >= 0 {
		rest = rest[:slash]
	}
	return rest
}
