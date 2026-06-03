package dir_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"framework2/internal/dir"
	"framework2/internal/testutil"
)

func TestDirWorktreeRootBeadOnMain(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "wt-root-main")
	bare := bareRepo(t, "main")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare)
	if code != 0 {
		t.Fatalf("worktree exit=%d stderr=%q", code, stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v, want 1", entries)
	}
	got := entries[0]
	// testutil.CreateBead drives `bd create` directly (no slug metadata), so
	// the worktree falls back to the slug-less branch shape "<prefix>/<id>".
	// Default issue type is "task" → "todo".
	wantBranch := "todo/" + ticket
	wantPath := filepath.Join(filepath.Dir(bare), strings.TrimSuffix(filepath.Base(bare), ".git")+"-"+ticket)
	if got.Path != wantPath {
		t.Errorf("path=%q want %q", got.Path, wantPath)
	}
	if got.Branch != wantBranch {
		t.Errorf("branch=%q want %q", got.Branch, wantBranch)
	}
	if got.BareRepo != bare {
		t.Errorf("bareRepo=%q want %q", got.BareRepo, bare)
	}
	if !worktreeOnBranch(t, got.Path, wantBranch) {
		t.Errorf("worktree at %s is not on branch %s", got.Path, wantBranch)
	}
	if sha := refSHA(t, got.Path, "HEAD"); sha != bareBranchSHA(t, bare, "main") {
		t.Errorf("worktree HEAD %q != bare main %q (root should branch off main)", sha, bareBranchSHA(t, bare, "main"))
	}
}

func TestDirWorktreeRootBeadOnMaster(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "wt-root-master")
	bare := bareRepo(t, "master")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare)
	if code != 0 {
		t.Fatalf("worktree exit=%d stderr=%q", code, stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v, want 1", entries)
	}
	if sha := refSHA(t, entries[0].Path, "HEAD"); sha != bareBranchSHA(t, bare, "master") {
		t.Errorf("worktree HEAD %q != bare master %q", sha, bareBranchSHA(t, bare, "master"))
	}
}

func TestDirWorktreeCascadesToParent(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "wt-cascade-parent")
	child := testutil.CreateChildBead(t, beadsDir, "wt-cascade-child", parent)
	bare := bareRepo(t, "main")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", child, bare)
	if code != 0 {
		t.Fatalf("worktree exit=%d stderr=%q", code, stderr)
	}

	parentEntries := listEntries(t, beadsDir, parent)
	if len(parentEntries) != 1 {
		t.Fatalf("parent entries=%+v, want 1 (cascade should have created parent's worktree first)", parentEntries)
	}
	wantParentBranch := "todo/" + parent
	if parentEntries[0].Branch != wantParentBranch {
		t.Errorf("parent branch=%q want %q", parentEntries[0].Branch, wantParentBranch)
	}
	if sha := refSHA(t, parentEntries[0].Path, "HEAD"); sha != bareBranchSHA(t, bare, "main") {
		t.Errorf("parent worktree HEAD %q != bare main %q", sha, bareBranchSHA(t, bare, "main"))
	}

	childEntries := listEntries(t, beadsDir, child)
	if len(childEntries) != 1 {
		t.Fatalf("child entries=%+v, want 1", childEntries)
	}
	wantChildBranch := "todo/" + child
	if childEntries[0].Branch != wantChildBranch {
		t.Errorf("child branch=%q want %q", childEntries[0].Branch, wantChildBranch)
	}
	if sha := refSHA(t, childEntries[0].Path, "HEAD"); sha != refSHA(t, parentEntries[0].Path, "HEAD") {
		t.Errorf("child HEAD %q != parent HEAD %q (child should branch off parent)", sha, refSHA(t, parentEntries[0].Path, "HEAD"))
	}
}

func TestDirWorktreeInheritsAncestorJiraID(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "wt-jira-parent")
	if out, err := testutil.BdCmd(beadsDir, "update", parent,
		"--external-ref", "https://jira.example.com/browse/UIEXT-9000").CombinedOutput(); err != nil {
		t.Fatalf("bd update --external-ref: %v: %s", err, out)
	}
	child := testutil.CreateChildBead(t, beadsDir, "wt-jira-child", parent)
	bare := bareRepo(t, "main")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", child, bare)
	if code != 0 {
		t.Fatalf("worktree exit=%d stderr=%q", code, stderr)
	}

	parentEntries := listEntries(t, beadsDir, parent)
	if len(parentEntries) != 1 {
		t.Fatalf("parent entries=%+v, want 1", parentEntries)
	}
	wantParentBranch := "todo/UIEXT-9000-" + parent
	if parentEntries[0].Branch != wantParentBranch {
		t.Errorf("parent branch=%q want %q", parentEntries[0].Branch, wantParentBranch)
	}

	childEntries := listEntries(t, beadsDir, child)
	if len(childEntries) != 1 {
		t.Fatalf("child entries=%+v, want 1", childEntries)
	}
	wantChildBranch := "todo/UIEXT-9000-" + child
	if childEntries[0].Branch != wantChildBranch {
		t.Errorf("child branch=%q want %q (jira id should be inherited from parent)",
			childEntries[0].Branch, wantChildBranch)
	}
}

func TestDirWorktreeStopsCascadeAtLinkedAncestor(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "wt-stop-parent")
	child := testutil.CreateChildBead(t, beadsDir, "wt-stop-child", parent)
	bare := bareRepo(t, "main")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", parent, bare); code != 0 {
		t.Fatalf("parent worktree: exit=%d stderr=%q", code, stderr)
	}
	parentEntries := listEntries(t, beadsDir, parent)
	if len(parentEntries) != 1 {
		t.Fatalf("parent entries after first call=%+v", parentEntries)
	}
	parentSHA := refSHA(t, parentEntries[0].Path, "HEAD")

	advanceBranch(t, parentEntries[0].Path)

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", child, bare); code != 0 {
		t.Fatalf("child worktree: exit=%d stderr=%q", code, stderr)
	}

	parentEntriesAfter := listEntries(t, beadsDir, parent)
	if len(parentEntriesAfter) != 1 {
		t.Errorf("parent entries changed: want 1, got %+v", parentEntriesAfter)
	}

	childEntries := listEntries(t, beadsDir, child)
	if len(childEntries) != 1 {
		t.Fatalf("child entries=%+v, want 1", childEntries)
	}
	parentSHAAfter := refSHA(t, parentEntries[0].Path, "HEAD")
	childSHA := refSHA(t, childEntries[0].Path, "HEAD")
	if parentSHAAfter == parentSHA {
		t.Fatalf("test setup broken: parent HEAD did not advance")
	}
	if childSHA != parentSHAAfter {
		t.Errorf("child HEAD %q does not match parent HEAD %q (child should branch off the parent's latest)", childSHA, parentSHAAfter)
	}
}

func TestDirWorktreeIdempotent(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "wt-idempotent")
	bare := bareRepo(t, "main")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare); code != 0 {
		t.Fatalf("first worktree: exit=%d stderr=%q", code, stderr)
	}
	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare); code != 0 {
		t.Fatalf("second worktree: exit=%d stderr=%q", code, stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Errorf("entries=%+v, want exactly 1 after idempotent re-run", entries)
	}
}

// TestDirWorktreeParallelSameTicket regresses
// https://beads/ai-vrc.58: launching `dir worktree` on the same ticket in
// parallel against different bare repos would race on the ticket's
// metadata.dirs read-modify-write and silently drop entries. The lock added
// in dir/lock.go serialises these calls per beadsDir; this test verifies
// every concurrent worktree shows up in the final entry list.
func TestDirWorktreeParallelSameTicket(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "wt-parallel")

	const n = 4
	bares := make([]string, n)
	for i := range bares {
		bares[i] = bareRepo(t, "main")
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			errs[i] = dir.Worktree("", beadsDir, ticket, bares[i])
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Worktree #%d (%s): %v", i, bares[i], err)
		}
	}

	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != n {
		t.Fatalf("entries=%+v, want %d (one per parallel call)", entries, n)
	}
	gotBares := make([]string, len(entries))
	for i, e := range entries {
		gotBares[i] = e.BareRepo
	}
	sort.Strings(gotBares)
	wantBares := append([]string(nil), bares...)
	sort.Strings(wantBares)
	for i := range wantBares {
		if gotBares[i] != wantBares[i] {
			t.Errorf("bareRepo[%d]=%q want %q (full got=%v want=%v)", i, gotBares[i], wantBares[i], gotBares, wantBares)
		}
	}
}

// TestDirWorktreeParallelChildrenSameParent regresses the cascade race: when
// multiple child beads are addressed in parallel against the same bare repo,
// each cascade tries to create the shared parent's worktree first. Without
// the lock they raced on bd metadata and on `git worktree add` against the
// same bare. The lock makes the parent's worktree appear exactly once and
// every child cascades through it.
func TestDirWorktreeParallelChildrenSameParent(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "wt-par-cascade-parent")
	const n = 4
	children := make([]string, n)
	for i := range children {
		children[i] = testutil.CreateChildBead(t, beadsDir, fmt.Sprintf("wt-par-cascade-child-%d", i), parent)
	}
	bare := bareRepo(t, "main")

	var wg sync.WaitGroup
	errs := make([]error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			errs[i] = dir.Worktree("", beadsDir, children[i], bare)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Worktree child#%d: %v", i, err)
		}
	}

	parentEntries := listEntries(t, beadsDir, parent)
	if len(parentEntries) != 1 {
		t.Fatalf("parent entries=%+v want exactly 1 (cascade should create the parent's worktree exactly once)", parentEntries)
	}
	for i, child := range children {
		ce := listEntries(t, beadsDir, child)
		if len(ce) != 1 {
			t.Errorf("child#%d entries=%+v want exactly 1", i, ce)
		}
	}
}

// TestDirWorktreeCascadeMultipleParentBranchesErrors regresses the "two
// branches" rule on the cascade side: when an ancestor has more than one
// worktree linked to the same bare, the base branch is ambiguous, so creating
// a descendant's worktree must abort rather than silently pick one.
func TestDirWorktreeCascadeMultipleParentBranchesErrors(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	withProjectConfig(t, beadsDir, `
worktree:
  path: "{bare-repo-dir}/{bare-repo-name}-{branch}"
`)
	parent := testutil.CreateBead(t, beadsDir, "wt-multi-parent")
	child := testutil.CreateChildBead(t, beadsDir, "wt-multi-child", parent)
	bare := bareRepo(t, "main")
	gitInTest(t, bare, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	source := sourceOf(bare)
	for _, b := range []string{"topic/a", "topic/b"} {
		gitInTest(t, source, "checkout", "-q", "-b", b)
		gitInTest(t, source, "commit", "--allow-empty", "-q", "-m", "on "+b)
		gitInTest(t, source, "checkout", "-q", "main")
		if err := dir.WorktreeForBranch("", beadsDir, parent, bare, b); err != nil {
			t.Fatalf("WorktreeForBranch(%s): %v", b, err)
		}
	}

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", child, bare)
	if code == 0 {
		t.Fatal("dir worktree succeeded despite ambiguous parent, want error")
	}
	if !strings.Contains(stderr, "worktrees linked to") {
		t.Errorf("stderr=%q want to mention multiple worktrees", stderr)
	}
	if entries := listEntries(t, beadsDir, child); len(entries) != 0 {
		t.Errorf("child entries=%+v want none (cascade aborted before linking)", entries)
	}
}

func TestDirWorktreeNotAGitRepo(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "wt-not-git")
	notRepo := t.TempDir()

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, notRepo)
	if code == 0 {
		t.Fatal("worktree succeeded on non-git path, want error")
	}
	if !strings.Contains(stderr, "not a git repository") {
		t.Errorf("stderr=%q want to mention 'not a git repository'", stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 0 {
		t.Errorf("entries=%+v want none", entries)
	}
}

// bareRepo creates a bare repo with one initial commit on defaultBranch and a
// remote/origin/HEAD symref pointing at it (simulating a clone). Returns the
// absolute bare path.
func bareRepo(t *testing.T, defaultBranch string) string {
	t.Helper()
	parent := t.TempDir()

	// Create a "remote" non-bare repo with one commit on defaultBranch.
	source := filepath.Join(parent, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	envv := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	gitIn := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = envv
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	gitIn(source, "init", "-q", "-b", defaultBranch)
	gitIn(source, "commit", "--allow-empty", "-q", "-m", "init")

	// Clone source into bare; clone will set origin/HEAD automatically.
	bare := filepath.Join(parent, "repo.git")
	cloneCmd := exec.Command("git", "clone", "--bare", source, bare)
	cloneCmd.Env = envv
	if out, err := cloneCmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v\n%s", err, out)
	}
	abs, err := filepath.Abs(bare)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

func worktreeOnBranch(t *testing.T, wt, branch string) bool {
	t.Helper()
	out, err := exec.Command("git", "-C", wt, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse abbrev-ref: %v", err)
	}
	return strings.TrimSpace(string(out)) == branch
}

func refSHA(t *testing.T, wt, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", wt, "rev-parse", ref).Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(string(out))
}

// bareBranchSHA returns the SHA at refs/heads/<branch> in the bare repo.
func bareBranchSHA(t *testing.T, bare, branch string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", bare, "rev-parse", "refs/heads/"+branch).Output()
	if err != nil {
		t.Fatalf("bare rev-parse refs/heads/%s: %v", branch, err)
	}
	return strings.TrimSpace(string(out))
}

// advanceBranch makes a commit on the current branch of wt so that its HEAD
// diverges from where it started.
func advanceBranch(t *testing.T, wt string) {
	t.Helper()
	envv := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	cmd := exec.Command("git", "-C", wt, "commit", "--allow-empty", "-q", "-m", "advance")
	cmd.Env = envv
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("advance branch: %v\n%s", err, out)
	}
}

// silence unused-import warnings if a test is commented out.
var _ = dir.Entry{}.Path

// withProjectConfig writes a project.yaml into beadsDir for the duration of
// the test. The file is removed in cleanup so it does not leak into other
// tests sharing the same pool entry.
func withProjectConfig(t *testing.T, beadsDir, yaml string) {
	t.Helper()
	p := filepath.Join(beadsDir, "project.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write project.yaml: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(p) })
}

func TestDirWorktreeUsesProjectConfig(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	withProjectConfig(t, beadsDir, `
worktree:
  branch: "custom/{bead-id}"
  path: "{bare-repo-dir}/{bare-repo-name}/branches/{bead-id}"
`)
	ticket := testutil.CreateBead(t, beadsDir, "wt-custom")
	bare := bareRepo(t, "main")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare)
	if code != 0 {
		t.Fatalf("worktree exit=%d stderr=%q", code, stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v want 1", entries)
	}
	wantBranch := "custom/" + ticket
	wantPath := filepath.Join(filepath.Dir(bare),
		strings.TrimSuffix(filepath.Base(bare), ".git"), "branches", ticket)
	if entries[0].Branch != wantBranch {
		t.Errorf("branch=%q want %q", entries[0].Branch, wantBranch)
	}
	if entries[0].Path != wantPath {
		t.Errorf("path=%q want %q", entries[0].Path, wantPath)
	}
	if !worktreeOnBranch(t, entries[0].Path, wantBranch) {
		t.Errorf("worktree at %s is not on branch %s", entries[0].Path, wantBranch)
	}
}

// TestWorktreeForBranchDiffersFromTemplate verifies that an explicit branch
// name is checked out as-is, even when the template would have rendered a
// different name. The branch must pre-exist on the bare's "origin".
func TestWorktreeForBranchDiffersFromTemplate(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "wt-explicit-differs")
	bare := bareRepo(t, "main")
	gitInTest(t, bare, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	// Create the explicit branch on the "remote" (source repo).
	source := sourceOf(bare)
	explicit := "feature/from-github"
	gitInTest(t, source, "checkout", "-q", "-b", explicit)
	gitInTest(t, source, "commit", "--allow-empty", "-q", "-m", "on explicit")
	explicitSHA := gitInTest(t, source, "rev-parse", "HEAD")
	gitInTest(t, source, "checkout", "-q", "main")

	if err := dir.WorktreeForBranch("", beadsDir, ticket, bare, explicit); err != nil {
		t.Fatalf("WorktreeForBranch: %v", err)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v want 1", entries)
	}
	if entries[0].Branch != explicit {
		t.Errorf("branch=%q want %q (must be discovered, not template)", entries[0].Branch, explicit)
	}
	// The template-derived branch must NOT have been created.
	templateBranch := "todo/" + ticket
	if _, ok := runGitTest(bare, "rev-parse", "--verify", "refs/heads/"+templateBranch); ok {
		t.Errorf("template branch %q was created; should be skipped", templateBranch)
	}
	if !worktreeOnBranch(t, entries[0].Path, explicit) {
		t.Errorf("worktree not on branch %q", explicit)
	}
	if sha := refSHA(t, entries[0].Path, "HEAD"); sha != explicitSHA {
		t.Errorf("HEAD=%q want explicit branch SHA %q", sha, explicitSHA)
	}
}

// TestWorktreeForBranchMatchesTemplate verifies the discovered branch is used
// directly even when it happens to equal what the template would render. We
// pre-create the branch on the remote; the worktree must reuse it rather than
// fail with "branch already exists" from a -b create.
func TestWorktreeForBranchMatchesTemplate(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "wt-explicit-matches")
	bare := bareRepo(t, "main")
	gitInTest(t, bare, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	source := sourceOf(bare)
	// Use whatever the template would render: "todo/<ticket>".
	branch := "todo/" + ticket
	gitInTest(t, source, "checkout", "-q", "-b", branch)
	gitInTest(t, source, "commit", "--allow-empty", "-q", "-m", "on branch")
	wantSHA := gitInTest(t, source, "rev-parse", "HEAD")
	gitInTest(t, source, "checkout", "-q", "main")

	if err := dir.WorktreeForBranch("", beadsDir, ticket, bare, branch); err != nil {
		t.Fatalf("WorktreeForBranch: %v", err)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v want 1", entries)
	}
	if entries[0].Branch != branch {
		t.Errorf("branch=%q want %q", entries[0].Branch, branch)
	}
	if sha := refSHA(t, entries[0].Path, "HEAD"); sha != wantSHA {
		t.Errorf("HEAD=%q want %q (must reuse remote branch, not branch off main)", sha, wantSHA)
	}
}

// TestWorktreeForBranchMultipleBranchesSameBare verifies a single ticket can
// hold multiple worktrees on the same bare repo, one per discovered branch.
func TestWorktreeForBranchMultipleBranchesSameBare(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	withProjectConfig(t, beadsDir, `
worktree:
  path: "{bare-repo-dir}/{bare-repo-name}-{branch}"
`)
	ticket := testutil.CreateBead(t, beadsDir, "wt-multi-branch")
	bare := bareRepo(t, "main")
	gitInTest(t, bare, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	source := sourceOf(bare)
	for _, b := range []string{"topic/a", "topic/b"} {
		gitInTest(t, source, "checkout", "-q", "-b", b)
		gitInTest(t, source, "commit", "--allow-empty", "-q", "-m", "on "+b)
		gitInTest(t, source, "checkout", "-q", "main")
	}

	for _, b := range []string{"topic/a", "topic/b"} {
		if err := dir.WorktreeForBranch("", beadsDir, ticket, bare, b); err != nil {
			t.Fatalf("WorktreeForBranch(%s): %v", b, err)
		}
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 2 {
		t.Fatalf("entries=%+v want 2", entries)
	}
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Branch] = true
	}
	for _, want := range []string{"topic/a", "topic/b"} {
		if !got[want] {
			t.Errorf("missing entry for branch %q; got %v", want, got)
		}
	}
}

// TestDirWorktreeBranchFlagAttachesRemoteBranch drives the `--branch` CLI
// path: an explicit branch that exists only on origin must be fetched and
// attached as-is, bypassing the template-derived name.
func TestDirWorktreeBranchFlagAttachesRemoteBranch(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "wt-branch-flag")
	bare := bareRepo(t, "main")
	gitInTest(t, bare, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	source := sourceOf(bare)
	explicit := "feature/from-flag"
	gitInTest(t, source, "checkout", "-q", "-b", explicit)
	gitInTest(t, source, "commit", "--allow-empty", "-q", "-m", "on explicit")
	explicitSHA := gitInTest(t, source, "rev-parse", "HEAD")
	gitInTest(t, source, "checkout", "-q", "main")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare, "--branch", explicit); code != 0 {
		t.Fatalf("worktree exit=%d stderr=%q", code, stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v want 1", entries)
	}
	if entries[0].Branch != explicit {
		t.Errorf("branch=%q want %q", entries[0].Branch, explicit)
	}
	if templateBranch := "todo/" + ticket; templateBranchExists(bare, templateBranch) {
		t.Errorf("template branch %q was created; should be skipped under --branch", templateBranch)
	}
	if sha := refSHA(t, entries[0].Path, "HEAD"); sha != explicitSHA {
		t.Errorf("HEAD=%q want explicit SHA %q", sha, explicitSHA)
	}
}

// TestDirWorktreeBranchFlagMissingValue: `--branch` with no following token
// is a usage error and must not mutate state.
func TestDirWorktreeBranchFlagMissingValue(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "wt-branch-missing")
	bare := bareRepo(t, "main")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare, "--branch")
	if code == 0 {
		t.Fatalf("worktree with bare --branch succeeded; want usage error")
	}
	if !strings.Contains(stderr, "usage") {
		t.Errorf("stderr=%q want 'usage'", stderr)
	}
	if got := listEntries(t, beadsDir, ticket); len(got) != 0 {
		t.Errorf("entries=%+v want none", got)
	}
}

func templateBranchExists(bare, branch string) bool {
	_, ok := runGitTest(bare, "rev-parse", "--verify", "refs/heads/"+branch)
	return ok
}

// runGitTest is a thin runGit wrapper for tests — returns (output, ok).
func runGitTest(repo string, args ...string) (string, bool) {
	full := append([]string{"-C", repo}, args...)
	out, err := exec.Command("git", full...).Output()
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(out))
	return s, s != ""
}

func TestDirWorktreeTypePrefixOverride(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	withProjectConfig(t, beadsDir, `
type-prefix:
  task: enh
`)
	ticket := testutil.CreateBead(t, beadsDir, "wt-typeprefix")
	bare := bareRepo(t, "main")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare); code != 0 {
		t.Fatalf("worktree exit=%d stderr=%q", code, stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v want 1", entries)
	}
	// default type is "task", configured to map to "enh".
	wantBranch := "enh/" + ticket
	if entries[0].Branch != wantBranch {
		t.Errorf("branch=%q want %q", entries[0].Branch, wantBranch)
	}
}

// gitInTest runs git in dir with deterministic author env. Fails the test
// on non-zero exit.
func gitInTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	envv := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = envv
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// sourceOf returns the path to the source (non-bare) repo that bareRepo
// cloned from; it lives next to the bare under the same parent tempdir.
func sourceOf(bare string) string {
	return filepath.Join(filepath.Dir(bare), "source")
}

func TestDirWorktreeReusesRemoteBranch(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "wt-remote-reuse")
	bare := bareRepo(t, "main")
	// bareRepo is a `git clone --bare`, whose default fetch refspec maps
	// origin's heads into local refs/heads/. To exercise the same
	// upstream-tracking semantics a real (non-bare) clone would have,
	// rewrite the refspec to the standard remote-tracking layout. Without
	// this, git refuses to resolve @{upstream} for refs that aren't stored
	// under refs/remotes/<remote>/*.
	gitInTest(t, bare, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	source := sourceOf(bare)

	// Create the branch on the remote ahead of `dir worktree`, with a commit
	// that is NOT on main, so we can distinguish remote-reuse from
	// based-on-main.
	branch := "todo/" + ticket
	gitInTest(t, source, "checkout", "-q", "-b", branch)
	gitInTest(t, source, "commit", "--allow-empty", "-q", "-m", "remote-only commit")
	remoteSHA := gitInTest(t, source, "rev-parse", "HEAD")
	gitInTest(t, source, "checkout", "-q", "main")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare); code != 0 {
		t.Fatalf("worktree exit=%d stderr=%q", code, stderr)
	}

	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v want 1", entries)
	}
	if sha := refSHA(t, entries[0].Path, "HEAD"); sha != remoteSHA {
		t.Errorf("worktree HEAD %q != origin/%s %q (should reuse remote branch)", sha, branch, remoteSHA)
	}
	upstream, err := exec.Command("git", "-C", entries[0].Path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}").CombinedOutput()
	if err != nil {
		t.Fatalf("rev-parse upstream: %v\n%s", err, upstream)
	}
	if got := strings.TrimSpace(string(upstream)); got != "origin/"+branch {
		t.Errorf("upstream=%q want %q", got, "origin/"+branch)
	}
}

func TestDirWorktreeRootBeadDoesNotTrackBase(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "wt-root-no-track")
	bare := bareRepo(t, "master")
	// Give the bare repo the standard remote-tracking refspec so origin/master
	// is a real remote-tracking ref (as a non-bare clone has). Without this,
	// gitDefaultBase falls back to the local branch and git would never auto-set
	// upstream — the bug only surfaces when the base is a remote-tracking ref.
	gitInTest(t, bare, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	gitInTest(t, bare, "fetch", "-q", "origin")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare); code != 0 {
		t.Fatalf("worktree exit=%d stderr=%q", code, stderr)
	}

	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v want 1", entries)
	}
	// Branch must start at the master commit...
	if sha := refSHA(t, entries[0].Path, "HEAD"); sha != bareBranchSHA(t, bare, "master") {
		t.Errorf("worktree HEAD %q != bare master %q (root should branch off master)", sha, bareBranchSHA(t, bare, "master"))
	}
	// ...but must NOT track origin/master (or anything) as upstream.
	out, err := exec.Command("git", "-C", entries[0].Path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}").CombinedOutput()
	if err == nil {
		t.Errorf("root bead branch has upstream %q, want none", strings.TrimSpace(string(out)))
	}
}

func TestDirWorktreeFetchFailureFallsThrough(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "wt-fetch-fail")
	bare := bareRepo(t, "main")
	// Break origin: point it at a non-existent path. fetch must fail but
	// `dir worktree` should still succeed via the existing code path.
	gitInTest(t, bare, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "does-not-exist"))

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare); code != 0 {
		t.Fatalf("worktree exit=%d stderr=%q", code, stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v want 1", entries)
	}
	if sha := refSHA(t, entries[0].Path, "HEAD"); sha != bareBranchSHA(t, bare, "main") {
		t.Errorf("worktree HEAD %q != bare main %q (should fall through to base)", sha, bareBranchSHA(t, bare, "main"))
	}
}

func TestDirWorktreeLocalBranchCollisionErrors(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "wt-local-collide")
	bare := bareRepo(t, "main")
	branch := "todo/" + ticket
	gitInTest(t, bare, "branch", branch, "main")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare)
	if code == 0 {
		t.Fatal("worktree succeeded despite local branch collision, want error")
	}
	if !strings.Contains(stderr, "worktree add") && !strings.Contains(stderr, "already exists") {
		t.Errorf("stderr=%q want to mention git worktree add error", stderr)
	}
	if entries := listEntries(t, beadsDir, ticket); len(entries) != 0 {
		t.Errorf("entries=%+v want none", entries)
	}
}

func TestDirWorktreeBadTemplateFailsLoudly(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	withProjectConfig(t, beadsDir, `
worktree:
  branch: "{nope}/{bead-id}"
`)
	ticket := testutil.CreateBead(t, beadsDir, "wt-bad-tmpl")
	bare := bareRepo(t, "main")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare)
	if code == 0 {
		t.Fatal("worktree succeeded with bad template, want error")
	}
	if !strings.Contains(stderr, "unknown placeholder") {
		t.Errorf("stderr=%q want to mention 'unknown placeholder'", stderr)
	}
	if entries := listEntries(t, beadsDir, ticket); len(entries) != 0 {
		t.Errorf("entries=%+v want none", entries)
	}
}

// TestFastForwardWorktreesAdvances is the main regression: parent branch
// advances after the child worktree is cut; FastForwardWorktrees brings
// the child up to the parent's new tip via --ff-only.
func TestFastForwardWorktreesAdvances(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "ff-advance-parent")
	child := testutil.CreateChildBead(t, beadsDir, "ff-advance-child", parent)
	bare := bareRepo(t, "main")

	if err := dir.Worktree("", beadsDir, child, bare); err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	parentEntries := listEntries(t, beadsDir, parent)
	childEntries := listEntries(t, beadsDir, child)
	if len(parentEntries) != 1 || len(childEntries) != 1 {
		t.Fatalf("parent entries=%+v child entries=%+v want 1 each", parentEntries, childEntries)
	}

	initialSHA := refSHA(t, childEntries[0].Path, "HEAD")
	advanceBranch(t, parentEntries[0].Path)
	parentAdvancedSHA := refSHA(t, parentEntries[0].Path, "HEAD")
	if parentAdvancedSHA == initialSHA {
		t.Fatal("test setup: parent did not advance")
	}

	results, err := dir.FastForwardWorktrees("", beadsDir, child)
	if err != nil {
		t.Fatalf("FastForwardWorktrees: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%+v want 1", results)
	}
	if !results[0].Advanced {
		t.Errorf("Advanced=false, want true")
	}
	if results[0].Skipped {
		t.Errorf("Skipped=true, want false")
	}
	if sha := refSHA(t, childEntries[0].Path, "HEAD"); sha != parentAdvancedSHA {
		t.Errorf("child HEAD %q != parent HEAD %q after FF", sha, parentAdvancedSHA)
	}
}

// TestFastForwardWorktreesDiverged verifies that a child branch with its own
// commits is left untouched.
func TestFastForwardWorktreesDiverged(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "ff-diverged-parent")
	child := testutil.CreateChildBead(t, beadsDir, "ff-diverged-child", parent)
	bare := bareRepo(t, "main")

	if err := dir.Worktree("", beadsDir, child, bare); err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	parentEntries := listEntries(t, beadsDir, parent)
	childEntries := listEntries(t, beadsDir, child)

	// Both advance independently with different files so they produce distinct
	// commit hashes and the branches genuinely diverge.
	advanceBranchWithFile(t, parentEntries[0].Path, "parent-work.txt", "parent\n")
	advanceBranchWithFile(t, childEntries[0].Path, "child-work.txt", "child\n")
	childSHABefore := refSHA(t, childEntries[0].Path, "HEAD")

	results, err := dir.FastForwardWorktrees("", beadsDir, child)
	if err != nil {
		t.Fatalf("FastForwardWorktrees: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%+v want 1", results)
	}
	if results[0].Advanced {
		t.Errorf("Advanced=true on diverged child, want false")
	}
	if !results[0].Skipped {
		t.Errorf("Skipped=false on diverged child, want true")
	}
	if sha := refSHA(t, childEntries[0].Path, "HEAD"); sha != childSHABefore {
		t.Errorf("child HEAD changed %q → %q; diverged branch must not be touched", childSHABefore, sha)
	}
}

// advanceBranchWithFile commits a new/modified tracked file so that the
// resulting commit changes the working tree (unlike --allow-empty).
func advanceBranchWithFile(t *testing.T, wt, filename, content string) {
	t.Helper()
	envv := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if err := os.WriteFile(filepath.Join(wt, filename), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
	for _, args := range [][]string{{"add", filename}, {"commit", "-q", "-m", "advance with file"}} {
		cmd := exec.Command("git", append([]string{"-C", wt}, args...)...)
		cmd.Env = envv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// TestFastForwardWorktreesDirty verifies that a worktree with an untracked
// file that would be overwritten by the FF is left untouched.
func TestFastForwardWorktreesDirty(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "ff-dirty-parent")
	child := testutil.CreateChildBead(t, beadsDir, "ff-dirty-child", parent)
	bare := bareRepo(t, "main")

	if err := dir.Worktree("", beadsDir, child, bare); err != nil {
		t.Fatalf("Worktree: %v", err)
	}
	parentEntries := listEntries(t, beadsDir, parent)
	childEntries := listEntries(t, beadsDir, child)

	// Parent advances by adding shared.txt so the FF would create that file
	// in the child's working tree.
	advanceBranchWithFile(t, parentEntries[0].Path, "shared.txt", "parent version\n")

	// Dirty the child with an untracked file at the same path — FF must be
	// refused by git ("would be overwritten by merge").
	if err := os.WriteFile(filepath.Join(childEntries[0].Path, "shared.txt"), []byte("local work\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	childSHABefore := refSHA(t, childEntries[0].Path, "HEAD")

	results, err := dir.FastForwardWorktrees("", beadsDir, child)
	if err != nil {
		t.Fatalf("FastForwardWorktrees: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results=%+v want 1", results)
	}
	if results[0].Advanced {
		t.Errorf("Advanced=true on dirty worktree, want false")
	}
	if !results[0].Skipped {
		t.Errorf("Skipped=false on dirty worktree, want true")
	}
	if sha := refSHA(t, childEntries[0].Path, "HEAD"); sha != childSHABefore {
		t.Errorf("child HEAD changed; dirty worktree must not be touched")
	}
}

// TestFastForwardWorktreesNoParentEntry verifies that when the parent bead has
// no worktree for a given bare (e.g. the child was linked via WorktreeForBranch
// without cascade), FastForwardWorktrees silently no-ops for that bare.
func TestFastForwardWorktreesNoParentEntry(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "ff-noentry-parent")
	child := testutil.CreateChildBead(t, beadsDir, "ff-noentry-child", parent)

	// bare2: child is linked via WorktreeForBranch (no cascade), so parent
	// gets no entry for bare2.
	bare2 := bareRepo(t, "main")
	gitInTest(t, bare2, "config", "remote.origin.fetch", "+refs/heads/*:refs/remotes/origin/*")
	source2 := sourceOf(bare2)
	childBranch := "todo/" + child
	gitInTest(t, source2, "checkout", "-q", "-b", childBranch)
	gitInTest(t, source2, "commit", "--allow-empty", "-q", "-m", "branch on bare2")
	gitInTest(t, source2, "checkout", "-q", "main")
	if err := dir.WorktreeForBranch("", beadsDir, child, bare2, childBranch); err != nil {
		t.Fatalf("WorktreeForBranch: %v", err)
	}

	childEntries := listEntries(t, beadsDir, child)
	if len(childEntries) != 1 {
		t.Fatalf("child entries=%+v want 1", childEntries)
	}
	childSHABefore := refSHA(t, childEntries[0].Path, "HEAD")

	// Parent has no entry for bare2 → child worktree is silently skipped.
	results, err := dir.FastForwardWorktrees("", beadsDir, child)
	if err != nil {
		t.Fatalf("FastForwardWorktrees: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("results=%+v want none (parent has no entry for bare2)", results)
	}
	if sha := refSHA(t, childEntries[0].Path, "HEAD"); sha != childSHABefore {
		t.Errorf("child HEAD changed; must not be touched when parent has no entry")
	}
}
