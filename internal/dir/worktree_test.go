package dir_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
