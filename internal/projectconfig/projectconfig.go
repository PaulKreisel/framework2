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
//	{id}            jira-id if present on the bead, else bead-id
//	{bead-id}       bead id
//	{jira-id}       jira issue key parsed from external_ref, or empty
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

	"framework2/internal/slug"
	"gopkg.in/yaml.v3"
)

// Filename is the basename of the per-project config inside beadsDir.
const Filename = "project.yaml"

// Config is the parsed per-project configuration.
type Config struct {
	Worktree     WorktreeConfig    `yaml:"worktree"`
	TypePrefixes map[string]string `yaml:"type-prefix"`
}

// WorktreeConfig holds the two templates that control `ticket dir worktree`.
type WorktreeConfig struct {
	Branch string `yaml:"branch"`
	Path   string `yaml:"path"`
}

// DefaultBranchTemplate matches the pre-config behavior of slug.Branch.
const DefaultBranchTemplate = "{type-prefix}/{bead-id}-{slug}"

// DefaultPathTemplate matches the pre-config behavior of the old internal
// worktreePath helper: sibling of the bare repo, named
// `<bare-basename-without-.git>-<bead-id>[-<slug>]`.
const DefaultPathTemplate = "{bare-repo-dir}/{bare-repo-name}-{bead-id}-{slug}"

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
	path := filepath.Join(beadsDir, Filename)
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
	return cfg, nil
}

// Vars is the input to template rendering. Fields map 1:1 to placeholders
// (modulo the {id} / {type-prefix} derivations performed by the renderer).
type Vars struct {
	BeadID       string
	JiraID       string
	Slug         string
	Type         string
	BareRepoPath string
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

// RenderBranch renders the branch-name template.
func (c Config) RenderBranch(v Vars) (string, error) {
	return c.render(c.Worktree.Branch, v)
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
		"id":             firstNonEmpty(v.JiraID, v.BeadID),
		"bead-id":        v.BeadID,
		"jira-id":        v.JiraID,
		"slug":           v.Slug,
		"type":           v.Type,
		"type-prefix":    c.TypePrefix(v.Type),
		"bare-repo-dir":  bareDir,
		"bare-repo-name": bareName,
	}
	return substitute(tmpl, values)
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

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
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
