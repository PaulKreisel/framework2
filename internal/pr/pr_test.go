package pr_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
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
	if !slices.Contains(beadLabels(t, beadsDir, childID), pr.Label) {
		t.Errorf("child missing %q label", pr.Label)
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

// --- Check -----------------------------------------------------------------

func TestCheckPassesWhenAllDirsHavePR(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-check-ok")
	mustLinkDir(t, beadsDir, id, t.TempDir())
	mustSavePRs(t, beadsDir, id, []pr.Record{{Repo: "o/r", Number: 1, Branch: "x"}})

	// Single dir → single repo, so the repo-view stub can be keyed by args.
	installStubs(t, map[string]ghResp{
		"repo view": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
	}, nil)

	var buf bytes.Buffer
	if err := pr.Check("", beadsDir, id, false, &buf); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !strings.Contains(buf.String(), "every linked dir has an attached PR") {
		t.Errorf("stdout = %q, want pass message", buf.String())
	}
}

func TestCheckFailsWhenDirHasNoPR(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-check-missing")
	missing := t.TempDir()
	mustLinkDir(t, beadsDir, id, missing)
	// No PR records at all.

	installStubs(t, map[string]ghResp{
		"repo view": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
	}, nil)

	var buf bytes.Buffer
	err := pr.Check("", beadsDir, id, false, &buf)
	if err == nil {
		t.Fatal("Check succeeded with a dir missing a PR, want error")
	}
	if !strings.Contains(err.Error(), "without an attached PR") {
		t.Errorf("error = %v, want 'without an attached PR'", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error = %v, want offending dir %q listed", err, missing)
	}
}

func TestCheckMergedPassesWhenMerged(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-check-merged-ok")
	mustLinkDir(t, beadsDir, id, t.TempDir())
	mustSavePRs(t, beadsDir, id, []pr.Record{{Repo: "o/r", Number: 1, Branch: "x"}})

	installStubs(t, map[string]ghResp{
		"repo view": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
		"pr view": ghResp{Out: ghJSON(t, pr.PRView{Number: 1, State: "MERGED", HeadRefName: "x"})},
	}, nil)

	var buf bytes.Buffer
	if err := pr.Check("", beadsDir, id, true, &buf); err != nil {
		t.Fatalf("Check --merged: %v", err)
	}
	if !strings.Contains(buf.String(), "every linked dir has a merged PR") {
		t.Errorf("stdout = %q, want merged pass message", buf.String())
	}
}

func TestCheckMergedPassesWhenDeleted(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-check-merged-deleted")
	mustLinkDir(t, beadsDir, id, t.TempDir())
	mustSavePRs(t, beadsDir, id, []pr.Record{{Repo: "o/r", Number: 1, Branch: "x"}})

	// A deleted-on-GitHub PR is treated as merged-then-cleaned-up.
	installStubs(t, map[string]ghResp{
		"repo view": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
		"pr view": ghResp{Err: makeExitErr("no pull requests found")},
	}, nil)

	var buf bytes.Buffer
	if err := pr.Check("", beadsDir, id, true, &buf); err != nil {
		t.Fatalf("Check --merged on deleted PR: %v", err)
	}
	if !strings.Contains(buf.String(), "every linked dir has a merged PR") {
		t.Errorf("stdout = %q, want merged pass message", buf.String())
	}
}

func TestCheckMergedFailsWhenOpen(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-check-merged-open")
	open := t.TempDir()
	mustLinkDir(t, beadsDir, id, open)
	mustSavePRs(t, beadsDir, id, []pr.Record{{Repo: "o/r", Number: 1, Branch: "x"}})

	installStubs(t, map[string]ghResp{
		"repo view": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
		"pr view": ghResp{Out: ghJSON(t, pr.PRView{Number: 1, State: "OPEN", HeadRefName: "x"})},
	}, nil)

	var buf bytes.Buffer
	err := pr.Check("", beadsDir, id, true, &buf)
	if err == nil {
		t.Fatal("Check --merged succeeded with an open PR, want error")
	}
	if !strings.Contains(err.Error(), "without a merged PR") {
		t.Errorf("error = %v, want 'without a merged PR'", err)
	}
	if !strings.Contains(err.Error(), open) {
		t.Errorf("error = %v, want offending dir %q listed", err, open)
	}
}

func TestCheckMergedFailsWhenClosedUnmerged(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-check-merged-closed")
	mustLinkDir(t, beadsDir, id, t.TempDir())
	mustSavePRs(t, beadsDir, id, []pr.Record{{Repo: "o/r", Number: 1, Branch: "x"}})

	installStubs(t, map[string]ghResp{
		"repo view": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
		"pr view": ghResp{Out: ghJSON(t, pr.PRView{Number: 1, State: "CLOSED", HeadRefName: "x"})},
	}, nil)

	var buf bytes.Buffer
	err := pr.Check("", beadsDir, id, true, &buf)
	if err == nil {
		t.Fatal("Check --merged succeeded with a closed-unmerged PR, want error")
	}
	if !strings.Contains(err.Error(), "without a merged PR") {
		t.Errorf("error = %v, want 'without a merged PR'", err)
	}
}

// TestCheckCLIExitCodes drives the check through the CLI dispatcher to confirm
// the exit code (0 on pass, non-zero on failure) and --merged flag parsing.
func TestCheckCLIExitCodes(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-check-cli")
	mustLinkDir(t, beadsDir, id, t.TempDir())
	mustSavePRs(t, beadsDir, id, []pr.Record{{Repo: "o/r", Number: 1, Branch: "x"}})

	installStubs(t, map[string]ghResp{
		"repo view": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
		"pr view": ghResp{Out: ghJSON(t, pr.PRView{Number: 1, State: "OPEN", HeadRefName: "x"})},
	}, nil)

	// Default mode: dir has a PR → exit 0.
	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "pr", "check", id); code != 0 {
		t.Errorf("pr check: exit=%d stderr=%q, want 0", code, stderr)
	}
	// --merged: PR is open → non-zero exit.
	if _, _, code := testutil.RunTicketIn(t, beadsDir, "pr", "check", id, "--merged"); code == 0 {
		t.Error("pr check --merged: exit=0, want non-zero (PR is open)")
	}
}

// --- Show / ShowAll --------------------------------------------------------

// stubOpenURL replaces pr.OpenURL with a recorder for the duration of the
// test. Returns a pointer to the slice of URLs that the production code
// called OpenURL with.
func stubOpenURL(t *testing.T) *[]string {
	t.Helper()
	var got []string
	prev := pr.OpenURL
	pr.OpenURL = func(url string) error {
		got = append(got, url)
		return nil
	}
	t.Cleanup(func() { pr.OpenURL = prev })
	return &got
}

func TestShowOpensAttachedPR(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-show")
	tmp := t.TempDir()
	mustLinkDir(t, beadsDir, id, tmp)
	mustSavePRs(t, beadsDir, id, []pr.Record{{Repo: "o/r", Number: 42, Branch: "feat"}})

	ghMap := map[string]ghResp{
		"repo view": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
	}
	installStubs(t, ghMap, nil)
	opened := stubOpenURL(t)

	var buf bytes.Buffer
	if err := pr.Show("", beadsDir, id, "", &buf); err != nil {
		t.Fatalf("Show: %v", err)
	}
	if len(*opened) != 1 || (*opened)[0] != "https://github.com/o/r/pull/42" {
		t.Errorf("opened = %v, want [https://github.com/o/r/pull/42]", *opened)
	}
	if !strings.Contains(buf.String(), "https://github.com/o/r/pull/42") {
		t.Errorf("stdout = %q, want PR url printed", buf.String())
	}
}

func TestShowErrorsWhenNoPRAttached(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-show-none")
	tmp := t.TempDir()
	mustLinkDir(t, beadsDir, id, tmp)

	ghMap := map[string]ghResp{
		"repo view": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
	}
	installStubs(t, ghMap, nil)
	opened := stubOpenURL(t)

	var buf bytes.Buffer
	err := pr.Show("", beadsDir, id, "", &buf)
	if err == nil {
		t.Fatal("Show succeeded with no attached PR, want error")
	}
	if !strings.Contains(err.Error(), "no PR attached") {
		t.Errorf("error = %v, want 'no PR attached'", err)
	}
	if len(*opened) != 0 {
		t.Errorf("opened = %v, want no calls", *opened)
	}
}

func TestShowAllOpensEligibleWithPR(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-show-all")
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	mustLinkDir(t, beadsDir, id, dir1)
	mustLinkDir(t, beadsDir, id, dir2)
	mustSavePRs(t, beadsDir, id, []pr.Record{
		{Repo: "o/a", Number: 1, Branch: "b1"},
		{Repo: "o/b", Number: 2, Branch: "b2"},
	})

	// Each dir reports a different repo so the records pair 1:1 with dirs.
	ghMap := map[string]ghResp{}
	// We can't key nameWithOwner by dir in matchResp (it keys on args, not
	// Dir). Two dirs needing different "o/a" vs "o/b" responses: use a
	// per-dir delegate via a custom ExecGH.
	stubMutex.Lock()
	prevGH, prevGit := pr.ExecGH, pr.ExecGit
	t.Cleanup(func() {
		pr.ExecGH = prevGH
		pr.ExecGit = prevGit
		stubMutex.Unlock()
	})
	pr.ExecGH = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			for _, a := range args {
				if a == "nameWithOwner" {
					if d == dir1 {
						return ghJSON(t, struct {
							NameWithOwner string `json:"nameWithOwner"`
						}{"o/a"}), nil
					}
					return ghJSON(t, struct {
						NameWithOwner string `json:"nameWithOwner"`
					}{"o/b"}), nil
				}
				if a == "defaultBranchRef" {
					return ghJSON(t, struct {
						DefaultBranchRef struct {
							Name string `json:"name"`
						} `json:"defaultBranchRef"`
					}{struct {
						Name string `json:"name"`
					}{"main"}}), nil
				}
			}
		}
		return matchResp(ghMap, args)
	}
	pr.ExecGit = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--abbrev-ref" {
			return []byte("feat\n"), nil
		}
		if len(args) >= 2 && args[0] == "rev-list" && args[1] == "--count" {
			return []byte("3\n"), nil
		}
		return nil, fmt.Errorf("test stub: unhandled git call %v", args)
	}
	opened := stubOpenURL(t)

	var buf bytes.Buffer
	if err := pr.ShowAll("", beadsDir, id, &buf); err != nil {
		t.Fatalf("ShowAll: %v", err)
	}
	if len(*opened) != 2 {
		t.Fatalf("opened = %v, want two URLs", *opened)
	}
	got := map[string]bool{(*opened)[0]: true, (*opened)[1]: true}
	for _, want := range []string{
		"https://github.com/o/a/pull/1",
		"https://github.com/o/b/pull/2",
	} {
		if !got[want] {
			t.Errorf("opened missing %s; got %v", want, *opened)
		}
	}
}

func TestShowAllSkipsDirsWithoutPR(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-show-all-skip")
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	mustLinkDir(t, beadsDir, id, dir1)
	mustLinkDir(t, beadsDir, id, dir2)
	// Only dir1's repo has a PR record.
	mustSavePRs(t, beadsDir, id, []pr.Record{
		{Repo: "o/a", Number: 1, Branch: "b1"},
	})

	stubMutex.Lock()
	prevGH, prevGit := pr.ExecGH, pr.ExecGit
	t.Cleanup(func() {
		pr.ExecGH = prevGH
		pr.ExecGit = prevGit
		stubMutex.Unlock()
	})
	pr.ExecGH = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			for _, a := range args {
				if a == "nameWithOwner" {
					if d == dir1 {
						return ghJSON(t, struct {
							NameWithOwner string `json:"nameWithOwner"`
						}{"o/a"}), nil
					}
					return ghJSON(t, struct {
						NameWithOwner string `json:"nameWithOwner"`
					}{"o/b"}), nil
				}
				if a == "defaultBranchRef" {
					return ghJSON(t, struct {
						DefaultBranchRef struct {
							Name string `json:"name"`
						} `json:"defaultBranchRef"`
					}{struct {
						Name string `json:"name"`
					}{"main"}}), nil
				}
			}
		}
		return nil, fmt.Errorf("test stub: unhandled gh call %v", args)
	}
	pr.ExecGit = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--abbrev-ref" {
			return []byte("feat\n"), nil
		}
		if len(args) >= 2 && args[0] == "rev-list" && args[1] == "--count" {
			return []byte("2\n"), nil
		}
		return nil, fmt.Errorf("test stub: unhandled git call %v", args)
	}
	opened := stubOpenURL(t)

	var buf bytes.Buffer
	if err := pr.ShowAll("", beadsDir, id, &buf); err != nil {
		t.Fatalf("ShowAll: %v", err)
	}
	if len(*opened) != 1 || (*opened)[0] != "https://github.com/o/a/pull/1" {
		t.Errorf("opened = %v, want only o/a#1", *opened)
	}
	if !strings.Contains(buf.String(), "no PR attached, skipping") {
		t.Errorf("stdout = %q, want skip notice", buf.String())
	}
}

func TestShowAllErrorsWhenNoEligibleDirs(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-show-all-none")
	tmp := t.TempDir()
	mustLinkDir(t, beadsDir, id, tmp)

	stubMutex.Lock()
	prevGH, prevGit := pr.ExecGH, pr.ExecGit
	t.Cleanup(func() {
		pr.ExecGH = prevGH
		pr.ExecGit = prevGit
		stubMutex.Unlock()
	})
	pr.ExecGH = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			for _, a := range args {
				if a == "defaultBranchRef" {
					return ghJSON(t, struct {
						DefaultBranchRef struct {
							Name string `json:"name"`
						} `json:"defaultBranchRef"`
					}{struct {
						Name string `json:"name"`
					}{"main"}}), nil
				}
			}
		}
		return nil, fmt.Errorf("test stub: unhandled gh call %v", args)
	}
	pr.ExecGit = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--abbrev-ref" {
			return []byte("feat\n"), nil
		}
		if len(args) >= 2 && args[0] == "rev-list" && args[1] == "--count" {
			return []byte("0\n"), nil
		}
		return nil, fmt.Errorf("test stub: unhandled git call %v", args)
	}
	stubOpenURL(t)

	var buf bytes.Buffer
	err := pr.ShowAll("", beadsDir, id, &buf)
	if err == nil {
		t.Fatal("ShowAll succeeded with no eligible dirs, want error")
	}
	if !strings.Contains(err.Error(), "ahead of their base branch") {
		t.Errorf("error = %v, want 'ahead of their base branch'", err)
	}
}

// --- CreateAll ------------------------------------------------------------

func TestCreateAllSkipsDirsAlreadyAttached(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-create-all-dup")
	tmp := t.TempDir()
	mustLinkDir(t, beadsDir, id, tmp)
	mustSavePRs(t, beadsDir, id, []pr.Record{
		{Repo: "o/a", Number: 1, Branch: "b1"},
	})

	stubMutex.Lock()
	prevGH, prevGit := pr.ExecGH, pr.ExecGit
	t.Cleanup(func() {
		pr.ExecGH = prevGH
		pr.ExecGit = prevGit
		stubMutex.Unlock()
	})
	pr.ExecGH = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			for _, a := range args {
				if a == "nameWithOwner" {
					return ghJSON(t, struct {
						NameWithOwner string `json:"nameWithOwner"`
					}{"o/a"}), nil
				}
				if a == "defaultBranchRef" {
					return ghJSON(t, struct {
						DefaultBranchRef struct {
							Name string `json:"name"`
						} `json:"defaultBranchRef"`
					}{struct {
						Name string `json:"name"`
					}{"main"}}), nil
				}
			}
		}
		return nil, fmt.Errorf("test stub: unhandled gh call %v", args)
	}
	pr.ExecGit = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--abbrev-ref" {
			return []byte("b1\n"), nil
		}
		if len(args) >= 2 && args[0] == "rev-list" && args[1] == "--count" {
			return []byte("4\n"), nil
		}
		return nil, fmt.Errorf("test stub: unhandled git call %v", args)
	}

	var buf bytes.Buffer
	if err := pr.CreateAll("", beadsDir, id, false, &buf); err != nil {
		t.Fatalf("CreateAll: %v", err)
	}
	if !strings.Contains(buf.String(), "PR already attached for o/a") {
		t.Errorf("stdout = %q, want skip notice for o/a", buf.String())
	}
}

func TestCreateAllErrorsWhenNoEligibleDirs(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-create-all-none")
	tmp := t.TempDir()
	mustLinkDir(t, beadsDir, id, tmp)

	stubMutex.Lock()
	prevGH, prevGit := pr.ExecGH, pr.ExecGit
	t.Cleanup(func() {
		pr.ExecGH = prevGH
		pr.ExecGit = prevGit
		stubMutex.Unlock()
	})
	pr.ExecGH = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			for _, a := range args {
				if a == "defaultBranchRef" {
					return ghJSON(t, struct {
						DefaultBranchRef struct {
							Name string `json:"name"`
						} `json:"defaultBranchRef"`
					}{struct {
						Name string `json:"name"`
					}{"main"}}), nil
				}
			}
		}
		return nil, fmt.Errorf("test stub: unhandled gh call %v", args)
	}
	pr.ExecGit = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--abbrev-ref" {
			return []byte("feat\n"), nil
		}
		if len(args) >= 2 && args[0] == "rev-list" && args[1] == "--count" {
			return []byte("0\n"), nil
		}
		return nil, fmt.Errorf("test stub: unhandled git call %v", args)
	}

	var buf bytes.Buffer
	err := pr.CreateAll("", beadsDir, id, false, &buf)
	if err == nil {
		t.Fatal("CreateAll succeeded with no eligible dirs, want error")
	}
	if !strings.Contains(err.Error(), "ahead of their base branch") {
		t.Errorf("error = %v, want 'ahead of their base branch'", err)
	}
}

// --- LinkAuto (no url) ----------------------------------------------------

func TestLinkAutoLinksUniqueOpenPR(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-link-auto-one")
	mustLinkDir(t, beadsDir, id, t.TempDir())

	ghMap := map[string]ghResp{
		"repo view": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
		"pr list": ghResp{Out: ghJSON(t, []struct {
			Number int `json:"number"`
		}{{42}})},
	}
	gitMap := map[string]ghResp{
		"rev-parse": ghResp{Out: []byte("feat/foo\n")},
	}
	installStubs(t, ghMap, gitMap)

	var buf bytes.Buffer
	if err := pr.LinkAuto("", beadsDir, id, "", &buf); err != nil {
		t.Fatalf("LinkAuto: %v", err)
	}
	recs, err := pr.Load("", beadsDir, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(recs) != 1 || recs[0].Repo != "o/r" || recs[0].Number != 42 || recs[0].Branch != "feat/foo" {
		t.Errorf("recs = %+v, want one PR o/r#42 branch feat/foo", recs)
	}
	if !strings.Contains(buf.String(), "https://github.com/o/r/pull/42") {
		t.Errorf("stdout = %q, want linked PR url", buf.String())
	}
}

func TestLinkAutoErrorsWhenNoOpenPR(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-link-auto-zero")
	mustLinkDir(t, beadsDir, id, t.TempDir())

	ghMap := map[string]ghResp{
		"repo view": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
		"pr list": ghResp{Out: ghJSON(t, []struct {
			Number int `json:"number"`
		}{})},
	}
	gitMap := map[string]ghResp{
		"rev-parse": ghResp{Out: []byte("feat/foo\n")},
	}
	installStubs(t, ghMap, gitMap)

	var buf bytes.Buffer
	err := pr.LinkAuto("", beadsDir, id, "", &buf)
	if err == nil {
		t.Fatal("LinkAuto succeeded with no open PR, want error")
	}
	if !strings.Contains(err.Error(), "no open PR") {
		t.Errorf("error = %v, want 'no open PR'", err)
	}
}

func TestLinkAutoErrorsWhenMultipleOpenPRs(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-link-auto-many")
	mustLinkDir(t, beadsDir, id, t.TempDir())

	ghMap := map[string]ghResp{
		"repo view": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
		"pr list": ghResp{Out: ghJSON(t, []struct {
			Number int `json:"number"`
		}{{1}, {2}})},
	}
	gitMap := map[string]ghResp{
		"rev-parse": ghResp{Out: []byte("feat/foo\n")},
	}
	installStubs(t, ghMap, gitMap)

	var buf bytes.Buffer
	err := pr.LinkAuto("", beadsDir, id, "", &buf)
	if err == nil {
		t.Fatal("LinkAuto succeeded with two open PRs, want error")
	}
	if !strings.Contains(err.Error(), "multiple open PRs") {
		t.Errorf("error = %v, want 'multiple open PRs'", err)
	}
}

func TestLinkAutoRefusesDuplicateRepo(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-link-auto-dup")
	mustLinkDir(t, beadsDir, id, t.TempDir())
	mustSavePRs(t, beadsDir, id, []pr.Record{{Repo: "o/r", Number: 1, Branch: "x"}})

	ghMap := map[string]ghResp{
		"repo view": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
		"pr list": ghResp{Out: ghJSON(t, []struct {
			Number int `json:"number"`
		}{{2}})},
	}
	gitMap := map[string]ghResp{
		"rev-parse": ghResp{Out: []byte("feat/foo\n")},
	}
	installStubs(t, ghMap, gitMap)

	var buf bytes.Buffer
	err := pr.LinkAuto("", beadsDir, id, "", &buf)
	if err == nil {
		t.Fatal("LinkAuto succeeded on already-attached repo, want error")
	}
	if !strings.Contains(err.Error(), "already has a PR") {
		t.Errorf("error = %v, want 'already has a PR'", err)
	}
}

// --- LinkAll (--all, no url) ----------------------------------------------

// flagVal returns the value following flag in args ("" when absent).
func flagVal(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func TestLinkAllLinksUniqueAndSkipsAmbiguous(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-link-all")
	dirLink := t.TempDir()  // unique open PR -> linked
	dirZero := t.TempDir()  // no open PR -> skip
	dirMany := t.TempDir()  // two open PRs -> skip
	dirAttd := t.TempDir()  // already attached -> skip
	mustLinkDir(t, beadsDir, id, dirLink)
	mustLinkDir(t, beadsDir, id, dirZero)
	mustLinkDir(t, beadsDir, id, dirMany)
	mustLinkDir(t, beadsDir, id, dirAttd)
	mustSavePRs(t, beadsDir, id, []pr.Record{{Repo: "o/attd", Number: 5, Branch: "b-attd"}})

	repoByDir := map[string]string{
		dirLink: "o/link", dirZero: "o/zero", dirMany: "o/many", dirAttd: "o/attd",
	}
	prsByRepo := map[string][]int{
		"o/link": {7}, "o/zero": {}, "o/many": {1, 2}, "o/attd": {9},
	}

	stubMutex.Lock()
	prevGH, prevGit := pr.ExecGH, pr.ExecGit
	t.Cleanup(func() {
		pr.ExecGH = prevGH
		pr.ExecGit = prevGit
		stubMutex.Unlock()
	})
	pr.ExecGH = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			for _, a := range args {
				if a == "nameWithOwner" {
					return ghJSON(t, struct {
						NameWithOwner string `json:"nameWithOwner"`
					}{repoByDir[d]}), nil
				}
				if a == "defaultBranchRef" {
					return ghJSON(t, struct {
						DefaultBranchRef struct {
							Name string `json:"name"`
						} `json:"defaultBranchRef"`
					}{struct {
						Name string `json:"name"`
					}{"main"}}), nil
				}
			}
		}
		if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
			repo := flagVal(args, "--repo")
			nums := prsByRepo[repo]
			rows := make([]struct {
				Number int `json:"number"`
			}, len(nums))
			for i, n := range nums {
				rows[i].Number = n
			}
			return ghJSON(t, rows), nil
		}
		return nil, fmt.Errorf("test stub: unhandled gh call %v", args)
	}
	pr.ExecGit = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--abbrev-ref" {
			return []byte("b-" + strings.TrimPrefix(repoByDir[d], "o/") + "\n"), nil
		}
		if len(args) >= 2 && args[0] == "rev-list" && args[1] == "--count" {
			return []byte("3\n"), nil
		}
		return nil, fmt.Errorf("test stub: unhandled git call %v", args)
	}

	var buf bytes.Buffer
	if err := pr.LinkAll("", beadsDir, id, &buf); err != nil {
		t.Fatalf("LinkAll: %v", err)
	}
	out := buf.String()
	recs, err := pr.Load("", beadsDir, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// o/attd was pre-existing; o/link gets newly linked. o/zero and o/many do not.
	byRepo := map[string]pr.Record{}
	for _, r := range recs {
		byRepo[r.Repo] = r
	}
	if r, ok := byRepo["o/link"]; !ok || r.Number != 7 || r.Branch != "b-link" {
		t.Errorf("o/link record = %+v (ok=%v), want #7 branch b-link", r, ok)
	}
	if _, ok := byRepo["o/zero"]; ok {
		t.Errorf("o/zero should not be linked: %+v", recs)
	}
	if _, ok := byRepo["o/many"]; ok {
		t.Errorf("o/many should not be linked: %+v", recs)
	}
	if !strings.Contains(out, "no open PR for o/zero") {
		t.Errorf("stdout missing o/zero skip notice:\n%s", out)
	}
	if !strings.Contains(out, "multiple open PRs for o/many") {
		t.Errorf("stdout missing o/many skip notice:\n%s", out)
	}
	if !strings.Contains(out, "PR already attached for o/attd") {
		t.Errorf("stdout missing o/attd skip notice:\n%s", out)
	}
	if !strings.Contains(out, "https://github.com/o/link/pull/7") {
		t.Errorf("stdout missing o/link link line:\n%s", out)
	}
}

// --- Create: link-vs-create-vs-skip ---------------------------------------

func TestCreateLinksExistingUniquePR(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-create-link")
	mustLinkDir(t, beadsDir, id, t.TempDir())

	ghMap := map[string]ghResp{
		"repo view": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
		"pr list": ghResp{Out: ghJSON(t, []struct {
			Number int `json:"number"`
		}{{55}})},
		// gh pr create must NOT be reached; make it explode if it is.
		"pr create": ghResp{Err: makeExitErr("unexpected gh pr create")},
	}
	gitMap := map[string]ghResp{
		"rev-parse": ghResp{Out: []byte("feat\n")},
	}
	installStubs(t, ghMap, gitMap)

	var buf bytes.Buffer
	if err := pr.Create("", beadsDir, id, "", false, &buf); err != nil {
		t.Fatalf("Create: %v", err)
	}
	recs, err := pr.Load("", beadsDir, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(recs) != 1 || recs[0].Repo != "o/r" || recs[0].Number != 55 || recs[0].Branch != "feat" {
		t.Errorf("recs = %+v, want linked o/r#55 branch feat", recs)
	}
	if !strings.Contains(buf.String(), "linked existing PR") {
		t.Errorf("stdout = %q, want 'linked existing PR'", buf.String())
	}
}

func TestCreateErrorsWhenMultipleOpenPRs(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-create-many")
	mustLinkDir(t, beadsDir, id, t.TempDir())

	ghMap := map[string]ghResp{
		"repo view": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
		"pr list": ghResp{Out: ghJSON(t, []struct {
			Number int `json:"number"`
		}{{1}, {2}})},
	}
	gitMap := map[string]ghResp{
		"rev-parse": ghResp{Out: []byte("feat\n")},
	}
	installStubs(t, ghMap, gitMap)

	var buf bytes.Buffer
	err := pr.Create("", beadsDir, id, "", false, &buf)
	if err == nil {
		t.Fatal("Create succeeded with two open PRs, want error")
	}
	if !strings.Contains(err.Error(), "multiple open PRs") {
		t.Errorf("error = %v, want 'multiple open PRs'", err)
	}
}

func TestCreateAllLinkCreateSkip(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-create-all-mix")
	dirLink := t.TempDir() // one open PR -> link
	dirNew := t.TempDir()  // zero open PRs -> create
	dirMany := t.TempDir() // two open PRs -> skip
	mustLinkDir(t, beadsDir, id, dirLink)
	mustLinkDir(t, beadsDir, id, dirNew)
	mustLinkDir(t, beadsDir, id, dirMany)

	repoByDir := map[string]string{
		dirLink: "o/link", dirNew: "o/new", dirMany: "o/many",
	}
	prsByRepo := map[string][]int{
		"o/link": {7}, "o/new": {}, "o/many": {1, 2},
	}

	stubMutex.Lock()
	prevGH, prevGit := pr.ExecGH, pr.ExecGit
	t.Cleanup(func() {
		pr.ExecGH = prevGH
		pr.ExecGit = prevGit
		stubMutex.Unlock()
	})
	pr.ExecGH = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			for _, a := range args {
				if a == "nameWithOwner" {
					return ghJSON(t, struct {
						NameWithOwner string `json:"nameWithOwner"`
					}{repoByDir[d]}), nil
				}
				if a == "defaultBranchRef" {
					return ghJSON(t, struct {
						DefaultBranchRef struct {
							Name string `json:"name"`
						} `json:"defaultBranchRef"`
					}{struct {
						Name string `json:"name"`
					}{"main"}}), nil
				}
			}
		}
		if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
			repo := flagVal(args, "--repo")
			nums := prsByRepo[repo]
			rows := make([]struct {
				Number int `json:"number"`
			}, len(nums))
			for i, n := range nums {
				rows[i].Number = n
			}
			return ghJSON(t, rows), nil
		}
		if len(args) >= 2 && args[0] == "pr" && args[1] == "create" {
			// Only o/new should reach create.
			if repoByDir[d] != "o/new" {
				return nil, fmt.Errorf("unexpected gh pr create for %s", repoByDir[d])
			}
			return []byte("https://github.com/o/new/pull/99\n"), nil
		}
		return nil, fmt.Errorf("test stub: unhandled gh call %v", args)
	}
	pr.ExecGit = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--abbrev-ref" {
			return []byte("b-" + strings.TrimPrefix(repoByDir[d], "o/") + "\n"), nil
		}
		if len(args) >= 1 && args[0] == "fetch" {
			return []byte(""), nil
		}
		if len(args) >= 2 && args[0] == "rev-list" && args[1] == "--count" {
			return []byte("3\n"), nil
		}
		if len(args) >= 1 && args[0] == "push" {
			return []byte(""), nil
		}
		return nil, fmt.Errorf("test stub: unhandled git call %v", args)
	}

	var buf bytes.Buffer
	if err := pr.CreateAll("", beadsDir, id, false, &buf); err != nil {
		t.Fatalf("CreateAll: %v", err)
	}
	out := buf.String()
	recs, err := pr.Load("", beadsDir, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	byRepo := map[string]pr.Record{}
	for _, r := range recs {
		byRepo[r.Repo] = r
	}
	if r, ok := byRepo["o/link"]; !ok || r.Number != 7 {
		t.Errorf("o/link record = %+v (ok=%v), want #7 (linked)", r, ok)
	}
	if r, ok := byRepo["o/new"]; !ok || r.Number != 99 {
		t.Errorf("o/new record = %+v (ok=%v), want #99 (created)", r, ok)
	}
	if _, ok := byRepo["o/many"]; ok {
		t.Errorf("o/many should be skipped, not attached: %+v", recs)
	}
	if !strings.Contains(out, "linked existing PR https://github.com/o/link/pull/7") {
		t.Errorf("stdout missing o/link link line:\n%s", out)
	}
	if !strings.Contains(out, "https://github.com/o/new/pull/99") {
		t.Errorf("stdout missing o/new create line:\n%s", out)
	}
	if !strings.Contains(out, "multiple open PRs for o/many") {
		t.Errorf("stdout missing o/many skip notice:\n%s", out)
	}
}

func TestCreateErrorsWhenNoCommits(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-create-no-commits")
	mustLinkDir(t, beadsDir, id, t.TempDir())

	ghMap := map[string]ghResp{
		"repo view --json nameWithOwner": ghResp{Out: ghJSON(t, struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{"o/r"})},
		"repo view --json defaultBranchRef": ghResp{Out: ghJSON(t, struct {
			DefaultBranchRef struct {
				Name string `json:"name"`
			} `json:"defaultBranchRef"`
		}{struct {
			Name string `json:"name"`
		}{"main"}})},
		// No open PR on the branch, so Create takes the create path…
		"pr list": ghResp{Out: ghJSON(t, []struct {
			Number int `json:"number"`
		}{})},
		// …but gh pr create must never be reached.
		"pr create": ghResp{Err: makeExitErr("unexpected gh pr create")},
	}
	gitMap := map[string]ghResp{
		"rev-parse": ghResp{Out: []byte("feat\n")},
		"fetch":     ghResp{Out: []byte("")},
		// Zero commits between the (default) base and HEAD.
		"rev-list": ghResp{Out: []byte("0\n")},
	}
	installStubs(t, ghMap, gitMap)

	var buf bytes.Buffer
	err := pr.Create("", beadsDir, id, "", false, &buf)
	if err == nil {
		t.Fatal("Create succeeded with no commits, want error")
	}
	if !strings.Contains(err.Error(), "no commits") {
		t.Errorf("error = %v, want 'no commits' substring", err)
	}
	recs, err := pr.Load("", beadsDir, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("recs = %+v, want none attached", recs)
	}
}

// TestCreateAllSkipsDirsWithNoRemoteCommits reproduces the reported bug: the
// dir looks ahead of its base locally (stale default branch) so eligibleDirs
// keeps it, but the remote base already contains every commit. CreateAll must
// skip it cleanly instead of letting gh pr create fail the whole run.
func TestCreateAllSkipsDirsWithNoRemoteCommits(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	id := testutil.CreateBead(t, beadsDir, "pr-create-all-no-commits")
	mustLinkDir(t, beadsDir, id, t.TempDir())

	stubMutex.Lock()
	prevGH, prevGit := pr.ExecGH, pr.ExecGit
	t.Cleanup(func() {
		pr.ExecGH = prevGH
		pr.ExecGit = prevGit
		stubMutex.Unlock()
	})
	pr.ExecGH = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "repo" && args[1] == "view" {
			for _, a := range args {
				if a == "nameWithOwner" {
					return ghJSON(t, struct {
						NameWithOwner string `json:"nameWithOwner"`
					}{"o/r"}), nil
				}
				if a == "defaultBranchRef" {
					return ghJSON(t, struct {
						DefaultBranchRef struct {
							Name string `json:"name"`
						} `json:"defaultBranchRef"`
					}{struct {
						Name string `json:"name"`
					}{"main"}}), nil
				}
			}
		}
		if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
			return ghJSON(t, []struct {
				Number int `json:"number"`
			}{}), nil
		}
		// gh pr create must never be reached.
		return nil, fmt.Errorf("test stub: unexpected gh call %v", args)
	}
	pr.ExecGit = func(d string, args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--abbrev-ref" {
			return []byte("feat\n"), nil
		}
		if len(args) >= 1 && args[0] == "fetch" {
			return []byte(""), nil
		}
		if len(args) >= 3 && args[0] == "rev-list" && args[1] == "--count" {
			// Local base ref looks ahead (eligibility passes); the fetched
			// remote base (FETCH_HEAD) has nothing new — the bug's condition.
			if strings.Contains(args[2], "FETCH_HEAD") {
				return []byte("0\n"), nil
			}
			return []byte("3\n"), nil
		}
		return nil, fmt.Errorf("test stub: unhandled git call %v", args)
	}

	var buf bytes.Buffer
	if err := pr.CreateAll("", beadsDir, id, false, &buf); err != nil {
		t.Fatalf("CreateAll: %v", err)
	}
	if !strings.Contains(buf.String(), "no commits to create a PR for, skipping") {
		t.Errorf("stdout = %q, want no-commits skip notice", buf.String())
	}
	recs, err := pr.Load("", beadsDir, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("recs = %+v, want none attached", recs)
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

func beadLabels(t *testing.T, beadsDir, id string) []string {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "show", id).Output()
	if err != nil {
		t.Fatalf("bd show: %v", err)
	}
	var rows []struct {
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) == 0 {
		return nil
	}
	return rows[0].Labels
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

