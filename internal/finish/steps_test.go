package finish_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"framework2/internal/finish"
	"framework2/internal/testutil"
)

// TestFinishStepBeadHappyPath is the single-step acceptance test: a
// finish-steps.yaml whose condition matches drives one step-bead, which is
// created as a `finish-step` child of the ticket, closed on a clean check, and
// followed by the full cleanup (ticket closed, worktree + env removed).
func TestFinishStepBeadHappyPath(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "step-happy-parent")
	child := testutil.CreateChildBead(t, beadsDir, "step-happy-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	mustMakeEnv(t, beadsDir, child)
	childWT := worktreePath(bare, child)
	envPath := filepath.Join(beadsDir, "envs", child)

	// The check (`test -f marker`) fails on its first run because the marker is
	// absent, so `complete` runs and records its cwd into a marker OUTSIDE the
	// worktree (so the tree stays clean and removable); the second check then
	// finds the marker and passes. We assert the recorded cwd is the worktree.
	marker := filepath.Join(t.TempDir(), "where.txt")
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: skeleton step\n"+
		"      complete: \"pwd > "+marker+"\"\n"+
		"      check: \"test -f "+marker+"\"\n")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q", code, stderr)
	}

	// complete and check ran in the worktree.
	assertRanIn(t, marker, childWT)

	// A step-bead exists as a child of the ticket, carries the label, and is
	// closed.
	steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(steps) != 1 {
		t.Fatalf("expected exactly one finish-step child of %s, got %v", child, steps)
	}
	if got := beadStatus(t, beadsDir, steps[0]); got != "closed" {
		t.Errorf("step-bead %s status=%q want closed", steps[0], got)
	}
	if got := beadTitle(t, beadsDir, steps[0]); got != "skeleton step" {
		t.Errorf("step-bead title=%q want %q", got, "skeleton step")
	}

	// Full cleanup ran: ticket closed, worktree + env gone.
	if got := beadStatus(t, beadsDir, child); got != "closed" {
		t.Errorf("ticket status=%q want closed", got)
	}
	if _, err := os.Stat(childWT); !os.IsNotExist(err) {
		t.Errorf("child worktree %s still exists after finish (err=%v)", childWT, err)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Errorf("env folder %s still exists after finish (err=%v)", envPath, err)
	}
}

// TestFinishStepThreeStepGraph is the headline acceptance test for this
// ticket: a three-step graph (step 2 blocked_by step 1, step 3 blocked_by
// step 2) advanced across three `this finish` calls, with checks that fail the
// first time they run and pass on the second.
func TestFinishStepThreeStepGraph(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-graph-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	mustMakeEnv(t, beadsDir, child)
	childWT := worktreePath(bare, child)
	envPath := filepath.Join(beadsDir, "envs", child)

	// step 1 passes immediately; steps 2 and 3 each fail the FIRST time their
	// check runs (creating their marker) and pass the SECOND time. Markers
	// live outside the worktree so the tree stays clean for removal.
	m2 := filepath.Join(t.TempDir(), "s2")
	m3 := filepath.Join(t.TempDir(), "s3")
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: one\n"+
		"      check: \"true\"\n"+
		"    - title: two\n"+
		"      check: "+failFirst(m2)+"\n"+
		"      blocked_by: [one]\n"+
		"    - title: three\n"+
		"      check: "+failFirst(m3)+"\n"+
		"      blocked_by: [two]\n")

	// --- First call: step 1 closes, step 2 launches (check fails →
	// in-progress), step 3 stays blocked.
	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code == 0 {
		t.Fatalf("first finish should exit non-zero (steps pending); stderr=%q", stderr)
	}
	s := stepsByTitle(t, beadsDir, child)
	assertStatus(t, beadsDir, s["one"], "closed")
	assertStatus(t, beadsDir, s["two"], "in_progress")
	assertStatus(t, beadsDir, s["three"], "open") // still blocked, never launched
	assertStatus(t, beadsDir, child, "open")
	if _, err := os.Stat(childWT); err != nil {
		t.Errorf("worktree removed before all steps closed: %v", err)
	}

	// --- Second call: step 2 check passes → closes, step 3 launches (check
	// fails → in-progress).
	_, stderr, code = testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code == 0 {
		t.Fatalf("second finish should exit non-zero (step 3 pending); stderr=%q", stderr)
	}
	assertStatus(t, beadsDir, s["two"], "closed")
	assertStatus(t, beadsDir, s["three"], "in_progress")
	assertStatus(t, beadsDir, child, "open")

	// --- Third call: step 3 check passes → closes, full cleanup runs.
	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("third finish exit=%d stderr=%q (expected success)", code, stderr)
	}
	assertStatus(t, beadsDir, s["three"], "closed")
	assertStatus(t, beadsDir, child, "closed")
	if _, err := os.Stat(childWT); !os.IsNotExist(err) {
		t.Errorf("worktree %s still present after full cleanup (err=%v)", childWT, err)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Errorf("env folder %s still present after full cleanup (err=%v)", envPath, err)
	}
}

// TestFinishStepsCascadeInOneCall: when every check passes immediately, closing
// a step unblocks the next within the SAME finish call, so a chain finishes in
// one go.
func TestFinishStepsCascadeInOneCall(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-cascade-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: a\n"+
		"      check: \"true\"\n"+
		"    - title: b\n"+
		"      check: \"true\"\n"+
		"      blocked_by: [a]\n"+
		"    - title: c\n"+
		"      check: \"true\"\n"+
		"      blocked_by: [b]\n")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q (chain should complete in one call)", code, stderr)
	}
	s := stepsByTitle(t, beadsDir, child)
	for _, title := range []string{"a", "b", "c"} {
		assertStatus(t, beadsDir, s[title], "closed")
	}
	assertStatus(t, beadsDir, child, "closed")
}

// TestFinishStepSiblingsRunConcurrently is the headline regression for this
// ticket: when one step unblocks several siblings, the siblings' checks run
// concurrently rather than serialised in list order. `root` closes immediately
// and unblocks `a` and `b`; each records the wall-clock window its check
// occupied. Under the old sequential advance, `b` would not start until `a`'s
// check (a 1s sleep) had finished, so their windows could never overlap. With
// concurrent launch they do.
func TestFinishStepSiblingsRunConcurrently(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-concurrent-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	dir := t.TempDir()
	// Each sibling check stamps a start time, sleeps, then a stop time — all
	// outside the worktree so the tree stays clean and removable. The checks
	// pass (exit 0), so the whole graph closes in one finish call.
	window := func(name string) string {
		start := filepath.Join(dir, name+".start")
		stop := filepath.Join(dir, name+".stop")
		return fmt.Sprintf("'date +%%s.%%N > %s; sleep 1; date +%%s.%%N > %s'", start, stop)
	}
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: root\n"+
		"      check: \"true\"\n"+
		"    - title: a\n"+
		"      check: "+window("a")+"\n"+
		"      blocked_by: [root]\n"+
		"    - title: b\n"+
		"      check: "+window("b")+"\n"+
		"      blocked_by: [root]\n")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q (graph should complete in one call)", code, stderr)
	}
	s := stepsByTitle(t, beadsDir, child)
	for _, title := range []string{"root", "a", "b"} {
		assertStatus(t, beadsDir, s[title], "closed")
	}

	aStart, aStop := readWindow(t, dir, "a")
	bStart, bStop := readWindow(t, dir, "b")
	// Overlap ⟺ a started before b finished AND b started before a finished.
	// Serialised execution would put b's whole window after a's, so no overlap.
	if !(aStart < bStop && bStart < aStop) {
		t.Errorf("sibling checks did not overlap (ran serially): a=[%.3f,%.3f] b=[%.3f,%.3f]", aStart, aStop, bStart, bStop)
	}
}

// readWindow reads the start/stop unix timestamps a windowed check wrote for
// step `name` (see TestFinishStepSiblingsRunConcurrently).
func readWindow(t *testing.T, dir, name string) (start, stop float64) {
	t.Helper()
	read := func(suffix string) float64 {
		raw, err := os.ReadFile(filepath.Join(dir, name+suffix))
		if err != nil {
			t.Fatalf("read %s%s: %v", name, suffix, err)
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
		if err != nil {
			t.Fatalf("parse %s%s %q: %v", name, suffix, raw, err)
		}
		return v
	}
	return read(".start"), read(".stop")
}

// TestFinishStepEnvVars verifies the env surface: $BEAD_ID is visible to the
// condition, and $BEAD_ID + $STEP_BEAD_ID are visible to check (the step's own
// bead id).
func TestFinishStepEnvVars(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-env-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	marker := filepath.Join(t.TempDir(), "env.txt")
	// condition only matches when $BEAD_ID is the ticket; check records both
	// ids so we can assert STEP_BEAD_ID is the step's own bead.
	writeStepsYAML(t, bare, ""+
		"- condition: '[ \"$BEAD_ID\" = \""+child+"\" ]'\n"+
		"  steps:\n"+
		"    - title: env step\n"+
		"      check: 'echo \"$BEAD_ID|$STEP_BEAD_ID\" > "+marker+"'\n")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q", code, stderr)
	}

	steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(steps) != 1 {
		t.Fatalf("expected one finish-step child, got %v", steps)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read env marker: %v", err)
	}
	want := child + "|" + steps[0]
	if strings.TrimSpace(string(got)) != want {
		t.Errorf("check saw env %q, want %q", strings.TrimSpace(string(got)), want)
	}
}

// TestFinishStepFirstConditionWins regresses the "first exit-0 wins" rule
// across step-sets: the first set's condition fails, the second matches, so
// the second set's steps run.
func TestFinishStepFirstConditionWins(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-select-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	writeStepsYAML(t, bare, ""+
		"- condition: \"false\"\n"+
		"  steps:\n"+
		"    - title: first never\n"+
		"      check: \"true\"\n"+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: second wins\n"+
		"      check: \"true\"\n")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q", code, stderr)
	}

	steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(steps) != 1 {
		t.Fatalf("expected one finish-step child, got %v", steps)
	}
	if got := beadTitle(t, beadsDir, steps[0]); got != "second wins" {
		t.Errorf("ran step %q, want the second set (first exit-0 wins)", got)
	}
}

// TestFinishStepNoConditionMatchFallsBack: a present finish-steps.yaml whose
// conditions all fail must fall back to the Go path — no step-bead, ticket
// still finishes normally.
func TestFinishStepNoConditionMatchFallsBack(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "step-nomatch-parent")
	child := testutil.CreateChildBead(t, beadsDir, "step-nomatch-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	childWT := worktreePath(bare, child)
	writeStepsYAML(t, bare, ""+
		"- condition: \"false\"\n"+
		"  steps:\n"+
		"    - title: never\n"+
		"      check: \"true\"\n")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q", code, stderr)
	}
	if steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel); len(steps) != 0 {
		t.Errorf("expected no step-bead on fallback, got %v", steps)
	}
	if got := beadStatus(t, beadsDir, child); got != "closed" {
		t.Errorf("ticket status=%q want closed (Go fallback should still finish)", got)
	}
	if _, err := os.Stat(childWT); !os.IsNotExist(err) {
		t.Errorf("child worktree still present after fallback finish: err=%v", err)
	}
}

// TestFinishStepCheckFailureLeavesStepInProgress: a failing check aborts the
// finish — the (launched) step-bead stays in-progress, the ticket stays open,
// and the worktree is not removed.
func TestFinishStepCheckFailureLeavesStepInProgress(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-checkfail-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	childWT := worktreePath(bare, child)
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: failing check\n"+
		"      check: \"echo nope-marker; exit 3\"\n")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code == 0 {
		t.Fatal("finish succeeded despite failing check, want error")
	}
	if !strings.Contains(stderr, "check failed") || !strings.Contains(stderr, "nope-marker") {
		t.Errorf("stderr=%q want to mention the failed check and its output", stderr)
	}

	steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(steps) != 1 {
		t.Fatalf("expected the step-bead to have been created, got %v", steps)
	}
	if got := beadStatus(t, beadsDir, steps[0]); got != "in_progress" {
		t.Errorf("step-bead status=%q want in_progress after failed check", got)
	}
	if got := beadStatus(t, beadsDir, child); got != "open" {
		t.Errorf("ticket status=%q want still open after failed check", got)
	}
	if _, err := os.Stat(childWT); err != nil {
		t.Errorf("worktree removed despite failed check: %v", err)
	}
}

// TestFinishStepResumeNoDuplicateBeads: a step whose check keeps failing leaves
// the step-bead in-progress; a second `this finish` must resume the existing
// step rather than create a fresh set of step-beads. Guards the resume path
// (runStepBead reads existing finish-step children before materialising any).
func TestFinishStepResumeNoDuplicateBeads(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-resume-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: failing check\n"+
		"      check: \"exit 7\"\n")

	if _, _, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code == 0 {
		t.Fatal("first finish succeeded despite failing check, want error")
	}
	first := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(first) != 1 {
		t.Fatalf("expected one finish-step child after first call, got %v", first)
	}

	if _, _, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code == 0 {
		t.Fatal("second finish succeeded despite failing check, want error")
	}
	second := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(second) != 1 {
		t.Fatalf("second finish created duplicate step-beads: %v (want the same single bead %v)", second, first)
	}
	if second[0] != first[0] {
		t.Errorf("resumed step-bead id changed: first=%s second=%s", first[0], second[0])
	}
	if got := beadStatus(t, beadsDir, second[0]); got != "in_progress" {
		t.Errorf("resumed step-bead status=%q want in_progress", got)
	}
}

// TestFinishStepCompleteFailureSkipsCheck: when the first check fails, `complete`
// runs; a failing `complete` then leaves the step in-progress and must NOT run a
// second check. The check counts its invocations and always fails, so we can
// assert it ran exactly once (the first check only — no second after the failed
// complete). The ticket stays open.
func TestFinishStepCompleteFailureSkipsCheck(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-completefail-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	checkCount := filepath.Join(t.TempDir(), "check-count.txt")
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: bad complete\n"+
		"      complete: \"echo boom-complete; exit 4\"\n"+
		"      check: \"echo x >> "+checkCount+"; exit 1\"\n")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code == 0 {
		t.Fatal("finish succeeded despite failing complete, want error")
	}
	if !strings.Contains(stderr, "complete failed") || !strings.Contains(stderr, "boom-complete") {
		t.Errorf("stderr=%q want to mention the failed complete and its output", stderr)
	}
	if got := countLines(t, checkCount); got != 1 {
		t.Errorf("check ran %d times, want 1 (first check only; no second check after failed complete)", got)
	}
	steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(steps) != 1 {
		t.Fatalf("expected one step-bead, got %v", steps)
	}
	if got := beadStatus(t, beadsDir, steps[0]); got != "in_progress" {
		t.Errorf("step status=%q want in_progress after failed complete", got)
	}
	if got := beadStatus(t, beadsDir, child); got != "open" {
		t.Errorf("ticket status=%q want still open after failed complete", got)
	}
}

// TestFinishStepFirstCheckPassesSkipsComplete: when the first check passes on
// launch the work is already done, so `complete` never runs and the step closes
// immediately.
func TestFinishStepFirstCheckPassesSkipsComplete(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-firstpass-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	completeRan := filepath.Join(t.TempDir(), "complete-ran.txt")
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: already done\n"+
		"      complete: \"touch "+completeRan+"\"\n"+
		"      check: \"true\"\n")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q (first check passes, should close)", code, stderr)
	}
	if _, err := os.Stat(completeRan); !os.IsNotExist(err) {
		t.Errorf("complete ran even though the first check passed (%s exists)", completeRan)
	}
	steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(steps) != 1 {
		t.Fatalf("expected one step-bead, got %v", steps)
	}
	if got := beadStatus(t, beadsDir, steps[0]); got != "closed" {
		t.Errorf("step status=%q want closed", got)
	}
	if got := beadStatus(t, beadsDir, child); got != "closed" {
		t.Errorf("ticket status=%q want closed", got)
	}
}

// TestFinishStepCompleteThenSecondCheckPasses: the first check fails, so
// `complete` runs and satisfies the goal, and the SECOND check passes — the step
// closes within the same launch wave.
func TestFinishStepCompleteThenSecondCheckPasses(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-secondpass-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	// The check passes only once `complete` has created the marker: the first
	// check fails (marker absent) → complete touches it → second check passes.
	marker := filepath.Join(t.TempDir(), "done.txt")
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: do work\n"+
		"      complete: \"touch "+marker+"\"\n"+
		"      check: \"test -f "+marker+"\"\n")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q (complete + second check should close)", code, stderr)
	}
	steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(steps) != 1 {
		t.Fatalf("expected one step-bead, got %v", steps)
	}
	if got := beadStatus(t, beadsDir, steps[0]); got != "closed" {
		t.Errorf("step status=%q want closed after complete + second check", got)
	}
	if got := beadStatus(t, beadsDir, child); got != "closed" {
		t.Errorf("ticket status=%q want closed", got)
	}
}

// TestFinishStepCompleteThenSecondCheckFails: the first check fails, `complete`
// succeeds, but the SECOND check still fails — the step stays in-progress.
// `complete` runs exactly once (only after the first failed check) and the check
// runs exactly twice (before and after complete).
func TestFinishStepCompleteThenSecondCheckFails(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-secondfail-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	completeCount := filepath.Join(t.TempDir(), "complete-count.txt")
	checkCount := filepath.Join(t.TempDir(), "check-count.txt")
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: never satisfied\n"+
		"      complete: \"echo x >> "+completeCount+"\"\n"+
		"      check: \"echo x >> "+checkCount+"; exit 1\"\n")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code == 0 {
		t.Fatal("finish succeeded despite a check that never passes, want error")
	}
	if !strings.Contains(stderr, "check failed") {
		t.Errorf("stderr=%q want to mention the failed check", stderr)
	}
	if got := countLines(t, completeCount); got != 1 {
		t.Errorf("complete ran %d times, want 1 (only after the first failed check)", got)
	}
	if got := countLines(t, checkCount); got != 2 {
		t.Errorf("check ran %d times, want 2 (before and after complete)", got)
	}
	steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(steps) != 1 {
		t.Fatalf("expected one step-bead, got %v", steps)
	}
	if got := beadStatus(t, beadsDir, steps[0]); got != "in_progress" {
		t.Errorf("step status=%q want in_progress", got)
	}
	if got := beadStatus(t, beadsDir, child); got != "open" {
		t.Errorf("ticket status=%q want open", got)
	}
}

// TestFinishStepCompleteRerunsOnNextCall: `complete` runs at most once per
// `this finish` call, but a later call repeats the full check → complete → check
// flow — so `complete` runs AGAIN whenever that call's first check still fails.
// Here the check passes only once `complete` has run twice (once per call), so
// the step needs two finish calls and `complete` must fire on both.
func TestFinishStepCompleteRerunsOnNextCall(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-recomplete-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	completeCount := filepath.Join(t.TempDir(), "complete-count.txt")
	// complete appends a line each run; check passes only once it has ≥2 lines.
	check := fmt.Sprintf("'[ -f %s ] && [ $(wc -l < %s) -ge 2 ]'", completeCount, completeCount)
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: needs two completes\n"+
		"      complete: \"echo x >> "+completeCount+"\"\n"+
		"      check: "+check+"\n")

	// --- Call 1: check fails → complete (count=1) → check still fails → in-progress.
	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code == 0 {
		t.Fatalf("first finish should exit non-zero (check not yet satisfied); stderr=%q", stderr)
	}
	if got := countLines(t, completeCount); got != 1 {
		t.Fatalf("complete ran %d times in the first call, want 1", got)
	}
	steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(steps) != 1 {
		t.Fatalf("expected one step-bead, got %v", steps)
	}
	assertStatus(t, beadsDir, steps[0], "in_progress")

	// --- Call 2: complete runs AGAIN (count=2) → second check passes → closes.
	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("second finish exit=%d stderr=%q (should close)", code, stderr)
	}
	if got := countLines(t, completeCount); got != 2 {
		t.Errorf("complete ran %d times total, want 2 (it must re-run on the second call)", got)
	}
	assertStatus(t, beadsDir, steps[0], "closed")
	assertStatus(t, beadsDir, child, "closed")
}

// TestFinishStepInvalidYAMLAborts: a malformed finish-steps.yaml (a step
// missing the required `check`) aborts loudly rather than silently dropping to
// the Go fallback.
func TestFinishStepInvalidYAMLAborts(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-badyaml-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: no-check step\n")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code == 0 {
		t.Fatal("finish succeeded on invalid finish-steps.yaml, want error")
	}
	if !strings.Contains(stderr, "missing `check`") {
		t.Errorf("stderr=%q want to mention the missing check field", stderr)
	}
	if got := beadStatus(t, beadsDir, child); got != "open" {
		t.Errorf("ticket status=%q want still open after config error", got)
	}
}

// TestFinishStepBlockedByUnknownTitleAborts: a blocked_by edge that names a
// non-existent step is a config error and aborts loudly.
func TestFinishStepBlockedByUnknownTitleAborts(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-badedge-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: real\n"+
		"      check: \"true\"\n"+
		"      blocked_by: [ghost]\n")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code == 0 {
		t.Fatal("finish succeeded with a blocked_by to an unknown step, want error")
	}
	if !strings.Contains(stderr, "ghost") || !strings.Contains(stderr, "not a step") {
		t.Errorf("stderr=%q want to flag the unknown blocked_by target", stderr)
	}
	if steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel); len(steps) != 0 {
		t.Errorf("expected no step-beads created on config error, got %v", steps)
	}
}

// TestFinishStepCycleAborts: a blocked_by cycle would deadlock the advance and
// is rejected at load time.
func TestFinishStepCycleAborts(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-cycle-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: x\n"+
		"      check: \"true\"\n"+
		"      blocked_by: [y]\n"+
		"    - title: y\n"+
		"      check: \"true\"\n"+
		"      blocked_by: [x]\n")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code == 0 {
		t.Fatal("finish succeeded on a blocked_by cycle, want error")
	}
	if !strings.Contains(stderr, "cycle") {
		t.Errorf("stderr=%q want to mention the blocked_by cycle", stderr)
	}
}

// TestFinishStepGlobalFallback: a finish-steps.yaml at the BEADS_DIR root
// drives the finish when the worktree's bare repo has none of its own. The
// step runs in the worktree.
func TestFinishStepGlobalFallback(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-global-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	childWT := worktreePath(bare, child)
	// No finish-steps.yaml in the bare repo; only the global one.
	marker := filepath.Join(t.TempDir(), "where.txt")
	writeGlobalStepsYAML(t, beadsDir, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: global step\n"+
		"      check: \"pwd > "+marker+"\"\n")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q", code, stderr)
	}

	steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(steps) != 1 {
		t.Fatalf("expected one finish-step child via global config, got %v", steps)
	}
	if got := beadTitle(t, beadsDir, steps[0]); got != "global step" {
		t.Errorf("step title=%q want %q (global config not used?)", got, "global step")
	}
	assertRanIn(t, marker, childWT)
}

// TestFinishStepGlobalNoWorktree exercises a top-level ticket with NO linked
// dirs. The global config still drives a step-bead; commands run in the env
// folder, and cleanup closes the ticket.
func TestFinishStepGlobalNoWorktree(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-global-nodir-child")
	mustMakeEnv(t, beadsDir, child)
	envPath := filepath.Join(beadsDir, "envs", child)

	marker := filepath.Join(t.TempDir(), "where.txt")
	writeGlobalStepsYAML(t, beadsDir, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: nodir step\n"+
		"      check: \"pwd > "+marker+"\"\n")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q", code, stderr)
	}

	steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(steps) != 1 {
		t.Fatalf("expected one finish-step child, got %v", steps)
	}
	if got := beadStatus(t, beadsDir, child); got != "closed" {
		t.Errorf("ticket status=%q want closed", got)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Errorf("env folder %s still exists after finish (err=%v)", envPath, err)
	}
	assertRanIn(t, marker, envPath)
}

// TestFinishStepNestedParentClosesAfterChildren is the headline acceptance test
// for this ticket: a parent (group) step with two leaf children only closes
// once BOTH children close, and the ticket only closes once the top-level group
// closes. The children fail their check the first time and pass the second, so
// the run spans two `this finish` calls — proving the parent stays open while
// any child is still open.
func TestFinishStepNestedParentClosesAfterChildren(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-nested-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	childWT := worktreePath(bare, child)
	envPath := filepath.Join(beadsDir, "envs", child)

	m1 := filepath.Join(t.TempDir(), "c1")
	m2 := filepath.Join(t.TempDir(), "c2")
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: PR\n"+
		"      steps:\n"+
		"        - title: lint\n"+
		"          check: "+failFirst(m1)+"\n"+
		"        - title: tests\n"+
		"          check: "+failFirst(m2)+"\n")

	// --- First call: the group launches, both children launch and their checks
	// fail → in-progress. The group must NOT close (children still open).
	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code == 0 {
		t.Fatalf("first finish should exit non-zero (children pending); stderr=%q", stderr)
	}
	top := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(top) != 1 {
		t.Fatalf("expected exactly one top-level step (the group), got %v", top)
	}
	parentID := top[0]
	assertStatus(t, beadsDir, parentID, "in_progress")
	kids := stepsByTitle(t, beadsDir, parentID)
	if len(kids) != 2 {
		t.Fatalf("expected two child steps under the group, got %v", kids)
	}
	assertStatus(t, beadsDir, kids["lint"], "in_progress")
	assertStatus(t, beadsDir, kids["tests"], "in_progress")
	assertStatus(t, beadsDir, child, "open")
	if _, err := os.Stat(childWT); err != nil {
		t.Errorf("worktree removed before the group closed: %v", err)
	}

	// --- Second call: both children pass → close, which closes the group, which
	// closes the ticket, then cleanup runs.
	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("second finish exit=%d stderr=%q (expected success)", code, stderr)
	}
	assertStatus(t, beadsDir, kids["lint"], "closed")
	assertStatus(t, beadsDir, kids["tests"], "closed")
	assertStatus(t, beadsDir, parentID, "closed")
	assertStatus(t, beadsDir, child, "closed")
	if _, err := os.Stat(childWT); !os.IsNotExist(err) {
		t.Errorf("worktree %s still present after full cleanup (err=%v)", childWT, err)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Errorf("env folder %s still present after full cleanup (err=%v)", envPath, err)
	}
}

// TestFinishStepNestedSiblingBlocking verifies blocked_by is honoured between
// siblings INSIDE a group: under one parent, step `b` is blocked_by `a`. `a`
// fails its first check, so `b` must stay blocked (open) — it only launches
// after `a` closes on the next call. Mirrors the flat three-step graph, nested.
func TestFinishStepNestedSiblingBlocking(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-nested-block-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	mA := filepath.Join(t.TempDir(), "a")
	mB := filepath.Join(t.TempDir(), "b")
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: group\n"+
		"      steps:\n"+
		"        - title: a\n"+
		"          check: "+failFirst(mA)+"\n"+
		"        - title: b\n"+
		"          check: "+failFirst(mB)+"\n"+
		"          blocked_by: [a]\n")

	// --- Call 1: a launches and fails (in-progress); b stays blocked (open).
	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code == 0 {
		t.Fatalf("first finish should exit non-zero; stderr=%q", stderr)
	}
	parentID := childrenWithLabel(t, beadsDir, child, finish.StepLabel)[0]
	kids := stepsByTitle(t, beadsDir, parentID)
	assertStatus(t, beadsDir, kids["a"], "in_progress")
	assertStatus(t, beadsDir, kids["b"], "open") // blocked: a not closed yet
	assertStatus(t, beadsDir, parentID, "in_progress")

	// --- Call 2: a passes → closes, which unblocks b; b launches and fails.
	_, stderr, code = testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code == 0 {
		t.Fatalf("second finish should exit non-zero (b pending); stderr=%q", stderr)
	}
	assertStatus(t, beadsDir, kids["a"], "closed")
	assertStatus(t, beadsDir, kids["b"], "in_progress")
	assertStatus(t, beadsDir, parentID, "in_progress")

	// --- Call 3: b passes → group closes → ticket closes.
	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("third finish exit=%d stderr=%q", code, stderr)
	}
	assertStatus(t, beadsDir, kids["b"], "closed")
	assertStatus(t, beadsDir, parentID, "closed")
	assertStatus(t, beadsDir, child, "closed")
}

// TestFinishStepDeepNestingCascades exercises unbounded depth: a three-level
// chain (L1 → L2 → L3) whose single leaf check passes immediately closes the
// whole tree — and the ticket — in one call.
func TestFinishStepDeepNestingCascades(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-deep-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: L1\n"+
		"      steps:\n"+
		"        - title: L2\n"+
		"          steps:\n"+
		"            - title: L3\n"+
		"              check: \"true\"\n")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q (deep chain should complete in one call)", code, stderr)
	}
	l1 := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(l1) != 1 {
		t.Fatalf("expected one top-level step, got %v", l1)
	}
	l2 := childrenWithLabel(t, beadsDir, l1[0], finish.StepLabel)
	if len(l2) != 1 {
		t.Fatalf("expected one L2 step, got %v", l2)
	}
	l3 := childrenWithLabel(t, beadsDir, l2[0], finish.StepLabel)
	if len(l3) != 1 {
		t.Fatalf("expected one L3 step, got %v", l3)
	}
	assertStatus(t, beadsDir, l3[0], "closed")
	assertStatus(t, beadsDir, l2[0], "closed")
	assertStatus(t, beadsDir, l1[0], "closed")
	assertStatus(t, beadsDir, child, "closed")
}

// TestFinishStepParentWithCheckAborts: a parent step (one carrying nested
// `steps`) must not also declare its own `check`/`complete` — that's a config
// error caught at load time.
func TestFinishStepParentWithCheckAborts(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-parentcheck-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: group\n"+
		"      check: \"true\"\n"+
		"      steps:\n"+
		"        - title: leaf\n"+
		"          check: \"true\"\n")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code == 0 {
		t.Fatal("finish succeeded with a parent step that has its own check, want error")
	}
	if !strings.Contains(stderr, "nested") || !strings.Contains(stderr, "check") {
		t.Errorf("stderr=%q want to flag the parent step carrying its own check", stderr)
	}
	if steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel); len(steps) != 0 {
		t.Errorf("expected no step-beads created on config error, got %v", steps)
	}
}

// --- helpers ---

// failFirst is a check command that fails (exit 1) the first time it runs —
// creating its marker file — and passes (exit 0) on every subsequent run. It
// simulates a check that only succeeds after some external state settles.
func failFirst(marker string) string {
	return fmt.Sprintf("'test -f %s && exit 0; touch %s; exit 1'", marker, marker)
}

// assertRanIn reads the cwd a step's command recorded into marker (via `pwd`)
// and asserts it is wantDir. Both sides are resolved with EvalSymlinks because
// `pwd` reports the real path while t.TempDir() may hand back a symlinked one
// (on macOS /tmp → /private/tmp).
func assertRanIn(t *testing.T, marker, wantDir string) {
	t.Helper()
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read cwd marker: %v", err)
	}
	got := resolve(t, strings.TrimSpace(string(raw)))
	want := resolve(t, wantDir)
	if got != want {
		t.Errorf("step ran in %q, want %q", got, want)
	}
}

// resolve canonicalises p by resolving symlinks. It works even when p itself
// no longer exists (cleanup may have removed the worktree by assert time): it
// resolves the longest existing ancestor and rejoins the remainder, which is
// enough to canonicalise the /tmp → /private/tmp symlink that matters here.
func resolve(t *testing.T, p string) string {
	t.Helper()
	rest := ""
	for d := p; ; {
		if r, err := filepath.EvalSymlinks(d); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(d)
		if parent == d {
			return p
		}
		rest = filepath.Join(filepath.Base(d), rest)
		d = parent
	}
}

// countLines returns the number of lines a counting command appended to path,
// or 0 if the file does not exist. Used to assert how many times a check or
// complete command ran across a finish wave.
func countLines(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read %s: %v", path, err)
	}
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return 0
	}
	return len(strings.Split(s, "\n"))
}

func writeStepsYAML(t *testing.T, bare, body string) {
	t.Helper()
	path := filepath.Join(bare, finish.StepsFilename)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// writeGlobalStepsYAML writes the project-wide finish-steps.yaml at the
// BEADS_DIR root and removes it on cleanup so it cannot leak into another test
// reusing the same pooled beadsDir.
func writeGlobalStepsYAML(t *testing.T, beadsDir, body string) {
	t.Helper()
	path := filepath.Join(beadsDir, finish.StepsFilename)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })
}

func mustMakeEnv(t *testing.T, beadsDir, id string) {
	t.Helper()
	mustRunTicket(t, beadsDir, "env", id)
}

// assertStatus fails unless bead id is in the expected status.
func assertStatus(t *testing.T, beadsDir, id, want string) {
	t.Helper()
	if got := beadStatus(t, beadsDir, id); got != want {
		t.Errorf("bead %s status=%q want %q", id, got, want)
	}
}

// stepsByTitle maps each finish-step child of ticketID to its bead id by title.
func stepsByTitle(t *testing.T, beadsDir, ticketID string) map[string]string {
	t.Helper()
	ids := childrenWithLabel(t, beadsDir, ticketID, finish.StepLabel)
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		out[beadTitle(t, beadsDir, id)] = id
	}
	return out
}

// childrenWithLabel returns the ids of beads whose parent is ticketID and
// which carry label.
func childrenWithLabel(t *testing.T, beadsDir, ticketID, label string) []string {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "list", "--all").Output()
	if err != nil {
		t.Fatalf("bd list: %v", err)
	}
	var rows []struct {
		ID     string   `json:"id"`
		Parent string   `json:"parent"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse bd list: %v\n%s", err, out)
	}
	var found []string
	for _, r := range rows {
		if r.Parent != ticketID {
			continue
		}
		for _, l := range r.Labels {
			if l == label {
				found = append(found, r.ID)
				break
			}
		}
	}
	return found
}

func beadTitle(t *testing.T, beadsDir, id string) string {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "show", id).Output()
	if err != nil {
		t.Fatalf("bd show: %v", err)
	}
	var rows []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse bd show: %v\n%s", err, out)
	}
	if len(rows) == 0 {
		return ""
	}
	return rows[0].Title
}
