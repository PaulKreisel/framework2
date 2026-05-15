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

func TestFinishAdvancesParentBranchToChildTip(t *testing.T) {
	testutil.SharedBdDB(t)
	parent := createBead(t, "finish-advance-parent")
	child := createChildBead(t, "finish-advance-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, "dir", "worktree", child, bare)
	parentWT := worktreePath(bare, parent)
	childWT := worktreePath(bare, child)

	// Child makes a commit; parent branch stays put.
	advanceBranch(t, childWT)
	childTip := gitOut(t, childWT, "rev-parse", "HEAD")

	if _, stderr, code := testutil.RunTicket(t, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q", code, stderr)
	}

	parentBranchTip := gitOut(t, bare, "rev-parse", "refs/heads/"+parent)
	if parentBranchTip != childTip {
		t.Errorf("parent branch tip %q != child tip %q (expected FF-advance)", parentBranchTip, childTip)
	}
	parentHeadTip := gitOut(t, parentWT, "rev-parse", "HEAD")
	if parentHeadTip != childTip {
		t.Errorf("parent worktree HEAD %q != child tip %q (expected FF-advance in worktree)", parentHeadTip, childTip)
	}
}

func TestFinishRebasesCleanlyDivergedBranch(t *testing.T) {
	testutil.SharedBdDB(t)
	parent := createBead(t, "finish-diverge-clean-parent")
	child := createChildBead(t, "finish-diverge-clean-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, "dir", "worktree", child, bare)
	parentWT := worktreePath(bare, parent)
	childWT := worktreePath(bare, child)

	// Each branch commits a uniquely-named file → divergent but
	// non-conflicting history (a rebase applies cleanly).
	advanceBranch(t, parentWT)
	advanceBranch(t, childWT)

	mustRunTicket(t, "finish", child)

	if status := beadStatus(t, child); status != "closed" {
		t.Errorf("bead status=%q want closed after successful finish", status)
	}
	if _, err := os.Stat(childWT); !os.IsNotExist(err) {
		t.Errorf("child worktree still present after finish: err=%v", err)
	}
	// Parent branch should now point at the child's rebased tip, which is
	// the latest commit on the parent worktree.
	got := gitOut(t, parentWT, "log", "--format=%s", "-1")
	if !strings.HasPrefix(got, "advance ") {
		t.Errorf("parent tip after finish = %q, want 'advance ...' (the rebased child commit)", got)
	}
}

func TestFinishPausesOnConflictAndResumesWithContinue(t *testing.T) {
	testutil.SharedBdDB(t)
	parent := createBead(t, "finish-diverge-conflict-parent")
	child := createChildBead(t, "finish-diverge-conflict-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, "dir", "worktree", child, bare)
	parentWT := worktreePath(bare, parent)
	childWT := worktreePath(bare, child)

	// Both branches touch the SAME file with conflicting content → rebase
	// hits conflicts and pauses.
	writeAndCommit(t, parentWT, "shared.txt", "parent line\n", "parent edit")
	writeAndCommit(t, childWT, "shared.txt", "child line\n", "child edit")

	_, stderr, code := testutil.RunTicket(t, "finish", child)
	if code == 0 {
		t.Fatal("finish succeeded on conflicting rebase, want error")
	}
	if !strings.Contains(stderr, "--continue") {
		t.Errorf("stderr=%q want to point at `--continue`", stderr)
	}
	// Worktree must be PAUSED in a rebase (not aborted), and bead still open.
	if _, err := os.Stat(filepath.Join(childWT, ".git")); err != nil {
		t.Errorf("child worktree gone despite refused finish: %v", err)
	}
	if status := beadStatus(t, child); status != "open" {
		t.Errorf("bead status=%q want still open after paused finish", status)
	}
	if !gitDirHasRebase(t, childWT) {
		t.Errorf("child worktree is not in a paused rebase; finish should have left rebase state in place")
	}

	// Without --continue, a second invocation should refuse because the
	// rebase is still in progress.
	_, stderr, code = testutil.RunTicket(t, "finish", child)
	if code == 0 {
		t.Fatal("finish succeeded with rebase still in progress, want error")
	}
	if !strings.Contains(stderr, "rebase in progress") {
		t.Errorf("stderr=%q want to mention 'rebase in progress'", stderr)
	}

	// Simulate the human resolving the conflict: pick child's version.
	if err := os.WriteFile(filepath.Join(childWT, "shared.txt"), []byte("child line\n"), 0o644); err != nil {
		t.Fatalf("write resolution: %v", err)
	}
	gitDo(t, childWT, "add", "shared.txt")

	// Resume.
	mustRunTicket(t, "finish", child, "--continue")

	if status := beadStatus(t, child); status != "closed" {
		t.Errorf("bead status=%q want closed after --continue", status)
	}
	if _, err := os.Stat(childWT); !os.IsNotExist(err) {
		t.Errorf("child worktree still present after --continue finish: err=%v", err)
	}
}

func TestFinishContinueWithoutPausedRebase(t *testing.T) {
	testutil.SharedBdDB(t)
	parent := createBead(t, "finish-continue-nothing-parent")
	child := createChildBead(t, "finish-continue-nothing-child", parent)
	bare := bareRepo(t, "main")
	mustRunTicket(t, "dir", "worktree", child, bare)
	advanceBranch(t, worktreePath(bare, child))

	_, stderr, code := testutil.RunTicket(t, "finish", child, "--continue")
	if code == 0 {
		t.Fatal("finish --continue succeeded with no paused rebase, want error")
	}
	if !strings.Contains(stderr, "no rebase in progress") {
		t.Errorf("stderr=%q want to mention 'no rebase in progress'", stderr)
	}
}

// gitDirHasRebase reports whether wt's git dir contains rebase-{merge,apply}
// state — i.e. the worktree is mid-rebase.
func gitDirHasRebase(t *testing.T, wt string) bool {
	t.Helper()
	gd := gitOut(t, wt, "rev-parse", "--git-dir")
	if !filepath.IsAbs(gd) {
		gd = filepath.Join(wt, gd)
	}
	for _, name := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Stat(filepath.Join(gd, name)); err == nil {
			return true
		}
	}
	return false
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

// writeAndCommit creates/overwrites a single file with body, stages it, and
// commits with msg. Used to set up rebase conflicts where two branches must
// touch the same path with different content.
func writeAndCommit(t *testing.T, wt, name, body, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	gitDo(t, wt, "add", name)
	gitDo(t, wt, "commit", "-q", "-m", msg)
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
