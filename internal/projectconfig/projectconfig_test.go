// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package projectconfig_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/projectconfig"
	"framework2/internal/slug"
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

func TestLoadDevserverBlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg, err := projectconfig.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Devserver.Configured() {
		t.Fatal("empty devserver block must not be configured")
	}
	if cfg.Devserver.HubsFile != projectconfig.DefaultHubsFile || cfg.Devserver.PortBase != projectconfig.DefaultPortBase || cfg.Devserver.EnvDir != projectconfig.DefaultEnvDir {
		t.Fatalf("defaults: %+v", cfg.Devserver)
	}
	yaml := `
devserver:
  hub: dev_green
  workflow-folder: "/Users/T/S/G"
  frontend-repo: ~/repos/knime-ui.git
  port-base: 40000
`
	if err := os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err = projectconfig.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Devserver.Missing(); len(got) != 1 || got[0] != "frontend-dir" {
		t.Fatalf("missing=%v", got)
	}
	if cfg.Devserver.PortBase != 40000 || cfg.Devserver.HubsFile != projectconfig.DefaultHubsFile {
		t.Fatalf("partial: %+v", cfg.Devserver)
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

func TestRenderIDCombinesJiraAndBead(t *testing.T) {
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
	if branch != "enh/UIEXT-1234-ai-42-foo" {
		t.Errorf("branch=%q", branch)
	}
	path, err := cfg.RenderPath(withJira)
	if err != nil {
		t.Fatal(err)
	}
	if path != "/UIEXT-1234-ai-42" {
		t.Errorf("path=%q", path)
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

func TestRenderBareRepoHomeExpansion(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	cfg := projectconfig.Config{BareRepo: "~/knime/repos/{repo-name}.git"}
	got, err := cfg.RenderBareRepo("knime", "knime-core-ui")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "knime/repos/knime-core-ui.git")
	if got != want {
		t.Errorf("got=%q want=%q", got, want)
	}
}

func TestRenderBareRepoOwnerPlaceholder(t *testing.T) {
	t.Parallel()
	cfg := projectconfig.Config{BareRepo: "/repos/{owner}/{repo-name}.git"}
	got, err := cfg.RenderBareRepo("knime", "x")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/repos/knime/x.git" {
		t.Errorf("got=%q", got)
	}
}

func TestRenderBareRepoMissingTemplate(t *testing.T) {
	t.Parallel()
	cfg := projectconfig.Defaults()
	_, err := cfg.RenderBareRepo("o", "r")
	if err == nil || !strings.Contains(err.Error(), "bare-repo") {
		t.Errorf("err=%v want bare-repo missing error", err)
	}
}

func TestLoadBareRepoAndJiraType(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	yaml := `
bare-repo: "~/knime/repos/{repo-name}.git"
jira-type:
  New node: feature
`
	if err := os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := projectconfig.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BareRepo != "~/knime/repos/{repo-name}.git" {
		t.Errorf("bare-repo=%q", cfg.BareRepo)
	}
	got, ok := cfg.MapJiraType("New node")
	if !ok || got != "feature" {
		t.Errorf("MapJiraType(New node)=%q,%v want feature,true", got, ok)
	}
	// Case-insensitive lookup matches the TypePrefix convention.
	if got, ok := cfg.MapJiraType("new NODE"); !ok || got != "feature" {
		t.Errorf("MapJiraType(case)=%q,%v", got, ok)
	}
	if _, ok := cfg.MapJiraType("Bug"); ok {
		t.Errorf("Bug should not be overridden")
	}
}

func TestLoadSetupScript(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	yaml := `setup-script: ~/knime/.beads/setup-repo.sh`
	if err := os.WriteFile(filepath.Join(dir, "project.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := projectconfig.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SetupScript != "~/knime/.beads/setup-repo.sh" {
		t.Errorf("setup-script=%q", cfg.SetupScript)
	}
}

func TestRenderPathWithBranchPlaceholder(t *testing.T) {
	t.Parallel()
	cfg := projectconfig.Config{
		Worktree: projectconfig.WorktreeConfig{
			Branch: "{type-prefix}/{bead-id}",
			Path:   "{bare-repo-dir}/{bare-repo-name}.git/branches/{branch}",
		},
	}
	v := projectconfig.Vars{BeadID: "ai-42", Type: "bug", BareRepoPath: "/repos/foo.git", Branch: "fix/ai-42"}
	got, err := cfg.RenderPath(v)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/repos/foo.git/branches/fix/ai-42" {
		t.Errorf("got=%q", got)
	}
}

func TestBranchSlugBudgetDefaultTemplate(t *testing.T) {
	t.Parallel()
	cfg := projectconfig.Defaults()
	// Short bead-id leaves plenty of room — budget caps at slug.MaxLen.
	if got := cfg.BranchSlugBudget(projectconfig.Vars{BeadID: "ai-42", Type: "task"}); got != slug.MaxLen {
		t.Errorf("short bead-id: budget=%d want %d", got, slug.MaxLen)
	}
	// Even with a long jira-prefixed id ("todo/SLEEK-233-kn-2lz-" = 22 chars
	// of overhead, leaving 70-22 = 48 chars for the slug), the budget is
	// still bounded by slug.MaxLen.
	v := projectconfig.Vars{BeadID: "kn-2lz", JiraID: "SLEEK-233", Type: "task"}
	if got := cfg.BranchSlugBudget(v); got != slug.MaxLen {
		t.Errorf("long id: budget=%d want %d", got, slug.MaxLen)
	}
	// When the overhead alone leaves less than MaxLen for the slug, the
	// budget shrinks to fit MaxBranchLen exactly.
	tight := projectconfig.Config{
		Worktree: projectconfig.WorktreeConfig{
			Branch: strings.Repeat("x", 50) + "-{slug}",
			Path:   "/x",
		},
	}
	if got := tight.BranchSlugBudget(projectconfig.Vars{}); got != 19 {
		t.Errorf("tight template: budget=%d want 19", got)
	}
}

func TestBranchSlugBudgetNoSlugInTemplate(t *testing.T) {
	t.Parallel()
	cfg := projectconfig.Config{Worktree: projectconfig.WorktreeConfig{Branch: "{type-prefix}/{id}", Path: "/x"}}
	if got := cfg.BranchSlugBudget(projectconfig.Vars{BeadID: "ai-42", Type: "task"}); got != slug.MaxLen {
		t.Errorf("no-slug template: budget=%d want %d", got, slug.MaxLen)
	}
}

func TestRenderBranchTruncatesOversizedSlug(t *testing.T) {
	t.Parallel()
	cfg := projectconfig.Defaults()
	v := projectconfig.Vars{
		BeadID: "kn-2lz",
		JiraID: "SLEEK-233",
		Slug:   "remove-possibility-to-set-the-space-name-per-flow",
		Type:   "task",
	}
	branch, err := cfg.RenderBranch(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(branch) > slug.MaxBranchLen {
		t.Errorf("branch=%q len=%d exceeds %d", branch, len(branch), slug.MaxBranchLen)
	}
	if !strings.HasPrefix(branch, "todo/SLEEK-233-kn-2lz-") {
		t.Errorf("branch=%q missing expected prefix", branch)
	}
}

func TestRenderBranchLeavesShortSlugAlone(t *testing.T) {
	t.Parallel()
	cfg := projectconfig.Defaults()
	v := projectconfig.Vars{BeadID: "ai-42", Slug: "short-slug", Type: "task"}
	branch, err := cfg.RenderBranch(v)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "todo/ai-42-short-slug" {
		t.Errorf("branch=%q want todo/ai-42-short-slug", branch)
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
