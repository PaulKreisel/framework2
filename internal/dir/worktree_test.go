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
