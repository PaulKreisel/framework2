package projectconfig_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/projectconfig"
)

func TestLoadDefaultsWhenMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg, err := projectconfig.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Worktree.Branch != projectconfig.DefaultBranchTemplate {
		t.Errorf("branch=%q want default", cfg.Worktree.Branch)
	}
	if cfg.Worktree.Path != projectconfig.DefaultPathTemplate {
		t.Errorf("path=%q want default", cfg.Worktree.Path)
	}
	if len(cfg.TypePrefixes) != 0 {
		t.Errorf("type-prefixes=%v want empty", cfg.TypePrefixes)
	}
}

func TestLoadFromFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	yaml := `
worktree:
  branch: "{type-prefix}/{id}-{slug}"
  path: "{bare-repo-dir}/{bare-repo-name}/branches/{type-prefix}/{id}-{slug}"
type-prefix:
  feature: enh
  task: enh
  bug: fix
`
	if err := os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := projectconfig.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Worktree.Branch != "{type-prefix}/{id}-{slug}" {
		t.Errorf("branch=%q", cfg.Worktree.Branch)
	}
	if cfg.TypePrefixes["task"] != "enh" {
		t.Errorf("task prefix=%q want enh", cfg.TypePrefixes["task"])
	}
}

func TestLoadPartialFallsBackToDefaults(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	yaml := `
worktree:
  branch: "custom/{bead-id}"
`
	if err := os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := projectconfig.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Worktree.Branch != "custom/{bead-id}" {
		t.Errorf("branch=%q", cfg.Worktree.Branch)
	}
	if cfg.Worktree.Path != projectconfig.DefaultPathTemplate {
		t.Errorf("path=%q want default", cfg.Worktree.Path)
	}
}

func TestRenderDefaultBranchAndPath(t *testing.T) {
	t.Parallel()
	cfg := projectconfig.Defaults()
	v := projectconfig.Vars{
		BeadID:       "ai-vrc.37",
		Slug:         "the-slug",
		Type:         "task",
		BareRepoPath: "/repos/foo.git",
	}
	branch, err := cfg.RenderBranch(v)
	if err != nil {
		t.Fatalf("RenderBranch: %v", err)
	}
	if branch != "todo/ai-vrc.37-the-slug" {
		t.Errorf("branch=%q", branch)
	}
	path, err := cfg.RenderPath(v)
	if err != nil {
		t.Fatalf("RenderPath: %v", err)
	}
	if path != "/repos/foo-ai-vrc.37-the-slug" {
		t.Errorf("path=%q", path)
	}
}

func TestRenderEmptySlugDropsPrecedingDash(t *testing.T) {
	t.Parallel()
	cfg := projectconfig.Defaults()
	v := projectconfig.Vars{
		BeadID:       "ai-42",
		Slug:         "",
		Type:         "bug",
		BareRepoPath: "/repos/foo.git",
	}
	branch, err := cfg.RenderBranch(v)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "fix/ai-42" {
		t.Errorf("branch=%q want fix/ai-42", branch)
	}
	path, err := cfg.RenderPath(v)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/repos/foo-ai-42" {
		t.Errorf("path=%q want /repos/foo-ai-42", path)
	}
}

func TestRenderIDPrefersJira(t *testing.T) {
	t.Parallel()
	cfg := projectconfig.Config{
		Worktree: projectconfig.WorktreeConfig{
			Branch: "{type-prefix}/{id}-{slug}",
			Path:   "/{id}",
		},
	}
	withJira := projectconfig.Vars{BeadID: "ai-42", JiraID: "UIEXT-1234", Slug: "foo", Type: "feature"}
	branch, err := cfg.RenderBranch(withJira)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "enh/UIEXT-1234-foo" {
		t.Errorf("branch=%q", branch)
	}
	noJira := projectconfig.Vars{BeadID: "ai-42", Slug: "foo", Type: "feature"}
	branch, err = cfg.RenderBranch(noJira)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "enh/ai-42-foo" {
		t.Errorf("branch (no jira)=%q", branch)
	}
}

func TestRenderTypePrefixOverride(t *testing.T) {
	t.Parallel()
	cfg := projectconfig.Config{
		Worktree:     projectconfig.WorktreeConfig{Branch: "{type-prefix}/x", Path: "/x"},
		TypePrefixes: map[string]string{"task": "enh"},
	}
	branch, err := cfg.RenderBranch(projectconfig.Vars{Type: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if branch != "enh/x" {
		t.Errorf("branch=%q want enh/x", branch)
	}
	// Unmapped types fall through to slug.BranchPrefix defaults.
	branch, err = cfg.RenderBranch(projectconfig.Vars{Type: "bug"})
	if err != nil {
		t.Fatal(err)
	}
	if branch != "fix/x" {
		t.Errorf("branch=%q want fix/x", branch)
	}
}

func TestRenderUnknownPlaceholder(t *testing.T) {
	t.Parallel()
	cfg := projectconfig.Config{Worktree: projectconfig.WorktreeConfig{Branch: "{nope}", Path: "/x"}}
	_, err := cfg.RenderBranch(projectconfig.Vars{})
	if err == nil || !strings.Contains(err.Error(), "unknown placeholder") {
		t.Errorf("err=%v want unknown placeholder", err)
	}
}

func TestRenderUnterminatedBrace(t *testing.T) {
	t.Parallel()
	cfg := projectconfig.Config{Worktree: projectconfig.WorktreeConfig{Branch: "{slug", Path: "/x"}}
	_, err := cfg.RenderBranch(projectconfig.Vars{})
	if err == nil || !strings.Contains(err.Error(), "unterminated") {
		t.Errorf("err=%v want unterminated", err)
	}
}

func TestJiraIDFromExternalRef(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://jira.example.com/browse/UIEXT-1234":         "UIEXT-1234",
		"https://jira.example.com/browse/UIEXT-1234?foo=bar": "UIEXT-1234",
		"http://x.test/sub/browse/AB-1/comments":             "AB-1",
		"":                                                   "",
		"https://example.com/no-browse":                      "",
		"https://x.test/browse/":                             "",
	}
	for in, want := range cases {
		if got := projectconfig.JiraIDFromExternalRef(in); got != want {
			t.Errorf("JiraIDFromExternalRef(%q)=%q want %q", in, got, want)
		}
	}
}
