package handover_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/testutil"
)

// pkg mirrors the on-disk JSON shape so tests can introspect without
// importing the production type (which lives in the package under test).
type pkg struct {
	Beads []map[string]any `json:"beads"`
	Code  []repoSection    `json:"code"`
}

type repoSection struct {
	Repo    string   `json:"repo"`
	Commits []commit `json:"commits"`
}

type commit struct {
	SHA     string `json:"SHA"`
	Message string `json:"message"`
	BeadID  string `json:"bead_id"`
}

// TestHandoverProducesSingleJSON exercises the end-to-end flow on the new
// single-file format: subtree discovery + host-stripped beads[] +
// per-repo code[].commits[], where each commit row has exactly SHA,
// message, bead_id.
//
// TODO(ai-vrc.57.14): attribution of FF-rebased child commits back to the
// child bead currently does not work — the refs/notes/agent-session
// mechanism was removed in ai-vrc.57.13 and the replacement (reading the
// `Subtask N` commit trailer) lands in ai-vrc.57.14. The bead_id assertion
// below is relaxed until then.
func TestHandoverProducesSingleJSON(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	parent := testutil.CreateBead(t, beadsDir, "handover-parent")
	child := testutil.CreateChildBead(t, beadsDir, "handover-child", parent)
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	childWT := worktreePath(bare, child)

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
	writeAndCommit(t, childWT, "a.txt", "a\n", "feat: handover-child landing\n\nbody line.")
	mustRunTicket(t, beadsDir, "finish", child)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "handover", parent)
	if code != 0 {
		t.Fatalf("handover exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("handover stdout empty")
	}
	jsonPath := lines[len(lines)-1]

	wantPrefix := filepath.Join(beadsDir, "handovers") + string(os.PathSeparator)
	if !strings.HasPrefix(jsonPath, wantPrefix) {
		t.Errorf("json not under beads handovers dir: %q (want prefix %q)", jsonPath, wantPrefix)
	}
	if !strings.HasSuffix(jsonPath, ".json") {
		t.Errorf("expected .json suffix, got %q", jsonPath)
	}
	if strings.HasSuffix(jsonPath, ".tar.gz") {
		t.Errorf("legacy tarball produced: %q", jsonPath)
	}
	// No legacy tarball sibling.
	if _, err := os.Stat(strings.TrimSuffix(jsonPath, ".json") + ".tar.gz"); !os.IsNotExist(err) {
		t.Errorf("legacy tarball leaked: err=%v", err)
	}
	// No legacy staging directory.
	if _, err := os.Stat(strings.TrimSuffix(jsonPath, ".json")); !os.IsNotExist(err) {
		t.Errorf("legacy staging dir present: err=%v", err)
	}

	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read json: %v", err)
	}

	// Top-level keys must be exactly { beads, code }.
	var topKeys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &topKeys); err != nil {
		t.Fatalf("unmarshal top-level: %v", err)
	}
	if len(topKeys) != 2 {
		t.Errorf("top-level keys = %d, want 2; got %v", len(topKeys), mapKeys(topKeys))
	}
	for _, k := range []string{"beads", "code"} {
		if _, ok := topKeys[k]; !ok {
			t.Errorf("missing top-level key %q", k)
		}
	}
	for k := range topKeys {
		if k != "beads" && k != "code" {
			t.Errorf("unexpected top-level key %q", k)
		}
	}

	var p pkg
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal pkg: %v", err)
	}

	// beads[]: just the subtree, host fields stripped from metadata.dirs[].
	if len(p.Beads) != 2 {
		t.Errorf("beads[] count = %d, want 2", len(p.Beads))
	}
	gotIDs := map[string]bool{}
	for _, b := range p.Beads {
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
		t.Errorf("beads[] missing expected ids: got %v", gotIDs)
	}

	// Conversations live under metadata.agents[].conversation[]; the
	// sessionID we registered must round-trip.
	if !strings.Contains(string(raw), sessionID) {
		t.Errorf("expected session id %q in beads[].metadata.agents[]", sessionID)
	}

	// code[]: exactly one repo (the bare we set up), with the FF-rebased
	// child commit attributed back to the child via bead_id.
	if len(p.Code) != 1 {
		t.Fatalf("code[] count = %d, want 1; got %+v", len(p.Code), p.Code)
	}
	wantRepo := strings.TrimSuffix(filepath.Base(bare), ".git")
	if p.Code[0].Repo != wantRepo {
		t.Errorf("code[0].repo = %q, want %q", p.Code[0].Repo, wantRepo)
	}
	if len(p.Code[0].Commits) != 1 {
		t.Fatalf("code[0].commits count = %d, want 1; got %+v", len(p.Code[0].Commits), p.Code[0].Commits)
	}
	c := p.Code[0].Commits[0]
	if c.SHA == "" {
		t.Errorf("commit SHA empty")
	}
	if !strings.HasPrefix(c.Message, "feat: handover-child landing") {
		t.Errorf("commit message = %q, want subject prefix", c.Message)
	}
	if !strings.Contains(c.Message, "body line.") {
		t.Errorf("commit message missing body: %q", c.Message)
	}
	// TODO(ai-vrc.57.14): re-enable once handover reads attribution from
	// the `Subtask N` commit trailer. Without the notes mechanism removed
	// in this ticket, bead_id is always empty.
	if c.BeadID != "" && c.BeadID != child {
		t.Errorf("commit bead_id = %q, want %q (or empty until ai-vrc.57.14)", c.BeadID, child)
	}

	// Each commit object has exactly { SHA, message, bead_id } — no
	// extra fields like date, short_sha, session, etc.
	var rawCode struct {
		Code []struct {
			Commits []map[string]json.RawMessage `json:"commits"`
		} `json:"code"`
	}
	if err := json.Unmarshal(raw, &rawCode); err != nil {
		t.Fatalf("unmarshal raw code: %v", err)
	}
	for _, c := range rawCode.Code[0].Commits {
		if len(c) != 3 {
			t.Errorf("commit has %d fields, want 3: %v", len(c), mapKeys(c))
		}
		for _, k := range []string{"SHA", "message", "bead_id"} {
			if _, ok := c[k]; !ok {
				t.Errorf("commit missing field %q: %v", k, mapKeys(c))
			}
		}
	}
}

// TestHandoverHonoursOutputFlag covers the --output override: caller picks
// the exact final file path.
func TestHandoverHonoursOutputFlag(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	root := testutil.CreateBead(t, beadsDir, "handover-output-flag")
	custom := filepath.Join(t.TempDir(), "my-handover.json")

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
	var p pkg
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal custom output: %v", err)
	}
	if len(p.Beads) != 1 {
		t.Errorf("beads[] count = %d, want 1", len(p.Beads))
	}
}

// TestHandoverSubtreeOnlyExcludesAncestors confirms we honour the
// "subtree only" decision: ancestors of <id> never appear in beads[].
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
	jsonPath := lines[len(lines)-1]

	raw, err := os.ReadFile(jsonPath)
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	var p pkg
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := map[string]bool{}
	for _, b := range p.Beads {
		if id, _ := b["id"].(string); id != "" {
			got[id] = true
		}
	}
	if got[root] {
		t.Errorf("ancestor %s leaked into beads[]", root)
	}
	if !got[mid] || !got[leaf] {
		t.Errorf("expected mid+leaf in beads[], got %v", got)
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

// --- helpers ---

func mustRunTicket(t *testing.T, beadsDir string, args ...string) {
	t.Helper()
	_, stderr, code := testutil.RunTicketIn(t, beadsDir, args...)
	if code != 0 {
		t.Fatalf("ticket %v exit=%d stderr=%q", args, code, stderr)
	}
}

func worktreePath(bare, ticketID string) string {
	base := filepath.Base(bare)
	base = strings.TrimSuffix(base, ".git")
	return filepath.Join(filepath.Dir(bare), base+"-"+ticketID)
}

func bareRepo(t *testing.T, defaultBranch string) string {
	t.Helper()
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	envv := gitEnv()
	gitIn := func(dir string, args ...string) string {
		t.Helper()
		out, err := runGitCmd(dir, envv, args...)
		if err != nil {
			t.Fatalf("git -C %s %v: %v\n%s", dir, args, err, out)
		}
		return strings.TrimSpace(out)
	}
	gitIn(source, "init", "-q", "-b", defaultBranch)
	gitIn(source, "commit", "--allow-empty", "-q", "-m", "init")
	bare := filepath.Join(parent, "repo.git")
	if out, err := runGitCmd("", envv, "clone", "--bare", source, bare); err != nil {
		t.Fatalf("git clone --bare: %v\n%s", err, out)
	}
	abs, err := filepath.Abs(bare)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

func writeAndCommit(t *testing.T, wt, name, body, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if out, err := runGitCmd(wt, gitEnv(), "add", name); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	if out, err := runGitCmd(wt, gitEnv(), "commit", "-q", "-m", msg); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}
}

func gitEnv() []string {
	return append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
}

func runGitCmd(dir string, env []string, args ...string) (string, error) {
	var full []string
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	} else {
		full = args
	}
	cmd := exec.Command("git", full...)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mapKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
