package ticketcli_test

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"framework2/internal/testutil"
)

func TestCreateChild(t *testing.T) {
	_ = testutil.SharedBdDB(t)
	parent := createBead(t, "create-child-parent")

	out, errOut, code := testutil.RunTicket(t, "create", "child", parent, "child of parent")
	if code != 0 {
		t.Fatalf("ticket create child: exit=%d stderr=%q", code, errOut)
	}
	childID := strings.TrimSpace(out)
	if childID == "" {
		t.Fatalf("create child produced no id; stderr=%q", errOut)
	}
	t.Cleanup(func() { _ = exec.Command("bd", "close", childID).Run() })

	if got := loadParent(t, childID); got != parent {
		t.Errorf("child %s parent = %q, want %q", childID, got, parent)
	}
}

func TestCreateFollowup(t *testing.T) {
	_ = testutil.SharedBdDB(t)
	current := createBead(t, "create-followup-current")

	out, errOut, code := testutil.RunTicket(t, "create", "followup", current, "the followup")
	if code != 0 {
		t.Fatalf("ticket create followup: exit=%d stderr=%q", code, errOut)
	}
	followID := strings.TrimSpace(out)
	if followID == "" {
		t.Fatalf("create followup produced no id; stderr=%q", errOut)
	}
	t.Cleanup(func() { _ = exec.Command("bd", "close", followID).Run() })

	// followup should depend on current (current blocks followup), and must
	// not have current as parent.
	if got := loadParent(t, followID); got == current {
		t.Errorf("followup unexpectedly has current as parent (%s)", current)
	}
	if !hasDependency(t, followID, current) {
		t.Errorf("followup %s missing dependency on %s", followID, current)
	}
}

func TestCreateSiblingWithParent(t *testing.T) {
	_ = testutil.SharedBdDB(t)
	parent := createBead(t, "create-sibling-parent")
	// Create the current ticket as a child of parent first.
	out, errOut, code := testutil.RunTicket(t, "create", "child", parent, "current of sibling test")
	if code != 0 {
		t.Fatalf("seed child: exit=%d stderr=%q", code, errOut)
	}
	current := strings.TrimSpace(out)
	t.Cleanup(func() { _ = exec.Command("bd", "close", current).Run() })

	out, errOut, code = testutil.RunTicket(t, "create", "sibling", current, "the sibling")
	if code != 0 {
		t.Fatalf("ticket create sibling: exit=%d stderr=%q", code, errOut)
	}
	sib := strings.TrimSpace(out)
	t.Cleanup(func() { _ = exec.Command("bd", "close", sib).Run() })

	if got := loadParent(t, sib); got != parent {
		t.Errorf("sibling %s parent = %q, want %q", sib, got, parent)
	}
}

func TestCreateSiblingWithoutParent(t *testing.T) {
	_ = testutil.SharedBdDB(t)
	current := createBead(t, "create-sibling-no-parent")

	out, errOut, code := testutil.RunTicket(t, "create", "sibling", current, "top-level sibling")
	if code != 0 {
		t.Fatalf("ticket create sibling: exit=%d stderr=%q", code, errOut)
	}
	sib := strings.TrimSpace(out)
	t.Cleanup(func() { _ = exec.Command("bd", "close", sib).Run() })

	if got := loadParent(t, sib); got != "" {
		t.Errorf("top-level sibling unexpectedly has parent %q", got)
	}
}

func TestCreateUnknownRelation(t *testing.T) {
	_ = testutil.SharedBdDB(t)
	current := createBead(t, "create-unknown-rel")

	_, errOut, code := testutil.RunTicket(t, "create", "nephew", current, "x")
	if code == 0 {
		t.Fatalf("expected nonzero exit for unknown relation")
	}
	if !strings.Contains(errOut, "unknown create relation") {
		t.Errorf("stderr missing message; got %q", errOut)
	}
}

// loadParent returns the parent id of ticketID, or "" if it has none.
func loadParent(t *testing.T, ticketID string) string {
	t.Helper()
	out, err := exec.Command("bd", "--json", "show", ticketID).Output()
	if err != nil {
		t.Fatalf("bd show %s: %v", ticketID, err)
	}
	var rows []struct {
		Parent string `json:"parent"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse bd show: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("bd show %s: no rows", ticketID)
	}
	return rows[0].Parent
}

// hasDependency reports whether ticketID depends on otherID (any
// dependency_type).
func hasDependency(t *testing.T, ticketID, otherID string) bool {
	t.Helper()
	out, err := exec.Command("bd", "--json", "show", ticketID).Output()
	if err != nil {
		t.Fatalf("bd show %s: %v", ticketID, err)
	}
	var rows []struct {
		Dependencies []struct {
			ID string `json:"id"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse bd show: %v", err)
	}
	if len(rows) == 0 {
		return false
	}
	for _, d := range rows[0].Dependencies {
		if d.ID == otherID {
			return true
		}
	}
	return false
}
