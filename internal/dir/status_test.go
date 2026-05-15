package dir_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/dir"
	"framework2/internal/testutil"
)

func TestDirStatusCleanWorktree(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "status-clean")
	path := gitRepo(t, false, false)

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, path); code != 0 {
		t.Fatalf("link: exit=%d stderr=%q", code, stderr)
	}
	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "status", ticket)
	if code != 0 {
		t.Fatalf("status exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "status: clean") {
		t.Errorf("stdout=%q, want 'status: clean'", stdout)
	}
	if !strings.Contains(stdout, "branch: main") {
		t.Errorf("stdout=%q, want 'branch: main'", stdout)
	}
}

func TestDirStatusDirtyWorktree(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "status-dirty")
	path := gitRepo(t, false, false)
	// Untracked file + staged file.
	if err := os.WriteFile(filepath.Join(path, "untracked.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write untracked: %v", err)
	}
	stagedPath := filepath.Join(path, "staged.txt")
	if err := os.WriteFile(stagedPath, []byte("y"), 0o644); err != nil {
		t.Fatalf("write staged: %v", err)
	}
	gitAt(t, path, "add", "staged.txt")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, path); code != 0 {
		t.Fatalf("link: exit=%d stderr=%q", code, stderr)
	}
	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "status", ticket)
	if code != 0 {
		t.Fatalf("status exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "status: dirty") {
		t.Errorf("stdout=%q, want 'status: dirty'", stdout)
	}
	if !strings.Contains(stdout, "staged: 1") || !strings.Contains(stdout, "untracked: 1") {
		t.Errorf("stdout=%q, want staged: 1 and untracked: 1", stdout)
	}
}

func TestDirStatusJSON(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "status-json")
	path := gitRepo(t, false, false)

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, path); code != 0 {
		t.Fatalf("link: exit=%d stderr=%q", code, stderr)
	}
	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "status", ticket, "--json")
	if code != 0 {
		t.Fatalf("status --json exit=%d stderr=%q", code, stderr)
	}
	var got dir.StatusResult
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse status json %q: %v", stdout, err)
	}
	if got.Entry.Path != path {
		t.Errorf("entry.path=%q want %q", got.Entry.Path, path)
	}
	if !got.Worktree.Clean {
		t.Errorf("worktree.clean=false, want true")
	}
	if got.Worktree.Branch != "main" {
		t.Errorf("worktree.branch=%q want main", got.Worktree.Branch)
	}
	if got.Divergence != nil {
		t.Errorf("divergence=%+v, want nil for parentless ticket", got.Divergence)
	}
}

func TestDirStatusMissingPath(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "status-missing")
	path := gitRepo(t, false, false)

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, path); code != 0 {
		t.Fatalf("link: exit=%d stderr=%q", code, stderr)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatalf("remove path: %v", err)
	}
	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "status", ticket)
	if code == 0 {
		t.Fatal("status succeeded after path removal, want error")
	}
	if !strings.Contains(stderr, "linked path is gone") {
		t.Errorf("stderr=%q, want 'linked path is gone'", stderr)
	}
}

func TestDirStatusMultipleEntriesNoSelector(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "status-multi")
	p1 := gitRepo(t, false, false)
	p2 := gitRepo(t, false, false)

	for _, p := range []string{p1, p2} {
		if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, p); code != 0 {
			t.Fatalf("link %s: exit=%d stderr=%q", p, code, stderr)
		}
	}
	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "status", ticket)
	if code == 0 {
		t.Fatal("status with no selector and 2 linked dirs succeeded, want error")
	}
	if !strings.Contains(stderr, "valid choices") {
		t.Errorf("stderr=%q, want 'valid choices'", stderr)
	}
	if !strings.Contains(stderr, p1) || !strings.Contains(stderr, p2) {
		t.Errorf("stderr=%q, want both paths listed", stderr)
	}
}

func TestDirStatusSelectorByPath(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "status-pick-path")
	p1 := gitRepo(t, false, false)
	p2 := gitRepo(t, false, false)

	for _, p := range []string{p1, p2} {
		if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, p); code != 0 {
			t.Fatalf("link %s: exit=%d stderr=%q", p, code, stderr)
		}
	}
	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "status", ticket, p2)
	if code != 0 {
		t.Fatalf("status exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "path: "+p2) {
		t.Errorf("stdout=%q, want 'path: %s'", stdout, p2)
	}
	if strings.Contains(stdout, "path: "+p1) {
		t.Errorf("stdout=%q, should not mention %s", stdout, p1)
	}
}

func TestDirStatusUnknownSelector(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "status-unknown-sel")
	path := gitRepo(t, false, false)

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, path); code != 0 {
		t.Fatalf("link: exit=%d stderr=%q", code, stderr)
	}
	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "status", ticket, "/no/such/path")
	if code == 0 {
		t.Fatal("status with unknown selector succeeded, want error")
	}
	if !strings.Contains(stderr, "valid choices") {
		t.Errorf("stderr=%q, want 'valid choices'", stderr)
	}
}

func TestDirStatusDivergence(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "status-parent")
	child := testutil.CreateChildBead(t, beadsDir, "status-child", parent)
	bare := bareRepo(t, "main")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", child, bare); code != 0 {
		t.Fatalf("worktree: exit=%d stderr=%q", code, stderr)
	}
	childEntries := listEntries(t, beadsDir, child)
	if len(childEntries) != 1 {
		t.Fatalf("childEntries=%+v want 1", childEntries)
	}
	// Add two commits on the child branch.
	advanceBranch(t, childEntries[0].Path)
	advanceBranch(t, childEntries[0].Path)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "status", child)
	if code != 0 {
		t.Fatalf("status exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "vs parent "+parent) {
		t.Errorf("stdout=%q, want 'vs parent %s'", stdout, parent)
	}
	if !strings.Contains(stdout, "ahead 2, behind 0") {
		t.Errorf("stdout=%q, want 'ahead 2, behind 0'", stdout)
	}
	// Two "+ <sha> advance" lines.
	if n := strings.Count(stdout, "+ "); n < 2 {
		t.Errorf("stdout=%q, want >=2 ahead commit lines, got %d", stdout, n)
	}
}

func TestDirStatusDivergenceJSON(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "status-div-json-parent")
	child := testutil.CreateChildBead(t, beadsDir, "status-div-json-child", parent)
	bare := bareRepo(t, "main")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", child, bare); code != 0 {
		t.Fatalf("worktree: exit=%d stderr=%q", code, stderr)
	}
	childEntries := listEntries(t, beadsDir, child)
	if len(childEntries) != 1 {
		t.Fatalf("childEntries=%+v want 1", childEntries)
	}
	advanceBranch(t, childEntries[0].Path)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "status", child, "--json")
	if code != 0 {
		t.Fatalf("status --json exit=%d stderr=%q", code, stderr)
	}
	var got dir.StatusResult
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("parse: %v stdout=%q", err, stdout)
	}
	if got.Divergence == nil {
		t.Fatalf("divergence nil, want populated")
	}
	if got.Divergence.ParentTicket != parent {
		t.Errorf("parentTicket=%q want %q", got.Divergence.ParentTicket, parent)
	}
	if got.Divergence.Ahead != 1 || got.Divergence.Behind != 0 {
		t.Errorf("ahead/behind = %d/%d, want 1/0", got.Divergence.Ahead, got.Divergence.Behind)
	}
	if len(got.Divergence.AheadCommits) != 1 {
		t.Errorf("aheadCommits=%+v, want 1 entry", got.Divergence.AheadCommits)
	}
}

func TestDirStatusNoParentLinkSkipsDivergence(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "status-no-parent-link-p")
	child := testutil.CreateChildBead(t, beadsDir, "status-no-parent-link-c", parent)
	// Child links a plain path (not a worktree on a bare), so divergence is skipped.
	path := gitRepo(t, false, false)
	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", child, path); code != 0 {
		t.Fatalf("link: exit=%d stderr=%q", code, stderr)
	}
	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "status", child)
	if code != 0 {
		t.Fatalf("status exit=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "vs parent") {
		t.Errorf("stdout=%q, divergence section should be omitted", stdout)
	}
}

func TestDirStatusSelectorByBareRepo(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "status-sel-bare")
	bare := bareRepo(t, "main")
	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare); code != 0 {
		t.Fatalf("worktree: exit=%d stderr=%q", code, stderr)
	}
	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "status", ticket, bare)
	if code != 0 {
		t.Fatalf("status exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "status: clean") {
		t.Errorf("stdout=%q, want 'status: clean'", stdout)
	}
}

// gitAt runs `git -C path <args>` with deterministic author env, failing the
// test on error.
func gitAt(t *testing.T, path string, args ...string) {
	t.Helper()
	envv := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
	cmd.Env = envv
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git -C %s %v: %v\n%s", path, args, err, out)
	}
}
