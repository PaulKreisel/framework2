package jiracli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"framework2/internal/githubcli"
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
		case "/rest/api/3/search/jql":
			if r.Method != http.MethodPost {
				t.Errorf("expected POST for /rest/api/3/search/jql, got %s", r.Method)
				http.Error(w, "method", 405)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"issues": []any{}, "isLast": true})
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
	blockedBy        []string // keys appearing as inward "is blocked by" links
	relatesTo        []string // keys appearing as inward "relates to" links (ignored by importer)
}

// fakeJira spins up an httptest.Server that serves /rest/api/2/issue/<KEY>
// and POST /rest/api/3/search/jql against an in-memory fixture, and counts
// how often each key is fetched so tests can assert the cache works.
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
			links := make([]map[string]any, 0, len(f.blockedBy)+len(f.relatesTo))
			for _, b := range f.blockedBy {
				links = append(links, map[string]any{
					"type": map[string]string{
						"name":    "Blocks",
						"inward":  "is blocked by",
						"outward": "blocks",
					},
					"inwardIssue": map[string]string{"key": b},
				})
			}
			for _, r := range f.relatesTo {
				links = append(links, map[string]any{
					"type": map[string]string{
						"name":    "Relates",
						"inward":  "relates to",
						"outward": "relates to",
					},
					"inwardIssue": map[string]string{"key": r},
				})
			}
			fields := map[string]any{
				"summary":     key + " summary",
				"description": key + " body",
				"issuetype":   map[string]any{"name": issueType},
				"subtasks":    subtasks,
				"issuelinks":  links,
			}
			if f.parent != "" {
				fields["parent"] = map[string]string{"key": f.parent}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"key": key, "fields": fields})
		case r.URL.Path == "/rest/api/3/search/jql":
			if r.Method != http.MethodPost {
				t.Errorf("expected POST for /rest/api/3/search/jql, got %s", r.Method)
				http.Error(w, "method", 405)
				return
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("expected Content-Type application/json, got %q", ct)
			}
			var body struct {
				JQL           string   `json:"jql"`
				Fields        []string `json:"fields"`
				NextPageToken string   `json:"nextPageToken"`
				MaxResults    int      `json:"maxResults"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode search body: %v", err)
				http.Error(w, "bad body", 400)
				return
			}
			// JQL we issue is either `parent = <KEY>` or `"Epic Link" = <KEY>`.
			var (
				key      string
				epicLink bool
			)
			if _, after, found := strings.Cut(body.JQL, `"Epic Link" = `); found {
				key = strings.TrimSpace(after)
				epicLink = true
			} else if _, after, found := strings.Cut(body.JQL, "parent = "); found {
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
				"issues": issues,
				"isLast": true,
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

// blockedByEdges returns blocked-jira-key → sorted blocker-jira-keys for the
// `blocks` deps stored in bd. Keys are derived from each bead's external_ref.
func blockedByEdges(t *testing.T, beadsDir string) map[string][]string {
	t.Helper()
	listOut, err := testutil.BdCmd(beadsDir, "--json", "list", "--all").Output()
	if err != nil {
		t.Fatalf("bd list: %v", err)
	}
	var rows []struct {
		ID          string `json:"id"`
		ExternalRef string `json:"external_ref"`
	}
	if err := json.Unmarshal(listOut, &rows); err != nil {
		t.Fatalf("parse bd list: %v", err)
	}
	idToKey := map[string]string{}
	ids := []string{}
	for _, r := range rows {
		if i := strings.LastIndex(r.ExternalRef, "/"); i >= 0 {
			idToKey[r.ID] = r.ExternalRef[i+1:]
			ids = append(ids, r.ID)
		}
	}
	edges := map[string][]string{}
	for _, id := range ids {
		out, err := testutil.BdCmd(beadsDir, "--json", "dep", "list", id, "-t", "blocks").Output()
		if err != nil {
			t.Fatalf("bd dep list %s: %v", id, err)
		}
		var deps []struct {
			ID             string `json:"id"`
			DependencyType string `json:"dependency_type"`
		}
		if err := json.Unmarshal(out, &deps); err != nil {
			t.Fatalf("parse bd dep list %s: %v", id, err)
		}
		for _, d := range deps {
			if d.DependencyType != "blocks" {
				continue
			}
			edges[idToKey[id]] = append(edges[idToKey[id]], idToKey[d.ID])
		}
		if v, ok := edges[idToKey[id]]; ok {
			sort.Strings(v)
		}
	}
	return edges
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

// TestRunPaginatesJQLSearch verifies the new token-based pagination loop:
// the search endpoint hands back two pages joined by an opaque nextPageToken
// and terminates with isLast=true.
func TestRunPaginatesJQLSearch(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/rest/api/2/issue/"):
			key := strings.TrimPrefix(r.URL.Path, "/rest/api/2/issue/")
			fields := map[string]any{
				"summary":     key + " summary",
				"description": key + " body",
				"issuetype":   map[string]any{"name": "Task"},
				"subtasks":    []any{},
				"issuelinks":  []any{},
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"key": key, "fields": fields})
		case r.URL.Path == "/rest/api/3/search/jql":
			if r.Method != http.MethodPost {
				http.Error(w, "method", 405)
				return
			}
			var body struct {
				JQL           string `json:"jql"`
				NextPageToken string `json:"nextPageToken"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad body", 400)
				return
			}
			// Only paginate the `parent = FOO-1` query; all others
			// return a single empty page.
			if body.JQL != "parent = FOO-1" {
				_ = json.NewEncoder(w).Encode(map[string]any{"issues": []any{}, "isLast": true})
				return
			}
			switch body.NextPageToken {
			case "":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"issues": []map[string]string{
						{"key": "FOO-2"},
						{"key": "FOO-3"},
					},
					"nextPageToken": "tok-2",
					"isLast":        false,
				})
			case "tok-2":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"issues": []map[string]string{
						{"key": "FOO-4"},
					},
					"isLast": true,
				})
			default:
				t.Errorf("unexpected nextPageToken %q", body.NextPageToken)
				http.Error(w, "bad token", 400)
			}
		default:
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
	}, []string{"FOO-1"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	tree := importedTree(t, beadsDir)
	want := map[string][]string{
		"FOO-1": {"FOO-2", "FOO-3", "FOO-4"},
		"FOO-2": nil,
		"FOO-3": nil,
		"FOO-4": nil,
	}
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

// TestRunBlockedBySiblingSubtasks covers the common case: two sibling
// subtasks where one is blocked by the other. Both are inside the imported
// tree so the dep must be wired.
func TestRunBlockedBySiblingSubtasks(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	fj := newFakeJira(t, map[string]fakeIssue{
		"FOO-1": {subtasks: []string{"FOO-2", "FOO-3"}},
		"FOO-2": {},
		"FOO-3": {blockedBy: []string{"FOO-2"}},
	})
	runJira(t, fj.server, beadsDir, "FOO-1")
	edges := blockedByEdges(t, beadsDir)
	want := map[string][]string{"FOO-3": {"FOO-2"}}
	if !equalTree(edges, want) {
		t.Errorf("blocked-by edges mismatch:\n got=%v\nwant=%v", edges, want)
	}
}

// TestRunBlockedByCrossLevel makes sure deps work across hierarchy levels:
// a deep descendant blocks an ancestor's sibling.
func TestRunBlockedByCrossLevel(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	fj := newFakeJira(t, map[string]fakeIssue{
		"FOO-1": {subtasks: []string{"FOO-2", "FOO-3"}},
		"FOO-2": {subtasks: []string{"FOO-4"}},
		"FOO-3": {blockedBy: []string{"FOO-4"}},
		"FOO-4": {},
	})
	runJira(t, fj.server, beadsDir, "FOO-1")
	edges := blockedByEdges(t, beadsDir)
	want := map[string][]string{"FOO-3": {"FOO-4"}}
	if !equalTree(edges, want) {
		t.Errorf("blocked-by edges mismatch:\n got=%v\nwant=%v", edges, want)
	}
}

// TestRunBlockedByExternalIgnored verifies external blockers and non-blocks
// link types produce no edges and no error.
func TestRunBlockedByExternalIgnored(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	fj := newFakeJira(t, map[string]fakeIssue{
		"FOO-1": {subtasks: []string{"FOO-2"}},
		// EXT-9 is not in the imported tree; the "relates to" link to
		// FOO-1 (also in-tree) must be ignored entirely.
		"FOO-2": {blockedBy: []string{"EXT-9"}, relatesTo: []string{"FOO-1"}},
	})
	runJira(t, fj.server, beadsDir, "FOO-1")
	edges := blockedByEdges(t, beadsDir)
	if len(edges) != 0 {
		t.Errorf("expected no blocked-by edges, got %v", edges)
	}
}

// bareRepoForTest creates a bare repo with one commit on main plus the
// given extra branches, and returns its absolute path. Mirrors the helper in
// dir/worktree_test.go.
func bareRepoForTest(t *testing.T, extraBranches ...string) string {
	t.Helper()
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	envv := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	gitIn := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = envv
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
		}
	}
	gitIn(source, "init", "-q", "-b", "main")
	gitIn(source, "commit", "--allow-empty", "-q", "-m", "init")
	for _, b := range extraBranches {
		gitIn(source, "checkout", "-q", "-b", b)
		gitIn(source, "commit", "--allow-empty", "-q", "-m", "on "+b)
		gitIn(source, "checkout", "-q", "main")
	}
	bare := filepath.Join(parent, "my-repo.git")
	c := exec.Command("git", "clone", "--bare", source, bare)
	c.Env = envv
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v\n%s", err, out)
	}
	abs, err := filepath.Abs(bare)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// listEntries reads the ticket's dirs metadata via `ticket dir list`.
func listEntries(t *testing.T, beadsDir, ticketID string) []map[string]any {
	t.Helper()
	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "list", ticketID)
	if code != 0 {
		t.Fatalf("ticket dir list: exit=%d stderr=%q", code, stderr)
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("parse %q: %v", stdout, err)
	}
	return entries
}

func TestRunDiscoverBranches(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	bare := bareRepoForTest(t, "topic/FOO-1-something")
	// Configure project.yaml to resolve owner/repo to the test bare.
	bareDir := filepath.Dir(bare)
	yaml := fmt.Sprintf("bare-repo: %q\n", filepath.Join(bareDir, "{repo-name}.git"))
	if err := os.WriteFile(filepath.Join(beadsDir, "project.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(beadsDir, "project.yaml")) })

	fj := newFakeJira(t, map[string]fakeIssue{"FOO-1": {}})

	// Fake gh runner: one org, one repo (matching the bare basename), one
	// branch containing the search term.
	now := time.Now()
	runner := func(_ context.Context, args []string) ([]byte, error) {
		switch {
		case len(args) >= 2 && args[0] == "api" && args[1] == "user/orgs":
			return []byte("knime\n"), nil
		case len(args) >= 2 && args[0] == "repo" && args[1] == "list":
			return json.Marshal([]map[string]any{{
				"name":     "my-repo",
				"pushedAt": now.Format(time.RFC3339),
			}})
		case len(args) >= 2 && args[0] == "api" && args[1] == "graphql":
			return json.Marshal(map[string]any{
				"data": map[string]any{
					"r0": map[string]any{
						"name": "my-repo",
						"refs": map[string]any{"nodes": []map[string]string{
							{"name": "topic/FOO-1-something"},
						}},
					},
				},
			})
		}
		return nil, fmt.Errorf("unexpected gh args: %v", args)
	}

	var stdout, stderr bytes.Buffer
	code := jiracli.RunWithOptions(jiracli.Options{
		BaseURL:  fj.server.URL,
		BeadsDir: beadsDir,
		Email:    "me@example.com",
		Token:    "tok",
		GHRunner: githubcli.GHRunner(runner),
	}, []string{"FOO-1", "--discover-branches"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	rootID := strings.TrimSpace(stdout.String())
	if rootID == "" {
		t.Fatalf("no root id: stderr=%q", stderr.String())
	}
	entries := listEntries(t, beadsDir, rootID)
	if len(entries) != 1 {
		t.Fatalf("entries=%v want 1", entries)
	}
	if got, _ := entries[0]["bareRepo"].(string); got != bare {
		t.Errorf("bareRepo=%q want %q", got, bare)
	}
	if got, _ := entries[0]["branch"].(string); got != "topic/FOO-1-something" {
		t.Errorf("branch=%q want discovered name %q", got, "topic/FOO-1-something")
	}
	wtPath, _ := entries[0]["path"].(string)
	out, err := exec.Command("git", "-C", wtPath, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "topic/FOO-1-something" {
		t.Errorf("worktree HEAD branch=%q want %q", got, "topic/FOO-1-something")
	}
}

// TestRunDiscoverBranchesMultiplePerRepo ensures every discovered branch on
// the same owner/repo produces its own worktree (no per-repo dedup).
func TestRunDiscoverBranchesMultiplePerRepo(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	bare := bareRepoForTest(t, "topic/FOO-1-a", "topic/FOO-1-b")
	bareDir := filepath.Dir(bare)
	// Path template must include {branch} so two branches don't collide.
	yaml := fmt.Sprintf("bare-repo: %q\nworktree:\n  path: %q\n",
		filepath.Join(bareDir, "{repo-name}.git"),
		filepath.Join(bareDir, "{bare-repo-name}-{branch}"))
	if err := os.WriteFile(filepath.Join(beadsDir, "project.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(beadsDir, "project.yaml")) })

	fj := newFakeJira(t, map[string]fakeIssue{"FOO-1": {}})

	now := time.Now()
	runner := func(_ context.Context, args []string) ([]byte, error) {
		switch {
		case len(args) >= 2 && args[0] == "api" && args[1] == "user/orgs":
			return []byte("knime\n"), nil
		case len(args) >= 2 && args[0] == "repo" && args[1] == "list":
			return json.Marshal([]map[string]any{{
				"name":     "my-repo",
				"pushedAt": now.Format(time.RFC3339),
			}})
		case len(args) >= 2 && args[0] == "api" && args[1] == "graphql":
			return json.Marshal(map[string]any{
				"data": map[string]any{
					"r0": map[string]any{
						"name": "my-repo",
						"refs": map[string]any{"nodes": []map[string]string{
							{"name": "topic/FOO-1-a"},
							{"name": "topic/FOO-1-b"},
						}},
					},
				},
			})
		}
		return nil, fmt.Errorf("unexpected gh args: %v", args)
	}

	var stdout, stderr bytes.Buffer
	code := jiracli.RunWithOptions(jiracli.Options{
		BaseURL:  fj.server.URL,
		BeadsDir: beadsDir,
		Email:    "me@example.com",
		Token:    "tok",
		GHRunner: githubcli.GHRunner(runner),
	}, []string{"FOO-1", "--discover-branches"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	rootID := strings.TrimSpace(stdout.String())
	entries := listEntries(t, beadsDir, rootID)
	if len(entries) != 2 {
		t.Fatalf("entries=%v want 2 (one worktree per discovered branch)", entries)
	}
	gotBranches := map[string]bool{}
	for _, e := range entries {
		b, _ := e["branch"].(string)
		gotBranches[b] = true
	}
	for _, want := range []string{"topic/FOO-1-a", "topic/FOO-1-b"} {
		if !gotBranches[want] {
			t.Errorf("missing entry for branch %q; got %v", want, gotBranches)
		}
	}
}

func TestRunDiscoverBranchesMissingBareRepoTemplate(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	fj := newFakeJira(t, map[string]fakeIssue{"FOO-1": {}})
	runner := func(context.Context, []string) ([]byte, error) {
		t.Errorf("gh runner should not be invoked when bare-repo template is missing")
		return nil, nil
	}
	var stdout, stderr bytes.Buffer
	code := jiracli.RunWithOptions(jiracli.Options{
		BaseURL:  fj.server.URL,
		BeadsDir: beadsDir,
		Email:    "me@example.com",
		Token:    "tok",
		GHRunner: githubcli.GHRunner(runner),
	}, []string{"FOO-1", "--discover-branches"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected nonzero exit; stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "bare-repo") {
		t.Errorf("stderr=%q missing bare-repo hint", stderr.String())
	}
}

// TestRunDiscoverBranchesBarePathMissing verifies the auto-clone fallback:
// when the bare repo for a discovered branch is missing, jira tries to clone
// it. Here we point bare-repo at an unwritable parent so the clone fails;
// pre-ai-vrc.79 this test asserted the "bare repo not found" error path,
// which no longer exists.
func TestRunDiscoverBranchesBarePathMissing(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	yaml := `bare-repo: "/definitely/does/not/exist/{repo-name}.git"`
	if err := os.WriteFile(filepath.Join(beadsDir, "project.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(beadsDir, "project.yaml")) })
	fj := newFakeJira(t, map[string]fakeIssue{"FOO-1": {}})
	now := time.Now()
	runner := func(_ context.Context, args []string) ([]byte, error) {
		switch {
		case len(args) >= 2 && args[0] == "api" && args[1] == "user/orgs":
			return []byte("knime\n"), nil
		case len(args) >= 2 && args[0] == "repo" && args[1] == "list":
			return json.Marshal([]map[string]any{{
				"name":     "ghost",
				"pushedAt": now.Format(time.RFC3339),
			}})
		case len(args) >= 2 && args[0] == "api" && args[1] == "graphql":
			return json.Marshal(map[string]any{
				"data": map[string]any{"r0": map[string]any{
					"name": "ghost",
					"refs": map[string]any{"nodes": []map[string]string{{"name": "topic/FOO-1"}}},
				}},
			})
		}
		return nil, fmt.Errorf("unexpected gh args: %v", args)
	}
	var stdout, stderr bytes.Buffer
	code := jiracli.RunWithOptions(jiracli.Options{
		BaseURL:  fj.server.URL,
		BeadsDir: beadsDir,
		Email:    "me@example.com",
		Token:    "tok",
		GHRunner: githubcli.GHRunner(runner),
	}, []string{"FOO-1", "--discover-branches"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected nonzero exit; stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "create parent dir") {
		t.Errorf("stderr=%q missing 'create parent dir' (expected auto-clone mkdir failure)", stderr.String())
	}
}

func TestRunJiraTypeOverride(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	yaml := `
jira-type:
  New node: feature
`
	if err := os.WriteFile(filepath.Join(beadsDir, "project.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(filepath.Join(beadsDir, "project.yaml")) })

	fj := newFakeJira(t, map[string]fakeIssue{
		"FOO-1": {issueType: "New node"},
	})
	rootID := runJira(t, fj.server, beadsDir, "FOO-1")
	out, err := testutil.BdCmd(beadsDir, "--json", "show", rootID).Output()
	if err != nil {
		t.Fatalf("bd show: %v", err)
	}
	var rows []struct {
		IssueType string `json:"issue_type"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatal(err)
	}
	if rows[0].IssueType != "feature" {
		t.Errorf("issue_type=%q want feature (New node → feature via project.yaml)", rows[0].IssueType)
	}
}
