// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package teardown_test

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

// TestTeardownRemovesDirtyWorktreeAndClosesBead covers the main path: a
// ticket with a worktree carrying uncommitted changes is torn down — the
// worktree dir is gone, the dirs entry is unlinked, the bead is closed and
// the env folder is deleted. The branch survives in the bare repo.
func TestTeardownRemovesDirtyWorktreeAndClosesBead(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "td-dirty")
	bare := bareRepo(t)

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare); code != 0 {
		t.Fatalf("worktree exit=%d stderr=%q", code, stderr)
	}
	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "env", ticket); code != 0 {
		t.Fatalf("env exit=%d stderr=%q", code, stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v, want 1", entries)
	}
	wt := entries[0].Path
	// Dirty the worktree: finish would refuse; teardown must not.
	if err := os.WriteFile(filepath.Join(wt, "scratch.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatalf("dirty worktree: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "teardown", ticket)
	if code != 0 {
		t.Fatalf("teardown exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("worktree still present at %s", wt)
	}
	if got := listEntries(t, beadsDir, ticket); len(got) != 0 {
		t.Errorf("dirs entries not unlinked: %+v", got)
	}
	if status := beadStatus(t, beadsDir, ticket); status != "closed" {
		t.Errorf("bead status=%q, want closed", status)
	}
	envPath := filepath.Join(beadsDir, "envs", ticket)
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Errorf("env folder still present at %s", envPath)
	}
	// Branch (and its commits) must survive in the bare repo.
	branch := entries[0].Branch
	if out, err := exec.Command("git", "-C", bare, "rev-parse", "--verify", branch).CombinedOutput(); err != nil {
		t.Errorf("branch %s gone from bare: %v\n%s", branch, err, out)
	}
}

// TestTeardownAlreadyClosedAndMissingWorktreeDir is the bulk-sweep case: the
// bead is already closed and the worktree directory was deleted manually.
// Teardown succeeds anyway and prunes the stale registration.
func TestTeardownAlreadyClosedAndMissingWorktreeDir(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "td-closed")
	bare := bareRepo(t)

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", ticket, bare); code != 0 {
		t.Fatalf("worktree exit=%d stderr=%q", code, stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v, want 1", entries)
	}
	if out, err := testutil.BdCmd(beadsDir, "close", ticket).CombinedOutput(); err != nil {
		t.Fatalf("bd close: %v\n%s", err, out)
	}
	if err := os.RemoveAll(entries[0].Path); err != nil {
		t.Fatalf("remove worktree dir: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "teardown", ticket)
	if code != 0 {
		t.Fatalf("teardown exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got := listEntries(t, beadsDir, ticket); len(got) != 0 {
		t.Errorf("dirs entries not unlinked: %+v", got)
	}
	// Registration must be pruned from the bare repo.
	out, err := exec.Command("git", "-C", bare, "worktree", "list", "--porcelain").Output()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	if strings.Contains(string(out), entries[0].Path) {
		t.Errorf("stale worktree registration survived:\n%s", out)
	}
}

func listEntries(t *testing.T, beadsDir, ticket string) []dir.Entry {
	t.Helper()
	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "list", ticket)
	if code != 0 {
		t.Fatalf("dir list exit=%d stderr=%q", code, stderr)
	}
	var entries []dir.Entry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("parse dir list: %v\n%s", err, stdout)
	}
	return entries
}

func beadStatus(t *testing.T, beadsDir, ticket string) string {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "show", ticket).Output()
	if err != nil {
		t.Fatalf("bd show: %v", err)
	}
	var rows []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &rows); err != nil || len(rows) == 0 {
		t.Fatalf("parse bd show: %v\n%s", err, out)
	}
	return rows[0].Status
}

func bareRepo(t *testing.T) string {
	t.Helper()
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	envv := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	gitIn := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = envv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
		}
	}
	gitIn(source, "init", "-q", "-b", "main")
	gitIn(source, "commit", "--allow-empty", "-q", "-m", "init")
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
