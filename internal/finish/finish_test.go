package finish_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/env"
	"framework2/internal/testutil"
)

func TestFinishRemovesWorktreeAndEnvAndClosesBead(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	parent := createBead(t, "finish-removes-parent")
	child := createChildBead(t, "finish-removes-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, "dir", "worktree", child, bare)
	if _, err := env.Create("", beadsDir, child); err != nil {
		t.Fatalf("env.Create: %v", err)
	}
	childWT := worktreePath(bare, child)
	parentWT := worktreePath(bare, parent)

	if _, stderr, code := testutil.RunTicket(t, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q", code, stderr)
	}

	if _, err := os.Stat(childWT); !os.IsNotExist(err) {
		t.Errorf("child worktree %s still exists after finish (err=%v)", childWT, err)
	}
	if _, err := os.Stat(parentWT); err != nil {
		t.Errorf("parent worktree %s should still exist: %v", parentWT, err)
	}
	envPath := filepath.Join(beadsDir, "envs", child)
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Errorf("env folder %s still exists after finish (err=%v)", envPath, err)
	}
	if status := beadStatus(t, child); status != "closed" {
		t.Errorf("bead status=%q want closed", status)
	}
}

func TestFinishRefusesDirtyWorktree(t *testing.T) {
	testutil.SharedBdDB(t)
	ticket := createBead(t, "finish-dirty")
	bare := bareRepo(t, "main")

	mustRunTicket(t, "dir", "worktree", ticket, bare)
	wt := worktreePath(bare, ticket)
	if err := os.WriteFile(filepath.Join(wt, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	_, stderr, code := testutil.RunTicket(t, "finish", ticket)
	if code == 0 {
		t.Fatal("finish succeeded on dirty worktree, want error")
	}
	if !strings.Contains(stderr, "uncommitted changes") {
		t.Errorf("stderr=%q want to mention 'uncommitted changes'", stderr)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("worktree removed despite refused finish: %v", err)
	}
}

func TestFinishRefusesDetachedHead(t *testing.T) {
	testutil.SharedBdDB(t)
	ticket := createBead(t, "finish-detached")
	bare := bareRepo(t, "main")

	mustRunTicket(t, "dir", "worktree", ticket, bare)
	wt := worktreePath(bare, ticket)
	sha := gitOut(t, wt, "rev-parse", "HEAD")
	gitDo(t, wt, "checkout", "-q", "--detach", sha)

	_, stderr, code := testutil.RunTicket(t, "finish", ticket)
	if code == 0 {
		t.Fatal("finish succeeded on detached HEAD, want error")
	}
	if !strings.Contains(stderr, "detached") {
		t.Errorf("stderr=%q want to mention 'detached'", stderr)
	}
}

func TestFinishFastForwardsOntoAdvancedParent(t *testing.T) {
	testutil.SharedBdDB(t)
	parent := createBead(t, "finish-ff-parent")
	child := createChildBead(t, "finish-ff-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, "dir", "worktree", child, bare)
	parentWT := worktreePath(bare, parent)

	// Advance the parent branch; the child's branch is still at the old
	// parent tip, so finish should fast-forward it.
	advanceBranch(t, parentWT)
	parentTip := gitOut(t, parentWT, "rev-parse", "HEAD")

	// We expect git worktree remove to fire next — so capture the child
	// branch SHA from the bare repo's ref after finish, not from the wt.
	if _, stderr, code := testutil.RunTicket(t, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q", code, stderr)
	}

	childTip := gitOut(t, bare, "rev-parse", "refs/heads/"+child)
	if childTip != parentTip {
		t.Errorf("child branch tip %q != parent tip %q (expected fast-forward)", childTip, parentTip)
	}
}

func TestFinishRefusesDivergedBranch(t *testing.T) {
	testutil.SharedBdDB(t)
	parent := createBead(t, "finish-diverge-parent")
	child := createChildBead(t, "finish-diverge-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, "dir", "worktree", child, bare)
	parentWT := worktreePath(bare, parent)
	childWT := worktreePath(bare, child)

	// Both branches commit independently → divergent history.
	advanceBranch(t, parentWT)
	advanceBranch(t, childWT)

	_, stderr, code := testutil.RunTicket(t, "finish", child)
	if code == 0 {
		t.Fatal("finish succeeded on diverged branch, want error")
	}
	if !strings.Contains(stderr, "diverged") {
		t.Errorf("stderr=%q want to mention 'diverged'", stderr)
	}
	if _, err := os.Stat(childWT); err != nil {
		t.Errorf("child worktree removed despite refused finish: %v", err)
	}
	if status := beadStatus(t, child); status != "open" {
		t.Errorf("bead status=%q want still open after refused finish", status)
	}
}

// --- helpers ---

func mustRunTicket(t *testing.T, args ...string) {
	t.Helper()
	_, stderr, code := testutil.RunTicket(t, args...)
	if code != 0 {
		t.Fatalf("ticket %v exit=%d stderr=%q", args, code, stderr)
	}
}

func createBead(t *testing.T, title string) string {
	t.Helper()
	out, err := exec.Command("bd", "create", title, "--silent").Output()
	if err != nil {
		t.Fatalf("bd create: %v", err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("bd", "close", id).Run() })
	return id
}

func createChildBead(t *testing.T, title, parent string) string {
	t.Helper()
	out, err := exec.Command("bd", "create", title, "--parent", parent, "--silent").Output()
	if err != nil {
		t.Fatalf("bd create child: %v", err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("bd", "close", id).Run() })
	return id
}

func beadStatus(t *testing.T, id string) string {
	t.Helper()
	out, err := exec.Command("bd", "--json", "show", id).Output()
	if err != nil {
		t.Fatalf("bd show: %v", err)
	}
	var rows []struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse bd show: %v\n%s", err, out)
	}
	if len(rows) == 0 {
		return ""
	}
	return rows[0].Status
}

func worktreePath(bare, ticketID string) string {
	base := filepath.Base(bare)
	base = strings.TrimSuffix(base, ".git")
	return filepath.Join(filepath.Dir(bare), base+"-"+ticketID)
}

func bareRepo(t *testing.T, defaultBranch string) string {
	t.Helper()
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	envv := gitEnv()
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

func gitDo(t *testing.T, wt string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", wt}, args...)...)
	cmd.Env = gitEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, wt, err, out)
	}
}

func gitOut(t *testing.T, wt string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", wt}, args...)...)
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, wt, err)
	}
	return strings.TrimSpace(string(out))
}

// advanceBranch makes a real (non-empty) commit on the current branch of wt,
// so that callers can advance two branches that share a parent without
// accidentally producing identical commit SHAs (which happens if you use
// identical empty commits with the same author within the same second).
func advanceBranch(t *testing.T, wt string) {
	t.Helper()
	stamp := filepath.Base(wt) + "-" + t.Name()
	f := filepath.Join(wt, "advance-"+strings.ReplaceAll(stamp, "/", "_"))
	if err := os.WriteFile(f, []byte(stamp), 0o644); err != nil {
		t.Fatalf("write advance file: %v", err)
	}
	gitDo(t, wt, "add", filepath.Base(f))
	gitDo(t, wt, "commit", "-q", "-m", "advance "+stamp)
}

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
}
