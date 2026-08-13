// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package finish_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/testutil"
)

// TestFinishAddsSubtaskTrailerToRebasedCommits covers the basic fresh-allocation
// case: a child with new commits gets `Subtask 1` appended to each commit
// message after finish, and the parent bead's metadata.handover.contributors
// is bumped to 1 with the child's contributionIndex set to 1.
func TestFinishAddsSubtaskTrailerToRebasedCommits(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "subtask-fresh-parent")
	child := testutil.CreateChildBead(t, beadsDir, "subtask-fresh-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	childWT := worktreePath(bare, child)
	writeAndCommit(t, childWT, "a.txt", "a\n", "feat: first commit")
	writeAndCommit(t, childWT, "b.txt", "b\n", "feat: second commit\n\nbody.")

	mustRunTicket(t, beadsDir, "finish", child)

	// Both rewritten commits should carry the `Subtask 1` trailer.
	msgs := commitMessagesOnBranch(t, bare, "todo/"+parent, 2)
	for i, m := range msgs {
		if !strings.HasSuffix(m, "\nSubtask 1") {
			t.Errorf("commit %d message does not end with Subtask 1 trailer:\n%s", i, m)
		}
	}

	// Parent metadata.handover.contributors = 1, child contributionIndex = 1.
	if got := handoverContributors(t, beadsDir, parent); got != 1 {
		t.Errorf("parent contributors = %d, want 1", got)
	}
	if got := handoverContributionIndex(t, beadsDir, child); got != 1 {
		t.Errorf("child contributionIndex = %d, want 1", got)
	}
}

// TestFinishSkipsEmptySubtask: a child with no new commits (its branch tip
// already equals the parent branch tip) should not consume an index. The
// parent's contributors counter and the child's contributionIndex stay at 0.
func TestFinishSkipsEmptySubtask(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "subtask-empty-parent")
	child := testutil.CreateChildBead(t, beadsDir, "subtask-empty-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	// No commits on the child branch.

	mustRunTicket(t, beadsDir, "finish", child)

	if got := handoverContributors(t, beadsDir, parent); got != 0 {
		t.Errorf("parent contributors = %d, want 0 for empty subtask", got)
	}
	if got := handoverContributionIndex(t, beadsDir, child); got != 0 {
		t.Errorf("child contributionIndex = %d, want 0 for empty subtask", got)
	}
}

// TestFinishAllocatesPerSiblingIndex: two siblings finished into the same
// parent get sequential indices (1, 2). The parent's contributors counter
// climbs to 2.
func TestFinishAllocatesPerSiblingIndex(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "subtask-siblings-parent")
	first := testutil.CreateChildBead(t, beadsDir, "subtask-siblings-first", parent)
	second := testutil.CreateChildBead(t, beadsDir, "subtask-siblings-second", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", first, bare)
	firstWT := worktreePath(bare, first)
	writeAndCommit(t, firstWT, "f.txt", "f\n", "feat: first sibling commit")

	mustRunTicket(t, beadsDir, "finish", first)

	if got := handoverContributors(t, beadsDir, parent); got != 1 {
		t.Errorf("after first sibling, parent contributors = %d, want 1", got)
	}
	if got := handoverContributionIndex(t, beadsDir, first); got != 1 {
		t.Errorf("first sibling contributionIndex = %d, want 1", got)
	}

	mustRunTicket(t, beadsDir, "dir", "worktree", second, bare)
	secondWT := worktreePath(bare, second)
	writeAndCommit(t, secondWT, "s.txt", "s\n", "feat: second sibling commit")

	mustRunTicket(t, beadsDir, "finish", second)

	if got := handoverContributors(t, beadsDir, parent); got != 2 {
		t.Errorf("after second sibling, parent contributors = %d, want 2", got)
	}
	if got := handoverContributionIndex(t, beadsDir, second); got != 2 {
		t.Errorf("second sibling contributionIndex = %d, want 2", got)
	}

	// The second sibling's commit on the parent branch carries `Subtask 2`.
	msgs := commitMessagesOnBranch(t, bare, "todo/"+parent, 1)
	if !strings.HasSuffix(msgs[0], "\nSubtask 2") {
		t.Errorf("second sibling commit does not end with Subtask 2:\n%s", msgs[0])
	}
}

// TestFinishCascadesSubtaskTrailer covers the grandchild → child → grandparent
// chain: when the grandchild's commits land via the child's later finish, the
// existing `Subtask M` trailer becomes `Subtask N.M` (N = child's index under
// its parent).
func TestFinishCascadesSubtaskTrailer(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	grandparent := testutil.CreateBead(t, beadsDir, "subtask-cascade-grandparent")
	parent := testutil.CreateChildBead(t, beadsDir, "subtask-cascade-parent", grandparent)
	child := testutil.CreateChildBead(t, beadsDir, "subtask-cascade-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	childWT := worktreePath(bare, child)
	writeAndCommit(t, childWT, "g.txt", "g\n", "feat: grandchild commit")

	// First finish: child → parent. Grandchild commit picks up `Subtask 1`.
	mustRunTicket(t, beadsDir, "finish", child)
	msgs := commitMessagesOnBranch(t, bare, "todo/"+parent, 1)
	if !strings.HasSuffix(msgs[0], "\nSubtask 1") {
		t.Fatalf("after first finish, expected `Subtask 1` trailer:\n%s", msgs[0])
	}

	// Second finish: parent → grandparent. The grandchild commit now lands
	// on the grandparent's branch with `Subtask N.1`. The parent's own
	// contributionIndex N under the grandparent is allocated now.
	mustRunTicket(t, beadsDir, "finish", parent)

	parentIdx := handoverContributionIndex(t, beadsDir, parent)
	if parentIdx == 0 {
		t.Fatalf("parent contributionIndex unset after second finish")
	}
	wantTrailer := fmt.Sprintf("Subtask %d.1", parentIdx)
	msgs = commitMessagesOnBranch(t, bare, "todo/"+grandparent, 1)
	if !strings.HasSuffix(msgs[0], "\n"+wantTrailer) {
		t.Errorf("expected trailer %q on grandparent tip, got:\n%s", wantTrailer, msgs[0])
	}
}

// TestFinishMultiWorktreeSharesIndex: a child with worktrees in two bares
// shares the same contributionIndex across both. Both bares' commits get
// the same `Subtask N` trailer.
func TestFinishMultiWorktreeSharesIndex(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "subtask-multi-parent")
	child := testutil.CreateChildBead(t, beadsDir, "subtask-multi-child", parent)
	bareA := bareRepo(t, "main")
	bareB := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bareA)
	mustRunTicket(t, beadsDir, "dir", "worktree", child, bareB)
	writeAndCommit(t, worktreePath(bareA, child), "a.txt", "a\n", "feat: bare A commit")
	writeAndCommit(t, worktreePath(bareB, child), "b.txt", "b\n", "feat: bare B commit")

	mustRunTicket(t, beadsDir, "finish", child)

	// Index allocated once (= 1) and used on both bares' commits.
	if got := handoverContributors(t, beadsDir, parent); got != 1 {
		t.Errorf("parent contributors = %d, want 1", got)
	}
	if got := handoverContributionIndex(t, beadsDir, child); got != 1 {
		t.Errorf("child contributionIndex = %d, want 1", got)
	}
	for _, bare := range []string{bareA, bareB} {
		msgs := commitMessagesOnBranch(t, bare, "todo/"+parent, 1)
		if !strings.HasSuffix(msgs[0], "\nSubtask 1") {
			t.Errorf("commit in %s does not end with Subtask 1:\n%s", bare, msgs[0])
		}
	}
}

// TestFinishResumeReusesContributionIndex: after a finish that hit a rebase
// conflict and was resumed via --continue, the index is allocated exactly
// once (the resume path doesn't bump the parent's counter again).
func TestFinishResumeReusesContributionIndex(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "subtask-resume-parent")
	child := testutil.CreateChildBead(t, beadsDir, "subtask-resume-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	parentWT := worktreePath(bare, parent)
	childWT := worktreePath(bare, child)

	// Create a rebase conflict: both branches touch the same file.
	writeAndCommit(t, parentWT, "shared.txt", "parent line\n", "parent edit")
	writeAndCommit(t, childWT, "shared.txt", "child line\n", "child edit")

	if _, _, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code == 0 {
		t.Fatal("finish unexpectedly succeeded on conflicting rebase")
	}
	// No index allocated yet — we paused before the rebase landed.
	if got := handoverContributors(t, beadsDir, parent); got != 0 {
		t.Errorf("parent contributors = %d after paused rebase, want 0", got)
	}

	// Resolve the conflict and resume.
	if err := writeFile(childWT, "shared.txt", "child line\n"); err != nil {
		t.Fatalf("write resolution: %v", err)
	}
	gitDo(t, childWT, "add", "shared.txt")
	mustRunTicket(t, beadsDir, "finish", child, "--continue")

	if got := handoverContributors(t, beadsDir, parent); got != 1 {
		t.Errorf("parent contributors = %d after resume, want 1", got)
	}
	if got := handoverContributionIndex(t, beadsDir, child); got != 1 {
		t.Errorf("child contributionIndex = %d after resume, want 1", got)
	}
}

// --- helpers ---

func writeFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

func handoverContributors(t *testing.T, beadsDir, id string) int {
	t.Helper()
	return readHandoverField(t, beadsDir, id, "contributors")
}

func handoverContributionIndex(t *testing.T, beadsDir, id string) int {
	t.Helper()
	return readHandoverField(t, beadsDir, id, "contributionIndex")
}

func readHandoverField(t *testing.T, beadsDir, id, field string) int {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "show", id).Output()
	if err != nil {
		t.Fatalf("bd show %s: %v", id, err)
	}
	var rows []struct {
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse bd show: %v\n%s", err, out)
	}
	if len(rows) == 0 {
		t.Fatalf("bd show %s returned no rows", id)
	}
	raw, ok := rows[0].Metadata["handover"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return 0
	}
	var hm map[string]int
	if err := json.Unmarshal(raw, &hm); err != nil {
		t.Fatalf("parse handover meta: %v\n%s", err, raw)
	}
	return hm[field]
}

// commitMessagesOnBranch returns the last n commit messages on the named
// branch in bare, newest first. Messages are returned with any surrounding
// whitespace stripped so callers can do exact-match suffix checks.
func commitMessagesOnBranch(t *testing.T, bare, branch string, n int) []string {
	t.Helper()
	// Resolve each commit individually and read its message via cat-file
	// so we don't have to deal with git's between-commit separator
	// heuristics from `log --format=...`.
	revs, err := runGitCmd(bare, gitEnv(), "rev-list", branch, "-n", fmt.Sprintf("%d", n))
	if err != nil {
		t.Fatalf("git rev-list %s: %v\n%s", branch, err, revs)
	}
	var msgs []string
	for _, sha := range strings.Split(strings.TrimSpace(revs), "\n") {
		if sha == "" {
			continue
		}
		body, err := runGitCmd(bare, gitEnv(), "log", "-1", "--format=%B", sha)
		if err != nil {
			t.Fatalf("git log %s: %v\n%s", sha, err, body)
		}
		msgs = append(msgs, strings.TrimRight(body, "\n"))
	}
	return msgs
}
