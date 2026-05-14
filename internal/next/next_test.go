package next_test

import (
	"os/exec"
	"strings"
	"testing"

	"framework2/internal/testutil"
)

func TestNextListsReadyChildren(t *testing.T) {
	testutil.SharedBdDB(t)

	parent := createBead(t, "next-parent")
	childA := createBead(t, "next-child-a")
	childB := createBead(t, "next-child-b")
	addParentChild(t, childA, parent)
	addParentChild(t, childB, parent)

	stdout, stderr, code := testutil.RunTicket(t, "next", parent)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, childA) {
		t.Errorf("output missing %s: %q", childA, stdout)
	}
	if !strings.Contains(stdout, childB) {
		t.Errorf("output missing %s: %q", childB, stdout)
	}
}

func TestNextEmptyForBeadWithoutReadyDescendants(t *testing.T) {
	testutil.SharedBdDB(t)
	bead := createBead(t, "next-no-children")

	stdout, stderr, code := testutil.RunTicket(t, "next", bead)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	if strings.Contains(stdout, bead) {
		t.Errorf("output unexpectedly contains parent id %s: %q", bead, stdout)
	}
}

func TestNextUnknownID(t *testing.T) {
	testutil.SharedBdDB(t)

	// `bd ready --parent <unknown>` exits 0 with no matching descendants;
	// `ticket next` surfaces that behavior as-is.
	stdout, stderr, code := testutil.RunTicket(t, "next", "tst-doesnotexist")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q stdout=%q", code, stderr, stdout)
	}
	if strings.Contains(stdout, "tst-doesnotexist") {
		t.Errorf("output unexpectedly references query id: %q", stdout)
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

func addParentChild(t *testing.T, child, parent string) {
	t.Helper()
	out, err := exec.Command("bd", "dep", "add", child, parent, "--type", "parent-child").CombinedOutput()
	if err != nil {
		t.Fatalf("bd dep add %s %s: %v\n%s", child, parent, err, out)
	}
}
