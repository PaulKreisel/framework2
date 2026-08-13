// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package ticketcli_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/testutil"
)

func TestDirSlugSetsSlug(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "slug-set-target")

	_, errOut, code := testutil.RunTicketIn(t, beadsDir, "dir", "slug", id, "Refined New Slug")
	if code != 0 {
		t.Fatalf("dir slug: exit=%d stderr=%q", code, errOut)
	}
	if got := loadSlug(t, beadsDir, id); got != "refined-new-slug" {
		t.Errorf("slug = %q, want %q", got, "refined-new-slug")
	}
}

func TestDirSlugRefusesAfterWorktreeMarkedUsed(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "slug-frozen-target")
	bare := bareRepoMain(t)

	if _, errOut, code := testutil.RunTicketIn(t, beadsDir, "dir", "worktree", id, bare); code != 0 {
		t.Fatalf("dir worktree: exit=%d stderr=%q", code, errOut)
	}
	if !loadSlugUsed(t, beadsDir, id) {
		t.Fatalf("expected slug_is_used=true after dir worktree")
	}

	_, errOut, code := testutil.RunTicketIn(t, beadsDir, "dir", "slug", id, "Try To Change")
	if code == 0 {
		t.Fatalf("dir slug succeeded on a frozen slug; expected error")
	}
	if !strings.Contains(errOut, "already used") {
		t.Errorf("stderr=%q, want mention of 'already used'", errOut)
	}
}

// bareRepoMain creates a bare repo with one commit on main and returns its
// absolute path. Mirrors the helper in internal/dir but the test packages are
// distinct so a duplicate is needed.
func bareRepoMain(t *testing.T) string {
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
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = envv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
		}
	}
	run(source, "init", "-q", "-b", "main")
	run(source, "commit", "--allow-empty", "-q", "-m", "init")
	bare := filepath.Join(parent, "repo.git")
	clone := exec.Command("git", "clone", "--bare", source, bare)
	clone.Env = envv
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v\n%s", err, out)
	}
	abs, err := filepath.Abs(bare)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}
