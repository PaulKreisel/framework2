// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package handover_test

import (
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/handover"
	"framework2/internal/testutil"
)

// TestHandoverProducesJSONL exercises the end-to-end flow: subtree discovery
// produces one host-stripped bead row per line in a `.jsonl` file. Code is
// not shipped — reviewers reconstruct work from each bead's `metadata.prs`
// (which the trailer-rewrite from ai-vrc.57.12 already attributes back to
// the right subtask).
func TestHandoverProducesJSONL(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "handover-parent")
	child := testutil.CreateChildBead(t, beadsDir, "handover-child", parent)

	sessionID := "33333333-3333-4333-8333-333333333333"
	if err := agent.RegisterAgent("", beadsDir, child, agent.AgentEntry{
		SessionID: sessionID,
		Mode:      agent.ModeAgent.String(),
		Conversation: []agent.ConvEntry{
			{Type: agent.ConvTypeUser, Prompt: "Implement the ticket"},
			{Type: agent.ConvTypeAssistantReply, Text: "Done — see handover-child for details."},
		},
	}); err != nil {
		t.Fatalf("register agent: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "handover", parent)
	if code != 0 {
		t.Fatalf("handover exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("handover stdout empty")
	}
	jsonlPath := lines[len(lines)-1]

	wantPrefix := filepath.Join(beadsDir, "handovers") + string(os.PathSeparator)
	if !strings.HasPrefix(jsonlPath, wantPrefix) {
		t.Errorf("output not under beads handovers dir: %q (want prefix %q)", jsonlPath, wantPrefix)
	}
	if !strings.HasSuffix(jsonlPath, ".jsonl") {
		t.Errorf("expected .jsonl suffix, got %q", jsonlPath)
	}

	raw, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}

	rows := parseJSONL(t, raw)
	if len(rows) != 2 {
		t.Errorf("row count = %d, want 2", len(rows))
	}
	gotIDs := map[string]bool{}
	for _, b := range rows {
		id, _ := b["id"].(string)
		gotIDs[id] = true
		md, _ := b["metadata"].(map[string]any)
		if md == nil {
			continue
		}
		dirs, _ := md["dirs"].([]any)
		for _, d := range dirs {
			e, _ := d.(map[string]any)
			for _, banned := range []string{"path", "localRepo", "bareRepo", "repo"} {
				if _, has := e[banned]; has {
					t.Errorf("host field %q leaked into bead %s: %v", banned, id, e)
				}
			}
		}
	}
	if !gotIDs[parent] || !gotIDs[child] {
		t.Errorf("rows missing expected ids: got %v", gotIDs)
	}

	// Conversations live under metadata.agents[].conversation[]; the
	// sessionID we registered must round-trip.
	if !strings.Contains(string(raw), sessionID) {
		t.Errorf("expected session id %q in metadata.agents[]", sessionID)
	}
}

// TestHandoverHonoursOutputFlag covers the --output override: caller picks
// the exact final file path.
func TestHandoverHonoursOutputFlag(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	root := testutil.CreateBead(t, beadsDir, "handover-output-flag")
	custom := filepath.Join(t.TempDir(), "my-handover.jsonl")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "handover", root, "--output", custom)
	if code != 0 {
		t.Fatalf("handover exit=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Errorf("custom output file not produced: %v", err)
	}
	raw, err := os.ReadFile(custom)
	if err != nil {
		t.Fatalf("read custom output: %v", err)
	}
	rows := parseJSONL(t, raw)
	if len(rows) != 1 {
		t.Errorf("row count = %d, want 1", len(rows))
	}
}

// TestHandoverSubtreeOnlyExcludesAncestors confirms we honour the
// "subtree only" decision: ancestors of <id> never appear.
func TestHandoverSubtreeOnlyExcludesAncestors(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	root := testutil.CreateBead(t, beadsDir, "handover-subtree-root")
	mid := testutil.CreateChildBead(t, beadsDir, "handover-subtree-mid", root)
	leaf := testutil.CreateChildBead(t, beadsDir, "handover-subtree-leaf", mid)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "handover", mid)
	if code != 0 {
		t.Fatalf("handover exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	jsonlPath := lines[len(lines)-1]

	raw, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	rows := parseJSONL(t, raw)
	got := map[string]bool{}
	for _, b := range rows {
		if id, _ := b["id"].(string); id != "" {
			got[id] = true
		}
	}
	if got[root] {
		t.Errorf("ancestor %s leaked into rows", root)
	}
	if !got[mid] || !got[leaf] {
		t.Errorf("expected mid+leaf in rows, got %v", got)
	}
}

// TestHandoverRejectsNoArchive covers the cleanup of the legacy flag:
// --no-archive must no longer be accepted (it was previously the toggle
// between tarball and staging-directory output).
func TestHandoverRejectsNoArchive(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	root := testutil.CreateBead(t, beadsDir, "handover-rejects-no-archive")
	_, _, code := testutil.RunTicketIn(t, beadsDir, "handover", root, "--no-archive")
	if code == 0 {
		t.Errorf("expected non-zero exit when --no-archive is passed")
	}
}

// TestHandoverUploadsToJira covers the happy path: when the ticket carries a
// Jira external_ref, the JSONL plus the three embedded review aids land on
// /rest/api/3/issue/<KEY>/attachments as a single multipart POST.
func TestHandoverUploadsToJira(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	root := testutil.CreateBead(t, beadsDir, "handover-jira-upload")
	if out, err := testutil.BdCmd(beadsDir, "update", root,
		"--external-ref", "https://jira.example.com/browse/UIEXT-9000").CombinedOutput(); err != nil {
		t.Fatalf("bd update --external-ref: %v: %s", err, out)
	}

	srv := newFakeJiraAttach(t, "UIEXT-9000")
	defer srv.Close()

	custom := filepath.Join(t.TempDir(), "handover-jira.jsonl")
	var stdout strings.Builder
	opts := handover.Options{Jira: handover.JiraOptions{
		BaseURL: srv.URL(),
		Email:   "tester@example.com",
		Token:   "secret",
	}}
	if err := handover.RunWithOptions(opts, "", beadsDir, root, custom, &stdout); err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}

	if got := srv.Count(); got != 1 {
		t.Fatalf("expected exactly 1 attach POST, got %d", got)
	}
	gotNames := srv.Filenames()
	sort.Strings(gotNames)
	wantNames := []string{
		filepath.Base(custom),
		"conversation.py",
		"handover_instructions.md",
		"overview.py",
	}
	sort.Strings(wantNames)
	if !equalStrings(gotNames, wantNames) {
		t.Errorf("attached files = %v, want %v", gotNames, wantNames)
	}
	if !strings.Contains(stdout.String(), "Jira upload OK") {
		t.Errorf("expected upload OK status line, got: %s", stdout.String())
	}
}

// TestHandoverSkipsUploadWhenNoJiraAncestor ensures we don't reach for Jira
// credentials (and therefore don't fail) when the ticket has no Jira ref.
func TestHandoverSkipsUploadWhenNoJiraAncestor(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	root := testutil.CreateBead(t, beadsDir, "handover-no-jira")

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	custom := filepath.Join(t.TempDir(), "handover-no-jira.jsonl")
	var stdout strings.Builder
	opts := handover.Options{Jira: handover.JiraOptions{BaseURL: srv.URL}}
	if err := handover.RunWithOptions(opts, "", beadsDir, root, custom, &stdout); err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	if called {
		t.Errorf("did not expect any Jira request, but server was hit")
	}
	if !strings.Contains(stdout.String(), "no Jira external_ref") {
		t.Errorf("expected skip status line, got: %s", stdout.String())
	}
}

// TestHandoverDoesNotInheritJiraFromAncestor confirms the upload is gated on
// the bead's own external_ref, not on its ancestors.
func TestHandoverDoesNotInheritJiraFromAncestor(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	root := testutil.CreateBead(t, beadsDir, "handover-jira-root")
	if out, err := testutil.BdCmd(beadsDir, "update", root,
		"--external-ref", "https://jira.example.com/browse/UIEXT-9001").CombinedOutput(); err != nil {
		t.Fatalf("bd update --external-ref: %v: %s", err, out)
	}
	child := testutil.CreateChildBead(t, beadsDir, "handover-jira-child", root)

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	custom := filepath.Join(t.TempDir(), "handover-child.jsonl")
	var stdout strings.Builder
	opts := handover.Options{Jira: handover.JiraOptions{BaseURL: srv.URL}}
	if err := handover.RunWithOptions(opts, "", beadsDir, child, custom, &stdout); err != nil {
		t.Fatalf("RunWithOptions: %v", err)
	}
	if called {
		t.Errorf("did not expect any Jira request — child bead has no own external_ref")
	}
	if !strings.Contains(stdout.String(), "no Jira external_ref") {
		t.Errorf("expected skip status line, got: %s", stdout.String())
	}
}

// --- helpers ---

// fakeJiraAttach captures multipart attachments POSTed to
// /rest/api/3/issue/<KEY>/attachments so the test can assert on filenames
// and request count.
type fakeJiraAttach struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	count     int
	filenames []string
}

func newFakeJiraAttach(t *testing.T, wantKey string) *fakeJiraAttach {
	t.Helper()
	f := &fakeJiraAttach{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := "/rest/api/3/issue/" + wantKey + "/attachments"
		if r.URL.Path != wantPath {
			t.Errorf("unexpected path %q (want %q)", r.URL.Path, wantPath)
			http.Error(w, "bad path", 404)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
			http.Error(w, "method", 405)
			return
		}
		if r.Header.Get("X-Atlassian-Token") != "no-check" {
			t.Errorf("missing X-Atlassian-Token: no-check header")
		}
		ct := r.Header.Get("Content-Type")
		if !strings.HasPrefix(ct, "multipart/form-data") {
			t.Errorf("expected multipart/form-data, got %q", ct)
			http.Error(w, "bad ct", 400)
			return
		}
		_, params, err := mime.ParseMediaType(ct)
		if err != nil {
			t.Errorf("parse Content-Type: %v", err)
			http.Error(w, "bad ct", 400)
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		var names []string
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("read part: %v", err)
				break
			}
			if part.FormName() != "file" {
				t.Errorf("expected form name 'file', got %q", part.FormName())
			}
			names = append(names, part.FileName())
			_, _ = io.Copy(io.Discard, part)
		}
		f.mu.Lock()
		f.count++
		f.filenames = append(f.filenames, names...)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	return f
}

func (f *fakeJiraAttach) URL() string { return f.srv.URL }
func (f *fakeJiraAttach) Close()      { f.srv.Close() }
func (f *fakeJiraAttach) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}
func (f *fakeJiraAttach) Filenames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.filenames))
	copy(out, f.filenames)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func parseJSONL(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	var rows []map[string]any
	for i, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("parse jsonl line %d: %v", i, err)
		}
		rows = append(rows, row)
	}
	return rows
}
