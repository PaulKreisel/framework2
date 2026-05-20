package pr_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"framework2/internal/pr"
	"framework2/internal/testutil"
)

// --- URL parsing ----------------------------------------------------------

func TestParseURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		wantRepo string
		wantNum  int
		wantErr  bool
	}{
		{"https://github.com/foo/bar/pull/42", "foo/bar", 42, false},
		{"http://github.com/foo/bar/pull/1", "foo/bar", 1, false},
		{"https://github.com/foo/bar/pull/9001/files", "foo/bar", 9001, false},
		{"https://github.com/foo/bar/pull/123#issuecomment-1", "foo/bar", 123, false},
		{"https://github.com/foo/bar.git/pull/7", "foo/bar", 7, false},
		{"https://example.com/foo/bar/pull/42", "", 0, true},
		{"not a url", "", 0, true},
		{"https://github.com/foo/bar/issues/42", "", 0, true},
	}
	for _, c := range cases {
		repo, num, err := pr.ParseURL(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseURL(%q): expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseURL(%q): %v", c.in, err)
			continue
		}
		if repo != c.wantRepo || num != c.wantNum {
			t.Errorf("ParseURL(%q) = (%q,%d), want (%q,%d)", c.in, repo, num, c.wantRepo, c.wantNum)
		}
	}
}

func TestRecordURL(t *testing.T) {
	t.Parallel()
	r := pr.Record{Repo: "foo/bar", Number: 42}
	if got, want := r.URL(), "https://github.com/foo/bar/pull/42"; got != want {
		t.Errorf("URL = %q, want %q", got, want)
	}
}

// --- Storage --------------------------------------------------------------

func TestLoadEmpty(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-load-empty")
	recs, err := pr.Load("", beadsDir, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if recs != nil {
		t.Errorf("Load on empty bead = %+v, want nil", recs)
	}
}

func TestLoadRoundTripViaBdUpdate(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-rt")
	want := []pr.Record{
		{Repo: "octocat/Hello-World", Number: 7, Branch: "feat/x"},
	}
	payload, _ := json.Marshal(map[string]any{"prs": want})
	if out, err := testutil.BdCmd(beadsDir, "update", id, "--metadata", string(payload)).CombinedOutput(); err != nil {
		t.Fatalf("bd update: %v: %s", err, out)
	}
	got, err := pr.Load("", beadsDir, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

// --- gh / git stubbing infrastructure -------------------------------------

// stubMutex serialises tests that swap ExecGH / ExecGit (package vars).
// Tests using stubs MUST NOT call t.Parallel().
var stubMutex sync.Mutex

type ghCall struct {
	Dir  string
	Args []string
}

type ghResp struct {
	Out []byte
	Err error
}

// installStubs replaces pr.ExecGH and pr.ExecGit with recording stubs for
// the duration of the test. responses is a map keyed by the first arg
// (e.g. "pr" / "repo") + optionally the second arg, joined by space.
func installStubs(t *testing.T, ghMap, gitMap map[string]ghResp) (ghCalls *[]ghCall, gitCalls *[]ghCall) {
	t.Helper()
	stubMutex.Lock()
	prevGH, prevGit := pr.ExecGH, pr.ExecGit
	var gh, git []ghCall
	pr.ExecGH = func(dir string, args ...string) ([]byte, error) {
		gh = append(gh, ghCall{Dir: dir, Args: args})
		return matchResp(ghMap, args)
	}
	pr.ExecGit = func(dir string, args ...string) ([]byte, error) {
		git = append(git, ghCall{Dir: dir, Args: args})
		return matchResp(gitMap, args)
	}
	t.Cleanup(func() {
		pr.ExecGH = prevGH
		pr.ExecGit = prevGit
		stubMutex.Unlock()
	})
	return &gh, &git
}

// matchResp tries longest-prefix-first on args. For args = ["pr","view",
// "--repo","o/r","1","--json","..."], it tries the full join first, then
// drops one trailing arg, etc. Returns the matched entry's response, or an
// "unhandled" error.
func matchResp(m map[string]ghResp, args []string) ([]byte, error) {
	for n := len(args); n >= 1; n-- {
		key := strings.Join(args[:n], " ")
		if r, ok := m[key]; ok {
			return r.Out, r.Err
		}
	}
	return nil, fmt.Errorf("test stub: unhandled call %v", args)
}

// makeExitErr fabricates an *exec.ExitError-like error carrying stderr.
// Used by stubs that need to simulate gh's "not found" exit.
func makeExitErr(stderr string) error {
	return &fakeExitErr{msg: stderr}
}

type fakeExitErr struct{ msg string }

func (e *fakeExitErr) Error() string { return e.msg }

// ghJSON marshals v as a one-line JSON byte slice. Helps tests look like real
// gh output.
func ghJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// --- List -----------------------------------------------------------------

func TestListPrintsRecordsAsTable(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-list")
	// Seed two PR records: one MERGED, one OPEN+isDraft, one (deleted) — we
	// stub gh pr view to drive these states.
	recs := []pr.Record{
		{Repo: "o/r", Number: 1, Branch: "f-1"},
		{Repo: "o/r", Number: 2, Branch: "f-2"},
		{Repo: "o/r", Number: 3, Branch: "f-3"},
	}
	mustSavePRs(t, beadsDir, id, recs)

	ghMap := map[string]ghResp{}
	for _, c := range []struct {
		num     int
		state   string
		isDraft bool
		exists  bool
	}{
		{1, "MERGED", false, true},
		{2, "OPEN", true, true},
		{3, "", false, false},
	} {
		num := c.num
		if !c.exists {
			ghMap[fmt.Sprintf("pr view --repo o/r %d", num)] = ghResp{Err: makeExitErr("no pull requests found")}
			continue
		}
		ghMap[fmt.Sprintf("pr view --repo o/r %d", num)] = ghResp{Out: ghJSON(t, pr.PRView{
			Number: num, State: c.state, IsDraft: c.isDraft, HeadRefName: fmt.Sprintf("f-%d", num),
		})}
	}
	// also stub the "pr view" prefix as a fallback so any combination works.
	ghMap["pr view"] = ghResp{Err: makeExitErr("unexpected pr view")}
	installStubs(t, ghMap, nil)

	var buf bytes.Buffer
	if err := pr.List("", beadsDir, id, &buf); err != nil {
		t.Fatalf("List: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "merged") {
		t.Errorf("output missing merged state:\n%s", out)
	}
	if !strings.Contains(out, "draft") {
		t.Errorf("output missing draft state:\n%s", out)
	}
	if !strings.Contains(out, "deleted") {
		t.Errorf("output missing deleted state:\n%s", out)
	}
	if !strings.Contains(out, "https://github.com/o/r/pull/1") {
		t.Errorf("output missing PR url:\n%s", out)
	}
}

// --- Link -----------------------------------------------------------------

func TestLinkAttachesPR(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-link")

	// Link doesn't require a linked dir when there are zero entries? — yes
	// it does. Link needs at least one dir to associate with. Create a fake
	// linked dir via testutil that we can pick as selector.
	mustLinkDir(t, beadsDir, id, t.TempDir())

	ghMap := map[string]ghResp{
		"pr view": ghResp{Out: ghJSON(t, pr.PRView{
			Number: 42, State: "OPEN", HeadRefName: "feat/foo", IsDraft: false,
		})},
		// nameWithOwner check: skipped because tmp dir isn't a github repo
		// → gh returns an error, Link tolerates this.
		"repo view": ghResp{Err: makeExitErr("not a github repo")},
	}
	installStubs(t, ghMap, nil)

	if err := pr.Link("", beadsDir, id, "", "https://github.com/o/r/pull/42"); err != nil {
		t.Fatalf("Link: %v", err)
	}
	recs, err := pr.Load("", beadsDir, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(recs) != 1 || recs[0].Repo != "o/r" || recs[0].Number != 42 || recs[0].Branch != "feat/foo" {
		t.Errorf("recs = %+v, want one PR o/r#42 branch feat/foo", recs)
	}
}

func TestLinkRefusesDuplicateRepo(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-link-dup")
	mustLinkDir(t, beadsDir, id, t.TempDir())
	mustSavePRs(t, beadsDir, id, []pr.Record{{Repo: "o/r", Number: 1, Branch: "x"}})

	ghMap := map[string]ghResp{
		"pr view":   ghResp{Out: ghJSON(t, pr.PRView{Number: 2, State: "OPEN", HeadRefName: "y"})},
		"repo view": ghResp{Err: makeExitErr("not a github repo")},
	}
	installStubs(t, ghMap, nil)

	err := pr.Link("", beadsDir, id, "", "https://github.com/o/r/pull/2")
	if err == nil {
		t.Fatal("Link succeeded, want duplicate-repo error")
	}
	if !strings.Contains(err.Error(), "already has a PR") {
		t.Errorf("error = %v, want 'already has a PR' substring", err)
	}
}

// --- CreateBead -----------------------------------------------------------

func TestCreateBeadSpawnsChild(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "pr-cb-parent")

	ghMap := map[string]ghResp{
		"pr view": ghResp{Out: ghJSON(t, pr.PRView{
			Number: 99, State: "OPEN", HeadRefName: "feat/bar", Title: "Add bar",
		})},
	}
	installStubs(t, ghMap, nil)

	var buf bytes.Buffer
	if err := pr.CreateBead("", beadsDir, parent, "https://github.com/o/r/pull/99", &buf); err != nil {
		t.Fatalf("CreateBead: %v", err)
	}
	childID := strings.TrimSpace(buf.String())
	if childID == "" {
		t.Fatal("no child id printed")
	}
	// Register child cleanup so bd close runs in t.Cleanup order.
	t.Cleanup(func() { _ = testutil.BdCmd(beadsDir, "close", childID).Run() })

	// Child has the PR attached and the right title.
	recs, err := pr.Load("", beadsDir, childID)
	if err != nil {
		t.Fatalf("Load child: %v", err)
	}
	if len(recs) != 1 || recs[0].Repo != "o/r" || recs[0].Number != 99 || recs[0].Branch != "feat/bar" {
		t.Errorf("child recs = %+v, want o/r#99/feat/bar", recs)
	}
	title := beadTitle(t, beadsDir, childID)
	if title != "PR Add bar" {
		t.Errorf("child title = %q, want %q", title, "PR Add bar")
	}
}

// --- Refresh --------------------------------------------------------------

func TestRefreshValidationFailsWithoutPRs(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-refresh-missing")
	mustLinkDir(t, beadsDir, id, t.TempDir())

	// No gh calls expected — validation should fail before reporting.
	installStubs(t, nil, nil)

	var buf bytes.Buffer
	err := pr.Refresh("", beadsDir, id, &buf)
	if err == nil {
		t.Fatal("Refresh succeeded with missing PR, want error")
	}
	if !strings.Contains(err.Error(), "without an attached PR") {
		t.Errorf("error = %v, want 'without an attached PR'", err)
	}
}

func TestRefreshSurfacesClosedUnmerged(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-refresh-closed")
	// No linked dirs → validation passes (zero dirs => zero offenders).
	mustSavePRs(t, beadsDir, id, []pr.Record{{Repo: "o/r", Number: 5, Branch: "x"}})

	ghMap := map[string]ghResp{
		"pr view": ghResp{Out: ghJSON(t, pr.PRView{Number: 5, State: "CLOSED", HeadRefName: "x"})},
	}
	installStubs(t, ghMap, nil)

	var buf bytes.Buffer
	err := pr.Refresh("", beadsDir, id, &buf)
	if err == nil {
		t.Fatal("Refresh succeeded on closed-unmerged PR, want error")
	}
	if !strings.Contains(buf.String(), "closed without merge") {
		t.Errorf("output = %q, want 'closed without merge'", buf.String())
	}
}

func TestRefreshDeletedPR(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-refresh-deleted")
	mustSavePRs(t, beadsDir, id, []pr.Record{{Repo: "o/r", Number: 5, Branch: "x"}})

	ghMap := map[string]ghResp{
		"pr view": ghResp{Err: makeExitErr("no pull requests found")},
	}
	installStubs(t, ghMap, nil)

	var buf bytes.Buffer
	err := pr.Refresh("", beadsDir, id, &buf)
	if err == nil {
		t.Fatal("Refresh succeeded on deleted PR, want error")
	}
	if !strings.Contains(buf.String(), "deleted on GitHub") {
		t.Errorf("output = %q, want 'deleted on GitHub'", buf.String())
	}
}

func TestRefreshOpenChildrenBlockAutoClose(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "pr-refresh-parent")
	child := testutil.CreateChildBead(t, beadsDir, "pr-refresh-child", parent)
	_ = child
	mustSavePRs(t, beadsDir, parent, []pr.Record{{Repo: "o/r", Number: 1, Branch: "x"}})

	ghMap := map[string]ghResp{
		"pr view": ghResp{Out: ghJSON(t, pr.PRView{Number: 1, State: "MERGED", HeadRefName: "x"})},
	}
	installStubs(t, ghMap, nil)

	var buf bytes.Buffer
	err := pr.Refresh("", beadsDir, parent, &buf)
	if err == nil {
		t.Fatal("Refresh succeeded with open child, want error")
	}
	if !strings.Contains(buf.String(), "blocking auto-close") {
		t.Errorf("output = %q, want 'blocking auto-close'", buf.String())
	}
}

// --- helpers --------------------------------------------------------------

// mustSavePRs writes recs to ticketID's metadata.prs via bd update. Mirrors
// the storage shape pr.save uses internally.
func mustSavePRs(t *testing.T, beadsDir, ticketID string, recs []pr.Record) {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"prs": recs})
	if out, err := testutil.BdCmd(beadsDir, "update", ticketID, "--metadata", string(payload)).CombinedOutput(); err != nil {
		t.Fatalf("seed prs metadata: %v: %s", err, out)
	}
}

// mustLinkDir links path to ticketID via the in-process ticket dispatcher.
func mustLinkDir(t *testing.T, beadsDir, ticketID, path string) {
	t.Helper()
	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticketID, path); code != 0 {
		t.Fatalf("dir link: exit=%d stderr=%q", code, stderr)
	}
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
		t.Fatalf("parse: %v", err)
	}
	if len(rows) == 0 {
		return ""
	}
	return rows[0].Title
}

