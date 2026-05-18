package ticketcli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"framework2/internal/testutil"
)

func TestCreateChild(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "create-child-parent")

	out, errOut, code := testutil.RunTicketIn(t, beadsDir, "create", "child", parent, "Child of Parent", "--description", "child body")
	if code != 0 {
		t.Fatalf("ticket create child: exit=%d stderr=%q", code, errOut)
	}
	childID := strings.TrimSpace(out)
	if childID == "" {
		t.Fatalf("create child produced no id; stderr=%q", errOut)
	}
	t.Cleanup(func() { _ = testutil.BdCmd(beadsDir, "close", childID).Run() })

	if got := loadParent(t, beadsDir, childID); got != parent {
		t.Errorf("child %s parent = %q, want %q", childID, got, parent)
	}
	if got := loadSlug(t, beadsDir, childID); got != "child-of-parent" {
		t.Errorf("child %s slug = %q, want %q", childID, got, "child-of-parent")
	}
	if got := loadDescription(t, beadsDir, childID); got != "child body" {
		t.Errorf("child %s description = %q, want %q", childID, got, "child body")
	}
}

func TestCreateChildPersistsDerivedSlug(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "slug-derive-parent")

	out, errOut, code := testutil.RunTicketIn(t, beadsDir, "create", "child", parent,
		"Fix the !!! BROKEN logger, please", "--description", "fix it")
	if code != 0 {
		t.Fatalf("create child: exit=%d stderr=%q", code, errOut)
	}
	id := strings.TrimSpace(out)
	t.Cleanup(func() { _ = testutil.BdCmd(beadsDir, "close", id).Run() })

	if got := loadSlug(t, beadsDir, id); got != "fix-the-broken-logger-please" {
		t.Errorf("slug = %q, want %q", got, "fix-the-broken-logger-please")
	}
	if used := loadSlugUsed(t, beadsDir, id); used {
		t.Errorf("slug_is_used should be false on a fresh bead")
	}
}

func TestCreateFollowupUnderSharedParent(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "create-followup-parent")
	out, errOut, code := testutil.RunTicketIn(t, beadsDir, "create", "child", parent, "current for followup", "--description", "seed")
	if code != 0 {
		t.Fatalf("seed child: exit=%d stderr=%q", code, errOut)
	}
	current := strings.TrimSpace(out)
	t.Cleanup(func() { _ = testutil.BdCmd(beadsDir, "close", current).Run() })

	out, errOut, code = testutil.RunTicketIn(t, beadsDir, "create", "followup", current, "the followup", "--description", "follow-up body")
	if code != 0 {
		t.Fatalf("ticket create followup: exit=%d stderr=%q", code, errOut)
	}
	followID := strings.TrimSpace(out)
	t.Cleanup(func() { _ = testutil.BdCmd(beadsDir, "close", followID).Run() })

	// followup must share current's parent AND be blocked by current.
	if got := loadParent(t, beadsDir, followID); got != parent {
		t.Errorf("followup %s parent = %q, want %q (sibling of current)", followID, got, parent)
	}
	if !hasDependency(t, beadsDir, followID, current) {
		t.Errorf("followup %s missing dependency on %s", followID, current)
	}
}

func TestCreateFollowupTopLevel(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	current := testutil.CreateBead(t, beadsDir, "create-followup-no-parent")

	out, errOut, code := testutil.RunTicketIn(t, beadsDir, "create", "followup", current, "top-level followup", "--description", "top-level body")
	if code != 0 {
		t.Fatalf("ticket create followup: exit=%d stderr=%q", code, errOut)
	}
	followID := strings.TrimSpace(out)
	t.Cleanup(func() { _ = testutil.BdCmd(beadsDir, "close", followID).Run() })

	if got := loadParent(t, beadsDir, followID); got != "" {
		t.Errorf("followup unexpectedly has parent %q (current has none → top-level)", got)
	}
	if !hasDependency(t, beadsDir, followID, current) {
		t.Errorf("followup %s missing dependency on %s", followID, current)
	}
}

func TestCreateFollowupNotBlocked(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "create-followup-notblocked-parent")
	out, errOut, code := testutil.RunTicketIn(t, beadsDir, "create", "child", parent, "current for notblocked", "--description", "seed")
	if code != 0 {
		t.Fatalf("seed child: exit=%d stderr=%q", code, errOut)
	}
	current := strings.TrimSpace(out)
	t.Cleanup(func() { _ = testutil.BdCmd(beadsDir, "close", current).Run() })

	out, errOut, code = testutil.RunTicketIn(t, beadsDir, "create", "followup", current, "--not-blocked", "plain sibling", "--description", "sibling body")
	if code != 0 {
		t.Fatalf("ticket create followup --not-blocked: exit=%d stderr=%q", code, errOut)
	}
	sib := strings.TrimSpace(out)
	t.Cleanup(func() { _ = testutil.BdCmd(beadsDir, "close", sib).Run() })

	if got := loadParent(t, beadsDir, sib); got != parent {
		t.Errorf("followup --not-blocked %s parent = %q, want %q", sib, got, parent)
	}
	if hasDependency(t, beadsDir, sib, current) {
		t.Errorf("followup --not-blocked %s should not depend on %s", sib, current)
	}
}

func TestCreateChildRejectsNotBlocked(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	current := testutil.CreateBead(t, beadsDir, "create-child-rejects-flag")

	_, errOut, code := testutil.RunTicketIn(t, beadsDir, "create", "child", current, "--not-blocked", "x", "--description", "d")
	if code == 0 {
		t.Fatalf("expected nonzero exit for --not-blocked on child")
	}
	if !strings.Contains(errOut, "--not-blocked is only valid for") {
		t.Errorf("stderr missing message; got %q", errOut)
	}
}

func TestCreateUnknownRelation(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	current := testutil.CreateBead(t, beadsDir, "create-unknown-rel")

	_, errOut, code := testutil.RunTicketIn(t, beadsDir, "create", "sibling", current, "x", "--description", "d")
	if code == 0 {
		t.Fatalf("expected nonzero exit for unknown relation")
	}
	if !strings.Contains(errOut, "unknown create relation") {
		t.Errorf("stderr missing message; got %q", errOut)
	}
}

func TestCreateChildRejectsMissingDescription(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "create-child-no-desc")

	_, errOut, code := testutil.RunTicketIn(t, beadsDir, "create", "child", parent, "no description here")
	if code == 0 {
		t.Fatalf("expected nonzero exit when --description is omitted")
	}
	if !strings.Contains(errOut, "--description") {
		t.Errorf("stderr missing --description message; got %q", errOut)
	}
}

func TestCreateChildRejectsEmptyDescription(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "create-child-empty-desc")

	_, errOut, code := testutil.RunTicketIn(t, beadsDir, "create", "child", parent, "empty description", "--description", "")
	if code == 0 {
		t.Fatalf("expected nonzero exit when --description is empty")
	}
	if !strings.Contains(errOut, "--description") {
		t.Errorf("stderr missing --description message; got %q", errOut)
	}
}

func TestCreateFollowupRejectsMissingDescription(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	current := testutil.CreateBead(t, beadsDir, "create-followup-no-desc")

	_, errOut, code := testutil.RunTicketIn(t, beadsDir, "create", "followup", current, "no description here")
	if code == 0 {
		t.Fatalf("expected nonzero exit when --description is omitted")
	}
	if !strings.Contains(errOut, "--description") {
		t.Errorf("stderr missing --description message; got %q", errOut)
	}
}

// loadDescription returns the bead's description field as stored by bd.
func loadDescription(t *testing.T, beadsDir, ticketID string) string {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "show", ticketID).Output()
	if err != nil {
		t.Fatalf("bd show %s: %v", ticketID, err)
	}
	var rows []struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse bd show: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("bd show %s: no rows", ticketID)
	}
	return rows[0].Description
}

// loadParent returns the parent id of ticketID, or "" if it has none.
func loadParent(t *testing.T, beadsDir, ticketID string) string {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "show", ticketID).Output()
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

// loadSlug returns the slug stored in the bead's metadata.
func loadSlug(t *testing.T, beadsDir, ticketID string) string {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "show", ticketID).Output()
	if err != nil {
		t.Fatalf("bd show %s: %v", ticketID, err)
	}
	var rows []struct {
		Metadata struct {
			Slug string `json:"slug"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse bd show: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("bd show %s: no rows", ticketID)
	}
	return rows[0].Metadata.Slug
}

// loadSlugUsed returns the slug_is_used flag stored in the bead's metadata.
func loadSlugUsed(t *testing.T, beadsDir, ticketID string) bool {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "show", ticketID).Output()
	if err != nil {
		t.Fatalf("bd show %s: %v", ticketID, err)
	}
	var rows []struct {
		Metadata struct {
			SlugIsUsed bool `json:"slug_is_used"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse bd show: %v", err)
	}
	if len(rows) == 0 {
		return false
	}
	return rows[0].Metadata.SlugIsUsed
}

// hasDependency reports whether ticketID depends on otherID (any
// dependency_type).
func hasDependency(t *testing.T, beadsDir, ticketID, otherID string) bool {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "show", ticketID).Output()
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
