// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package dir_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/testutil"
)

func TestDirSyncCheckInSync(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "sync-in-sync")
	work, _ := clonedRepo(t)

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, work); code != 0 {
		t.Fatalf("link: exit=%d stderr=%q", code, stderr)
	}
	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "sync-check", ticket)
	if code != 0 {
		t.Fatalf("sync-check exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "in sync") {
		t.Errorf("stdout=%q, want 'in sync'", stdout)
	}
}

func TestDirSyncCheckAhead(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "sync-ahead")
	work, _ := clonedRepo(t)
	// A local commit that was never pushed: HEAD is ahead of origin/main.
	advanceBranch(t, work)

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, work); code != 0 {
		t.Fatalf("link: exit=%d stderr=%q", code, stderr)
	}
	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "sync-check", ticket)
	if code == 0 {
		t.Fatal("sync-check succeeded while ahead, want non-zero")
	}
	if !strings.Contains(stderr, "ahead 1, behind 0") {
		t.Errorf("stderr=%q, want 'ahead 1, behind 0'", stderr)
	}
	if !strings.Contains(stderr, work) {
		t.Errorf("stderr=%q, want offending path %s", stderr, work)
	}
}

func TestDirSyncCheckBehind(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "sync-behind")
	work, bare := clonedRepo(t)
	// Advance the remote so the clone is behind; sync-check's own fetch must
	// pick this up before comparing.
	advanceRemoteMain(t, bare)

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, work); code != 0 {
		t.Fatalf("link: exit=%d stderr=%q", code, stderr)
	}
	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "sync-check", ticket)
	if code == 0 {
		t.Fatal("sync-check succeeded while behind, want non-zero")
	}
	if !strings.Contains(stderr, "ahead 0, behind 1") {
		t.Errorf("stderr=%q, want 'ahead 0, behind 1'", stderr)
	}
}

func TestDirSyncCheckNoUpstreamSkips(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "sync-no-upstream")
	// Plain repo on a branch with no configured upstream.
	path := gitRepo(t, false, false)

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, path); code != 0 {
		t.Fatalf("link: exit=%d stderr=%q", code, stderr)
	}
	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "sync-check", ticket)
	if code != 0 {
		t.Fatalf("sync-check exit=%d stderr=%q (no upstream should be skipped)", code, stderr)
	}
	if !strings.Contains(stdout, "no upstream, skipped") {
		t.Errorf("stdout=%q, want 'no upstream, skipped'", stdout)
	}
}

func TestDirSyncCheckMissingPathSkips(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "sync-missing-path")
	work, _ := clonedRepo(t)

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, work); code != 0 {
		t.Fatalf("link: exit=%d stderr=%q", code, stderr)
	}
	if err := os.RemoveAll(work); err != nil {
		t.Fatalf("remove path: %v", err)
	}
	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "sync-check", ticket)
	if code != 0 {
		t.Fatalf("sync-check exit=%d stderr=%q (gone path should be skipped)", code, stderr)
	}
	if !strings.Contains(stdout, "path is gone, skipped") {
		t.Errorf("stdout=%q, want 'path is gone, skipped'", stdout)
	}
}

func TestDirSyncCheckDetachedSkips(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "sync-detached")
	work, _ := clonedRepo(t)
	// Link while on a branch, then detach so sync-check sees a detached HEAD.
	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, work); code != 0 {
		t.Fatalf("link: exit=%d stderr=%q", code, stderr)
	}
	gitAt(t, work, "checkout", "-q", "--detach", "HEAD")

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "sync-check", ticket)
	if code != 0 {
		t.Fatalf("sync-check exit=%d stderr=%q (detached should be skipped)", code, stderr)
	}
	if !strings.Contains(stdout, "detached HEAD, skipped") {
		t.Errorf("stdout=%q, want 'detached HEAD, skipped'", stdout)
	}
}

func TestDirSyncCheckNoLinkedDirs(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "sync-no-dirs")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "sync-check", ticket)
	if code != 0 {
		t.Fatalf("sync-check exit=%d stderr=%q (zero dirs should pass vacuously)", code, stderr)
	}
}

func TestDirSyncCheckMixedReportsOnlyOffender(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "sync-mixed")
	inSync, _ := clonedRepo(t)
	ahead, _ := clonedRepo(t)
	advanceBranch(t, ahead)

	for _, p := range []string{inSync, ahead} {
		if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, p); code != 0 {
			t.Fatalf("link %s: exit=%d stderr=%q", p, code, stderr)
		}
	}
	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "sync-check", ticket)
	if code == 0 {
		t.Fatal("sync-check succeeded with one ahead dir, want non-zero")
	}
	if !strings.Contains(stderr, ahead) {
		t.Errorf("stderr=%q, want offending path %s", stderr, ahead)
	}
	if strings.Contains(stderr, inSync) {
		t.Errorf("stderr=%q, should not list the in-sync dir %s", stderr, inSync)
	}
}

// clonedRepo builds a bare "origin" with one commit on main and a non-bare
// working clone whose main tracks origin/main. Returns the working clone path
// and the bare path.
func clonedRepo(t *testing.T) (work, bare string) {
	t.Helper()
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	gitAt(t, source, "init", "-q", "-b", "main")
	gitAt(t, source, "commit", "--allow-empty", "-q", "-m", "init")

	bare = filepath.Join(parent, "repo.git")
	gitClone(t, "--bare", source, bare)
	work = filepath.Join(parent, "work")
	gitClone(t, bare, work)
	return work, bare
}

// advanceRemoteMain pushes a fresh commit onto the bare repo's main via a
// throwaway clone, so a clone of bare becomes one commit behind after fetching.
func advanceRemoteMain(t *testing.T, bare string) {
	t.Helper()
	other := filepath.Join(t.TempDir(), "pusher")
	gitClone(t, bare, other)
	advanceBranch(t, other)
	gitAt(t, other, "push", "-q", "origin", "main")
}

// gitClone runs `git clone <args...>` with the deterministic test author env.
func gitClone(t *testing.T, args ...string) {
	t.Helper()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	cmd := exec.Command("git", append([]string{"clone", "-q"}, args...)...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone %v: %v\n%s", args, err, out)
	}
}
