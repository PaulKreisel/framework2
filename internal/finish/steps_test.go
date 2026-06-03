package finish_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/finish"
	"framework2/internal/testutil"
)

// TestFinishStepBeadHappyPath is the walking-skeleton acceptance test: a
// finish-steps.yaml whose selector matches drives a single step-bead, which is
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

	// complete records its cwd to a marker OUTSIDE the worktree (so the tree
	// stays clean and removable); check fails unless that marker exists,
	// proving complete ran first. We assert the recorded cwd is the worktree.
	marker := filepath.Join(t.TempDir(), "where.txt")
	writeStepsYAML(t, bare, ""+
		"- selector: \"true\"\n"+
		"  title: skeleton step\n"+
		"  complete: \"pwd > " + marker + "\"\n"+
		"  check: \"test -f " + marker + "\"\n")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q", code, stderr)
	}

	// complete and check ran in the worktree.
	if got, err := os.ReadFile(marker); err != nil {
		t.Errorf("read cwd marker: %v", err)
	} else if strings.TrimSpace(string(got)) != childWT {
		t.Errorf("complete ran in %q, want worktree %q", strings.TrimSpace(string(got)), childWT)
	}

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

// TestFinishStepFirstSelectorWins regresses the "first exit-0 wins" rule: the
// first entry's selector fails, the second matches, so the second step runs.
func TestFinishStepFirstSelectorWins(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-select-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	writeStepsYAML(t, bare, ""+
		"- selector: \"false\"\n"+
		"  title: first never\n"+
		"  check: \"true\"\n"+
		"- selector: \"true\"\n"+
		"  title: second wins\n"+
		"  check: \"true\"\n")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q", code, stderr)
	}

	steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(steps) != 1 {
		t.Fatalf("expected one finish-step child, got %v", steps)
	}
	if got := beadTitle(t, beadsDir, steps[0]); got != "second wins" {
		t.Errorf("ran step %q, want the second (first exit-0 wins)", got)
	}
}

// TestFinishStepNoSelectorMatchFallsBack: a present finish-steps.yaml whose
// selectors all fail must fall back to the Go path — no step-bead, ticket
// still finishes normally.
func TestFinishStepNoSelectorMatchFallsBack(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "step-nomatch-parent")
	child := testutil.CreateChildBead(t, beadsDir, "step-nomatch-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	childWT := worktreePath(bare, child)
	writeStepsYAML(t, bare, ""+
		"- selector: \"false\"\n"+
		"  check: \"true\"\n")

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

// TestFinishStepCheckFailureLeavesBeadsOpen: a failing check aborts the finish
// — the step-bead and the ticket stay open and the worktree is not removed.
func TestFinishStepCheckFailureLeavesBeadsOpen(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-checkfail-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	childWT := worktreePath(bare, child)
	writeStepsYAML(t, bare, ""+
		"- selector: \"true\"\n"+
		"  title: failing check\n"+
		"  check: \"echo nope-marker; exit 3\"\n")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code == 0 {
		t.Fatal("finish succeeded despite failing check, want error")
	}
	if !strings.Contains(stderr, "check command failed") || !strings.Contains(stderr, "nope-marker") {
		t.Errorf("stderr=%q want to mention the failed check and its output", stderr)
	}

	steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel)
	if len(steps) != 1 {
		t.Fatalf("expected the step-bead to have been created, got %v", steps)
	}
	if got := beadStatus(t, beadsDir, steps[0]); got != "open" {
		t.Errorf("step-bead status=%q want open after failed check", got)
	}
	if got := beadStatus(t, beadsDir, child); got != "open" {
		t.Errorf("ticket status=%q want still open after failed check", got)
	}
	if _, err := os.Stat(childWT); err != nil {
		t.Errorf("worktree removed despite failed check: %v", err)
	}
}

// TestFinishStepCompleteFailureAbortsBeforeCheck: a failing complete aborts
// before check is reached, leaving the step-bead open.
func TestFinishStepCompleteFailureAbortsBeforeCheck(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-completefail-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	writeStepsYAML(t, bare, ""+
		"- selector: \"true\"\n"+
		"  complete: \"echo boom-complete; exit 4\"\n"+
		"  check: \"echo should-not-run > check-ran.txt\"\n")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code == 0 {
		t.Fatal("finish succeeded despite failing complete, want error")
	}
	if !strings.Contains(stderr, "complete command failed") || !strings.Contains(stderr, "boom-complete") {
		t.Errorf("stderr=%q want to mention the failed complete and its output", stderr)
	}
	if _, err := os.Stat(filepath.Join(worktreePath(bare, child), "check-ran.txt")); !os.IsNotExist(err) {
		t.Errorf("check ran despite failed complete (check-ran.txt exists)")
	}
	if got := beadStatus(t, beadsDir, child); got != "open" {
		t.Errorf("ticket status=%q want still open after failed complete", got)
	}
}

// TestFinishStepInvalidYAMLAborts: a malformed finish-steps.yaml (missing the
// required `check`) aborts loudly rather than silently dropping to the Go
// fallback.
func TestFinishStepInvalidYAMLAborts(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-badyaml-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	writeStepsYAML(t, bare, "- selector: \"true\"\n")

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
		"- selector: \"true\"\n"+
		"  title: global step\n"+
		"  check: \"pwd > "+marker+"\"\n")

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
	if got, err := os.ReadFile(marker); err != nil {
		t.Errorf("read cwd marker: %v", err)
	} else if strings.TrimSpace(string(got)) != childWT {
		t.Errorf("global step ran in %q, want worktree %q", strings.TrimSpace(string(got)), childWT)
	}
}

// TestFinishStepGlobalNoWorktree exercises the case the operator wants to try:
// a top-level ticket with NO linked dirs. The global config still drives a
// step-bead; commands run in the env folder, and cleanup closes the ticket.
func TestFinishStepGlobalNoWorktree(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "step-global-nodir-child")
	mustMakeEnv(t, beadsDir, child)
	envPath := filepath.Join(beadsDir, "envs", child)

	marker := filepath.Join(t.TempDir(), "where.txt")
	writeGlobalStepsYAML(t, beadsDir, ""+
		"- selector: \"true\"\n"+
		"  title: nodir step\n"+
		"  check: \"pwd > "+marker+"\"\n")

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
	if got, err := os.ReadFile(marker); err != nil {
		t.Errorf("read cwd marker: %v", err)
	} else if strings.TrimSpace(string(got)) != envPath {
		t.Errorf("step ran in %q, want env folder %q", strings.TrimSpace(string(got)), envPath)
	}
}

// --- helpers ---

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
