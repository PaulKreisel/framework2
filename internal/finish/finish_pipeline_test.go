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

func TestFinishRunsPipelineOnMatchingParentBranch(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "pipeline-success-parent")
	child := testutil.CreateChildBead(t, beadsDir, "pipeline-success-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	parentWT := worktreePath(bare, parent)
	parentBranch := "todo/" + parent

	// Drop a sentinel file when the pipeline runs in the parent worktree;
	// then assert it exists afterwards.
	sentinel := filepath.Join(parentWT, "pipeline-touched.txt")
	writePipelineMD(t, bare, fmt.Sprintf(""+
		"- branch: %s\n"+
		"  command: |\n"+
		"    touch %q\n",
		parentBranch, sentinel))

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Errorf("sentinel %s missing after finish: %v", sentinel, err)
	}
	if bugs := findBugsLinkedTo(t, beadsDir, child); len(bugs) != 0 {
		t.Errorf("expected no bug beads after successful pipeline, got %v", bugs)
	}
	if status := beadStatus(t, beadsDir, child); status != "closed" {
		t.Errorf("child status=%q want closed", status)
	}
}

func TestFinishFailedPipelineCreatesBugBead(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "pipeline-fail-parent")
	child := testutil.CreateChildBead(t, beadsDir, "pipeline-fail-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	parentBranch := "todo/" + parent

	writePipelineMD(t, bare, fmt.Sprintf(""+
		"- branch: %s\n"+
		"  command: |\n"+
		"    echo boom-marker\n"+
		"    exit 7\n",
		parentBranch))

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q", code, stderr)
	}
	if status := beadStatus(t, beadsDir, child); status != "closed" {
		t.Errorf("child status=%q want closed (failure is recorded as a bug, not an abort)", status)
	}
	bugs := findBugsLinkedTo(t, beadsDir, child)
	if len(bugs) != 1 {
		t.Fatalf("expected exactly one bug bead linked to %s, got %v", child, bugs)
	}
	bug := bugs[0]
	desc := beadDescription(t, beadsDir, bug)
	if !strings.Contains(desc, "exit code: 7") {
		t.Errorf("bug description missing exit code: %q", desc)
	}
	if !strings.Contains(desc, "boom-marker") {
		t.Errorf("bug description missing captured output: %q", desc)
	}
	if !strings.Contains(desc, child) {
		t.Errorf("bug description should reference the finished ticket id %s: %q", child, desc)
	}
}

func TestFinishPipelineNoMatchingBranchIsNoop(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "pipeline-nomatch-parent")
	child := testutil.CreateChildBead(t, beadsDir, "pipeline-nomatch-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	writePipelineMD(t, bare, ""+
		"- branch: some/other-branch\n"+
		"  command: exit 1\n")

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q", code, stderr)
	}
	if bugs := findBugsLinkedTo(t, beadsDir, child); len(bugs) != 0 {
		t.Errorf("expected no bug beads when no entry matches, got %v", bugs)
	}
}

func TestFinishWithoutPipelineMDIsNoop(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "pipeline-absent-parent")
	child := testutil.CreateChildBead(t, beadsDir, "pipeline-absent-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	// Deliberately no pipeline.md.

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish exit=%d stderr=%q", code, stderr)
	}
	if bugs := findBugsLinkedTo(t, beadsDir, child); len(bugs) != 0 {
		t.Errorf("expected no bug beads when pipeline.md is absent, got %v", bugs)
	}
}

// TestPipelineAsyncSubcommandExecutesPayload verifies the hidden
// `ticket pipeline-async --payload <file>` entrypoint: the runner that the
// async-mode `this finish` spawns. We bypass the subprocess detach (which is
// guarded off under `go test`) and call the subcommand in-process with a
// hand-crafted payload, then assert it ran the command and filed a bug bead
// on failure.
func TestPipelineAsyncSubcommandExecutesPayload(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "pipeline-async-parent")
	bare := bareRepo(t, "main")
	mustRunTicket(t, beadsDir, "dir", "worktree", parent, bare)
	parentWT := worktreePath(bare, parent)
	parentBranch := "todo/" + parent

	payload := map[string]any{
		"ticket_id": parent,
		"tasks": []map[string]any{{
			"entry": map[string]any{
				"Branch":  parentBranch,
				"Command": "echo boom-async-marker\nexit 9\n",
			},
			"parent": map[string]any{
				"path":     parentWT,
				"branch":   parentBranch,
				"bareRepo": bare,
			},
		}},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	payloadPath := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(payloadPath, body, 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "pipeline-async", "--payload", payloadPath); code != 0 {
		t.Fatalf("pipeline-async exit=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(payloadPath); !os.IsNotExist(err) {
		t.Errorf("payload file %s should have been removed by the runner (err=%v)", payloadPath, err)
	}

	bugs := findBugsLinkedTo(t, beadsDir, parent)
	if len(bugs) != 1 {
		t.Fatalf("expected one bug bead linked to %s, got %v", parent, bugs)
	}
	desc := beadDescription(t, beadsDir, bugs[0])
	for _, want := range []string{"exit code: 9", "boom-async-marker", parent, parentWT} {
		if !strings.Contains(desc, want) {
			t.Errorf("bug description missing %q in:\n%s", want, desc)
		}
	}
}

// --- helpers ---

func writePipelineMD(t *testing.T, bare, body string) {
	t.Helper()
	path := filepath.Join(bare, "pipeline.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// findBugsLinkedTo returns the ids of bug beads whose "depends_on" set
// contains ticketID. We use `bd --json list` and filter client-side because
// the test pool may have other beads in flight.
func findBugsLinkedTo(t *testing.T, beadsDir, ticketID string) []string {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "list", "--all").Output()
	if err != nil {
		t.Fatalf("bd list: %v", err)
	}
	var rows []struct {
		ID           string `json:"id"`
		IssueType    string `json:"issue_type"`
		Dependencies []struct {
			DependsOnID string `json:"depends_on_id"`
			Type        string `json:"type"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse bd list: %v\n%s", err, out)
	}
	var found []string
	for _, r := range rows {
		if r.IssueType != "bug" {
			continue
		}
		for _, dep := range r.Dependencies {
			if dep.DependsOnID == ticketID {
				found = append(found, r.ID)
				break
			}
		}
	}
	return found
}

func beadDescription(t *testing.T, beadsDir, id string) string {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "show", id).Output()
	if err != nil {
		t.Fatalf("bd show %s: %v", id, err)
	}
	var rows []struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse bd show %s: %v\n%s", id, err, out)
	}
	if len(rows) == 0 {
		return ""
	}
	return rows[0].Description
}
