package jiracli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"framework2/internal/jiracli"
	"framework2/internal/testutil"
)

func TestMapType(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"Bug":         "bug",
		"Regression":  "bug",
		"Story":       "feature",
		"Feature":     "feature",
		"Enhancement": "feature",
		"New Feature": "feature",
		"Epic":        "epic",
		"Task":        "task",
		"Sub-task":    "task",
		"Whatever":    "task",
		"":            "task",
	}
	for jiraType, want := range cases {
		if got := jiracli.MapType(jiraType); got != want {
			t.Errorf("MapType(%q) = %q, want %q", jiraType, got, want)
		}
	}
}

func TestBuildDescription(t *testing.T) {
	t.Parallel()
	mk := func(desc, steps string) *jiracli.Issue {
		i := &jiracli.Issue{}
		i.Fields.Description = desc
		i.Fields.StepsToReproduce = steps
		return i
	}
	cases := []struct {
		name string
		in   *jiracli.Issue
		want string
	}{
		{"desc only", mk("Body text.", ""), "Body text."},
		{"steps only", mk("", "1. do x"), "## Steps to Reproduce\n\n1. do x"},
		{"both", mk("Body.", "1. x"), "Body.\n\n## Steps to Reproduce\n\n1. x"},
		{"neither", mk("", ""), ""},
		{"trims trailing newline before steps", mk("Body.\n\n", "1. x"), "Body.\n\n## Steps to Reproduce\n\n1. x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jiracli.BuildDescription(tc.in); got != tc.want {
				t.Errorf("BuildDescription = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRunRejectsBadArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no args", nil, "usage:"},
		{"too many", []string{"FOO-1", "BAR-2"}, "usage:"},
		{"bad format", []string{"foo-1"}, "not a Jira key"},
		{"bad format 2", []string{"FOO"}, "not a Jira key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := jiracli.RunWithOptions(jiracli.Options{Email: "x", Token: "y"}, tc.args, &stdout, &stderr)
			if code == 0 {
				t.Fatalf("expected nonzero exit; stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr=%q does not contain %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestRunRejectsMissingCredentials(t *testing.T) {
	// no t.Parallel — uses t.Setenv to clear inherited credentials.
	t.Setenv("KNIME_ATLASSIAN_EMAIL", "")
	t.Setenv("KNIME_ATLASSIAN_API_TOKEN", "")
	var stdout, stderr bytes.Buffer
	code := jiracli.RunWithOptions(jiracli.Options{}, []string{"UIEXT-1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected nonzero exit")
	}
	if !strings.Contains(stderr.String(), "KNIME_ATLASSIAN_EMAIL") {
		t.Errorf("stderr=%q missing credential hint", stderr.String())
	}
}

func TestRunHelp(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	code := jiracli.RunWithOptions(jiracli.Options{}, []string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected zero exit; stderr=%q", stderr.String())
	}
	if !strings.Contains(stdout.String(), "usage:") {
		t.Errorf("stdout missing usage; got %q", stdout.String())
	}
}

// TestRunCreatesBead exercises the full flow: fake Jira server → bd create →
// inspect the resulting bead.
func TestRunCreatesBead(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "me@example.com" || pass != "tok" {
			http.Error(w, "unauth", 401)
			return
		}
		switch r.URL.Path {
		case "/rest/api/2/issue/UIEXT-9999":
			issue := map[string]any{
				"key": "UIEXT-9999",
				"fields": map[string]any{
					"summary":           "Make widgets clickable",
					"description":       "When the user clicks, things should happen.",
					"issuetype":         map[string]any{"name": "Enhancement"},
					"customfield_11444": "1. click widget\n2. observe nothing\n",
				},
			}
			_ = json.NewEncoder(w).Encode(issue)
		case "/rest/api/2/search":
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": []any{}, "total": 0})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.Error(w, "not found", 404)
		}
	}))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	code := jiracli.RunWithOptions(jiracli.Options{
		BaseURL:  server.URL,
		BeadsDir: beadsDir,
		Email:    "me@example.com",
		Token:    "tok",
	}, []string{"UIEXT-9999"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	id := strings.TrimSpace(stdout.String())
	if id == "" {
		t.Fatalf("no bead id printed; stderr=%q", stderr.String())
	}
	t.Cleanup(func() { _ = testutil.BdCmd(beadsDir, "close", id).Run() })

	out, err := testutil.BdCmd(beadsDir, "--json", "show", id).Output()
	if err != nil {
		t.Fatalf("bd show: %v", err)
	}
	var rows []struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		IssueType   string `json:"issue_type"`
		Priority    int    `json:"priority"`
		ExternalRef string `json:"external_ref"`
		Parent      string `json:"parent"`
		Metadata    struct {
			Slug string `json:"slug"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("bd show returned no rows")
	}
	got := rows[0]
	if got.Title != "Make widgets clickable" {
		t.Errorf("title = %q", got.Title)
	}
	if !strings.Contains(got.Description, "When the user clicks") {
		t.Errorf("description missing Jira body; got %q", got.Description)
	}
	if !strings.Contains(got.Description, "## Steps to Reproduce") {
		t.Errorf("description missing steps section; got %q", got.Description)
	}
	if got.IssueType != "feature" {
		t.Errorf("issue_type = %q, want feature (Enhancement → feature)", got.IssueType)
	}
	if got.Priority != 2 {
		t.Errorf("priority = %d, want 2", got.Priority)
	}
	wantRef := server.URL + "/browse/UIEXT-9999"
	if got.ExternalRef != wantRef {
		t.Errorf("external_ref = %q, want %q", got.ExternalRef, wantRef)
	}
	if got.Parent != "" {
		t.Errorf("expected top-level bead, got parent %q", got.Parent)
	}
	if got.Metadata.Slug == "" {
		t.Errorf("slug missing")
	}
}

// fakeIssue is the subset of a Jira issue our test fixtures need.
type fakeIssue struct {
	subtasks         []string // keys reachable via the subtasks field
	children         []string // keys reachable via the reverse `parent = KEY` JQL search
	epicLinkChildren []string // keys reachable via `"Epic Link" = KEY` JQL (older Jira)
	parent           string   // populated on returned issue, for completeness
	issueType        string   // Jira issuetype name; defaults to "Task"
}

// fakeJira spins up an httptest.Server that serves /rest/api/2/issue/<KEY>
// and /rest/api/2/search?jql=parent=<KEY> against an in-memory fixture, and
// counts how often each key is fetched so tests can assert the cache works.
type fakeJira struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	fetches map[string]int
}

func newFakeJira(t *testing.T, fixture map[string]fakeIssue) *fakeJira {
	t.Helper()
	fj := &fakeJira{t: t, fetches: map[string]int{}}
	fj.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/rest/api/2/issue/"):
			key := strings.TrimPrefix(r.URL.Path, "/rest/api/2/issue/")
			f, ok := fixture[key]
			if !ok {
				http.Error(w, "not found", 404)
				return
			}
			fj.mu.Lock()
			fj.fetches[key]++
			fj.mu.Unlock()
			subtasks := make([]map[string]string, 0, len(f.subtasks))
			for _, s := range f.subtasks {
				subtasks = append(subtasks, map[string]string{"key": s})
			}
			issueType := f.issueType
			if issueType == "" {
				issueType = "Task"
			}
			fields := map[string]any{
				"summary":     key + " summary",
				"description": key + " body",
				"issuetype":   map[string]any{"name": issueType},
				"subtasks":    subtasks,
			}
			if f.parent != "" {
				fields["parent"] = map[string]string{"key": f.parent}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"key": key, "fields": fields})
		case r.URL.Path == "/rest/api/2/search":
			jql := r.URL.Query().Get("jql")
			// JQL we issue is either `parent = <KEY>` or `"Epic Link" = <KEY>`.
			var (
				key      string
				epicLink bool
			)
			if _, after, found := strings.Cut(jql, `"Epic Link" = `); found {
				key = strings.TrimSpace(after)
				epicLink = true
			} else if _, after, found := strings.Cut(jql, "parent = "); found {
				key = strings.TrimSpace(after)
			}
			issues := []map[string]string{}
			if f, ok := fixture[key]; ok {
				src := f.children
				if epicLink {
					src = f.epicLinkChildren
				}
				for _, c := range src {
					issues = append(issues, map[string]string{"key": c})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issues":  issues,
				"total":   len(issues),
				"startAt": 0,
			})
		default:
			http.Error(w, "not found", 404)
		}
	}))
	t.Cleanup(fj.server.Close)
	return fj
}

// importedTree returns parent → []child as bd reports it, keyed by Jira key
// (read off the bead's external_ref). Each child slice is sorted for stable
// comparison; tree shape is what we care about, not BFS insertion order.
func importedTree(t *testing.T, beadsDir string) map[string][]string {
	t.Helper()
	out, err := testutil.BdCmd(beadsDir, "--json", "list", "--all").Output()
	if err != nil {
		t.Fatalf("bd list: %v", err)
	}
	var rows []struct {
		ID          string `json:"id"`
		Parent      string `json:"parent"`
		ExternalRef string `json:"external_ref"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatalf("parse bd list: %v", err)
	}
	idToKey := map[string]string{}
	for _, r := range rows {
		if i := strings.LastIndex(r.ExternalRef, "/"); i >= 0 {
			idToKey[r.ID] = r.ExternalRef[i+1:]
		}
	}
	tree := map[string][]string{}
	for _, r := range rows {
		key := idToKey[r.ID]
		if _, ok := tree[key]; !ok {
			tree[key] = nil
		}
		if r.Parent == "" {
			continue
		}
		parentKey := idToKey[r.Parent]
		tree[parentKey] = append(tree[parentKey], key)
	}
	for k := range tree {
		sort.Strings(tree[k])
	}
	return tree
}

func runJira(t *testing.T, server *httptest.Server, beadsDir, key string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := jiracli.RunWithOptions(jiracli.Options{
		BaseURL:  server.URL,
		BeadsDir: beadsDir,
		Email:    "me@example.com",
		Token:    "tok",
	}, []string{key}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("jira %s: exit=%d stderr=%q", key, code, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

func TestRunRecursiveShallow(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	fj := newFakeJira(t, map[string]fakeIssue{
		"FOO-1": {},
	})
	rootID := runJira(t, fj.server, beadsDir, "FOO-1")
	if rootID == "" {
		t.Fatal("no root id printed")
	}
	tree := importedTree(t, beadsDir)
	if len(tree) != 1 || len(tree["FOO-1"]) != 0 {
		t.Errorf("expected single bead, got tree=%v", tree)
	}
}

func TestRunRecursiveSubtasks(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	fj := newFakeJira(t, map[string]fakeIssue{
		"FOO-1": {subtasks: []string{"FOO-2", "FOO-3"}},
		"FOO-2": {},
		"FOO-3": {},
	})
	runJira(t, fj.server, beadsDir, "FOO-1")
	tree := importedTree(t, beadsDir)
	want := map[string][]string{
		"FOO-1": {"FOO-2", "FOO-3"},
		"FOO-2": nil,
		"FOO-3": nil,
	}
	if !equalTree(tree, want) {
		t.Errorf("tree mismatch:\n got=%v\nwant=%v", tree, want)
	}
}

func TestRunRecursiveThreeDeep(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	fj := newFakeJira(t, map[string]fakeIssue{
		"FOO-1": {subtasks: []string{"FOO-2"}},
		"FOO-2": {subtasks: []string{"FOO-3"}},
		"FOO-3": {subtasks: []string{"FOO-4"}},
		"FOO-4": {},
	})
	runJira(t, fj.server, beadsDir, "FOO-1")
	tree := importedTree(t, beadsDir)
	want := map[string][]string{
		"FOO-1": {"FOO-2"},
		"FOO-2": {"FOO-3"},
		"FOO-3": {"FOO-4"},
		"FOO-4": nil,
	}
	if !equalTree(tree, want) {
		t.Errorf("tree mismatch:\n got=%v\nwant=%v", tree, want)
	}
}

func TestRunRecursiveMixedDiscovery(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	// FOO-2 is reachable via the subtasks field; FOO-3 is reachable only
	// via the reverse parent search. Both must end up under FOO-1.
	fj := newFakeJira(t, map[string]fakeIssue{
		"FOO-1": {subtasks: []string{"FOO-2"}, children: []string{"FOO-3"}},
		"FOO-2": {},
		"FOO-3": {parent: "FOO-1"},
	})
	runJira(t, fj.server, beadsDir, "FOO-1")
	tree := importedTree(t, beadsDir)
	want := map[string][]string{
		"FOO-1": {"FOO-2", "FOO-3"},
		"FOO-2": nil,
		"FOO-3": nil,
	}
	if !equalTree(tree, want) {
		t.Errorf("tree mismatch:\n got=%v\nwant=%v", tree, want)
	}
}

func TestRunRecursiveCacheDedups(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	// FOO-2 is reachable both via FOO-1's subtasks AND via the reverse
	// parent search for FOO-1. The cache must collapse those into a
	// single fetch and a single bead.
	fj := newFakeJira(t, map[string]fakeIssue{
		"FOO-1": {subtasks: []string{"FOO-2"}, children: []string{"FOO-2"}},
		"FOO-2": {parent: "FOO-1"},
	})
	runJira(t, fj.server, beadsDir, "FOO-1")

	fj.mu.Lock()
	defer fj.mu.Unlock()
	if fj.fetches["FOO-2"] != 1 {
		t.Errorf("FOO-2 fetched %d times, want 1", fj.fetches["FOO-2"])
	}
	if fj.fetches["FOO-1"] != 1 {
		t.Errorf("FOO-1 fetched %d times, want 1", fj.fetches["FOO-1"])
	}

	tree := importedTree(t, beadsDir)
	want := map[string][]string{
		"FOO-1": {"FOO-2"},
		"FOO-2": nil,
	}
	if !equalTree(tree, want) {
		t.Errorf("tree mismatch:\n got=%v\nwant=%v", tree, want)
	}
}

func equalTree(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if av[i] != bv[i] {
				return false
			}
		}
	}
	return true
}

// TestRunEpicWithStories covers the Epic membership fallback: the epic has
// no `subtasks` and no `parent = KEY` matches, but its stories are reachable
// via the legacy `"Epic Link" = KEY` JQL.
func TestRunEpicWithStories(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	fj := newFakeJira(t, map[string]fakeIssue{
		"EPIC-1":  {issueType: "Epic", epicLinkChildren: []string{"STORY-1", "STORY-2"}},
		"STORY-1": {},
		"STORY-2": {},
	})
	runJira(t, fj.server, beadsDir, "EPIC-1")
	tree := importedTree(t, beadsDir)
	want := map[string][]string{
		"EPIC-1":  {"STORY-1", "STORY-2"},
		"STORY-1": nil,
		"STORY-2": nil,
	}
	if !equalTree(tree, want) {
		t.Errorf("tree mismatch:\n got=%v\nwant=%v", tree, want)
	}
}

// TestRunEpicWithStoriesAndSubtasks verifies that the slice-1 recursion
// engine picks up from the Epic Link discovery: stories returned via the
// Epic Link JQL further recurse into their own subtasks.
func TestRunEpicWithStoriesAndSubtasks(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	fj := newFakeJira(t, map[string]fakeIssue{
		"EPIC-1":  {issueType: "Epic", epicLinkChildren: []string{"STORY-1"}},
		"STORY-1": {subtasks: []string{"SUB-1", "SUB-2"}},
		"SUB-1":   {},
		"SUB-2":   {},
	})
	runJira(t, fj.server, beadsDir, "EPIC-1")
	tree := importedTree(t, beadsDir)
	want := map[string][]string{
		"EPIC-1":  {"STORY-1"},
		"STORY-1": {"SUB-1", "SUB-2"},
		"SUB-1":   nil,
		"SUB-2":   nil,
	}
	if !equalTree(tree, want) {
		t.Errorf("tree mismatch:\n got=%v\nwant=%v", tree, want)
	}
}

// TestRunEpicWithNoStories makes sure an empty Epic produces no error and no
// children — both the `parent` and `"Epic Link"` searches return nothing.
func TestRunEpicWithNoStories(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	fj := newFakeJira(t, map[string]fakeIssue{
		"EPIC-1": {issueType: "Epic"},
	})
	runJira(t, fj.server, beadsDir, "EPIC-1")
	tree := importedTree(t, beadsDir)
	want := map[string][]string{"EPIC-1": nil}
	if !equalTree(tree, want) {
		t.Errorf("tree mismatch:\n got=%v\nwant=%v", tree, want)
	}
}

func TestRunHandles404(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", 404)
	}))
	t.Cleanup(server.Close)

	var stdout, stderr bytes.Buffer
	code := jiracli.RunWithOptions(jiracli.Options{
		BaseURL: server.URL,
		Email:   "x",
		Token:   "y",
	}, []string{"UIEXT-1"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected nonzero exit")
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Errorf("stderr=%q missing not-found message", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stdout should be empty on error, got %q", stdout.String())
	}
}
