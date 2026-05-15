package jiracli_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		if r.URL.Path != "/rest/api/2/issue/UIEXT-9999" {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.Error(w, "not found", 404)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "me@example.com" || pass != "tok" {
			http.Error(w, "unauth", 401)
			return
		}
		issue := map[string]any{
			"key": "UIEXT-9999",
			"fields": map[string]any{
				"summary":            "Make widgets clickable",
				"description":        "When the user clicks, things should happen.",
				"issuetype":          map[string]any{"name": "Enhancement"},
				"customfield_11444":  "1. click widget\n2. observe nothing\n",
			},
		}
		_ = json.NewEncoder(w).Encode(issue)
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
