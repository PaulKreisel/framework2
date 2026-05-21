package handover_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/testutil"
)

// TestHandoverProducesTarballAndDirectory exercises the end-to-end flow:
// subtree discovery + beads.jsonl + per-bead conversation.md + top-level
// commits.md + README + tar.gz packaging. We register an agent session,
// commit code, finish the leaf so refs/notes/agent-session is written,
// then run handover on the parent (which is still open and links the
// same bare).
func TestHandoverProducesTarballAndDirectory(t *testing.T) {
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
	writeAndCommit(t, childWT, "a.txt", "a\n", "feat: handover-child landing")
	mustRunTicket(t, beadsDir, "finish", child)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "handover", parent)
	if code != 0 {
		t.Fatalf("handover exit=%d stderr=%q", code, stderr)
	}
	// Final line is the artifact path — tar.gz under <beadsDir>/handovers/.
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("handover stdout empty")
	}
	tarPath := lines[len(lines)-1]
	if !strings.HasPrefix(tarPath, filepath.Join(beadsDir, "handovers")+string(os.PathSeparator)) {
		t.Errorf("tarball not under beads handovers dir: %q", tarPath)
	}
	if !strings.HasSuffix(tarPath, ".tar.gz") {
		t.Errorf("expected .tar.gz suffix, got %q", tarPath)
	}
	if _, err := os.Stat(tarPath); err != nil {
		t.Fatalf("tarball not created: %v", err)
	}
	// Staging dir should be gone in the default (archive) mode.
	if _, err := os.Stat(strings.TrimSuffix(tarPath, ".tar.gz")); !os.IsNotExist(err) {
		t.Errorf("staging dir still present after archive: err=%v", err)
	}

	files := extractTarball(t, tarPath)
	if _, ok := files["README.md"]; !ok {
		t.Errorf("README.md missing from tarball; got keys: %v", keys(files))
	}
	if _, ok := files["beads.jsonl"]; !ok {
		t.Errorf("beads.jsonl missing from tarball")
	}
	parentConv, ok := files["tickets/"+parent+"/conversation.md"]
	if !ok {
		t.Fatalf("parent conversation.md missing")
	}
	if !strings.Contains(string(parentConv), parent) {
		t.Errorf("parent conversation does not mention parent id")
	}
	// Child of parent should be linked as a relative md link.
	wantLink := "[" + child + "](../" + child + "/conversation.md)"
	if !strings.Contains(string(parentConv), wantLink) {
		t.Errorf("parent conversation missing child link %q", wantLink)
	}

	childConv, ok := files["tickets/"+child+"/conversation.md"]
	if !ok {
		t.Fatalf("child conversation.md missing")
	}
	if !strings.Contains(string(childConv), sessionID) {
		t.Errorf("child conversation missing session id %q", sessionID)
	}
	if !strings.Contains(string(childConv), "Implement the ticket") {
		t.Errorf("child conversation missing user prompt body")
	}

	// One top-level commits.md covers the whole subtree. After the child
	// finished, its commit was FF-rebased onto the parent's branch — we
	// scan the parent's worktree and attribute the commit back to the
	// child via the `ticketId` from the agent-session note.
	commitsMD, ok := files["commits.md"]
	if !ok {
		t.Fatalf("top-level commits.md missing; got keys: %v", keys(files))
	}
	cs := string(commitsMD)
	if !strings.Contains(cs, "| Ticket |") {
		t.Errorf("commits.md missing Ticket column header:\n%s", cs)
	}
	if strings.Contains(cs, "| Session |") {
		t.Errorf("commits.md still has Session column:\n%s", cs)
	}
	wantTicketLink := "[" + child + "](tickets/" + child + "/conversation.md)"
	if !strings.Contains(cs, wantTicketLink) {
		t.Errorf("commits.md missing child ticket link %q:\n%s", wantTicketLink, cs)
	}
	if strings.Contains(cs, sessionID) {
		t.Errorf("commits.md still surfaces session id %q (should be ticket-only):\n%s", sessionID, cs)
	}
	if !strings.Contains(cs, "feat: handover-child landing") {
		t.Errorf("commits.md missing commit subject:\n%s", cs)
	}
	// Per-bead commits.md files must no longer be emitted.
	for k := range files {
		if strings.HasPrefix(k, "tickets/") && strings.HasSuffix(k, "/commits.md") {
			t.Errorf("legacy per-bead commits.md leaked: %s", k)
		}
	}

	// beads.jsonl: subtree-filtered (just parent + child), host fields
	// stripped from metadata.dirs[].
	bj := string(files["beads.jsonl"])
	rows := strings.Split(strings.TrimRight(bj, "\n"), "\n")
	if len(rows) != 2 {
		t.Errorf("beads.jsonl row count = %d, want 2:\n%s", len(rows), bj)
	}
	gotIDs := map[string]bool{}
	for _, r := range rows {
		var row map[string]any
		if err := json.Unmarshal([]byte(r), &row); err != nil {
			t.Fatalf("parse jsonl row: %v\n%s", err, r)
		}
		id, _ := row["id"].(string)
		gotIDs[id] = true
		// metadata.dirs[].path / .bareRepo / .localRepo must have been stripped.
		md, _ := row["metadata"].(map[string]any)
		if md == nil {
			continue
		}
		dirs, _ := md["dirs"].([]any)
		for _, d := range dirs {
			e, _ := d.(map[string]any)
			for _, banned := range []string{"path", "localRepo", "bareRepo", "repo"} {
				if _, has := e[banned]; has {
					t.Errorf("metadata.dirs[].%s leaked into beads.jsonl row %s: %v", banned, id, e)
				}
			}
		}
	}
	if !gotIDs[parent] || !gotIDs[child] {
		t.Errorf("beads.jsonl missing expected ids: got %v", gotIDs)
	}
}

// TestHandoverNoArchiveLeavesDirectory verifies the --no-archive flag emits
// the staging directory in place and skips tarball creation.
func TestHandoverNoArchiveLeavesDirectory(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	root := testutil.CreateBead(t, beadsDir, "handover-no-archive-root")

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "handover", root, "--no-archive")
	if code != 0 {
		t.Fatalf("handover exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	dirPath := lines[len(lines)-1]
	info, err := os.Stat(dirPath)
	if err != nil {
		t.Fatalf("staging dir missing: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected a directory, got file at %s", dirPath)
	}
	if _, err := os.Stat(dirPath + ".tar.gz"); !os.IsNotExist(err) {
		t.Errorf("tarball produced despite --no-archive: err=%v", err)
	}
	for _, expected := range []string{"README.md", "beads.jsonl", filepath.Join("tickets", root, "conversation.md")} {
		if _, err := os.Stat(filepath.Join(dirPath, expected)); err != nil {
			t.Errorf("expected %s in handover dir: %v", expected, err)
		}
	}
}

// TestHandoverHonoursOutputFlag covers the --output override: caller picks
// the exact final path for both archive and no-archive modes.
func TestHandoverHonoursOutputFlag(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	root := testutil.CreateBead(t, beadsDir, "handover-output-flag")
	custom := filepath.Join(t.TempDir(), "my-handover")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "handover", root, "--output", custom, "--no-archive")
	if code != 0 {
		t.Fatalf("handover exit=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(custom, "README.md")); err != nil {
		t.Errorf("custom output dir not populated: %v", err)
	}
}

// TestHandoverSubtreeOnlyExcludesAncestors confirms we honour the
// "subtree only" decision: ancestors of <id> never appear in beads.jsonl
// nor in tickets/.
func TestHandoverSubtreeOnlyExcludesAncestors(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	root := testutil.CreateBead(t, beadsDir, "handover-subtree-root")
	mid := testutil.CreateChildBead(t, beadsDir, "handover-subtree-mid", root)
	leaf := testutil.CreateChildBead(t, beadsDir, "handover-subtree-leaf", mid)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "handover", mid, "--no-archive")
	if code != 0 {
		t.Fatalf("handover exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	dirPath := lines[len(lines)-1]

	bj, err := os.ReadFile(filepath.Join(dirPath, "beads.jsonl"))
	if err != nil {
		t.Fatalf("read beads.jsonl: %v", err)
	}
	got := map[string]bool{}
	for _, r := range strings.Split(strings.TrimRight(string(bj), "\n"), "\n") {
		var row struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(r), &row)
		got[row.ID] = true
	}
	if got[root] {
		t.Errorf("ancestor %s leaked into beads.jsonl", root)
	}
	if !got[mid] || !got[leaf] {
		t.Errorf("expected mid+leaf in beads.jsonl, got %v", got)
	}
	if _, err := os.Stat(filepath.Join(dirPath, "tickets", root, "conversation.md")); !os.IsNotExist(err) {
		t.Errorf("conversation.md for ancestor %s should not exist: err=%v", root, err)
	}
}

// --- helpers (mirrors of internal/finish/finish_test.go's helpers) ---

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

// extractTarball returns the contents of every regular file in the .tar.gz
// at path, keyed by entry name (forward-slash separated, with the leading
// root directory component stripped).
func extractTarball(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open tarball: %v", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, tr); err != nil {
			t.Fatalf("read tar entry %s: %v", hdr.Name, err)
		}
		// Strip the leading directory component (the bundle's root folder).
		name := hdr.Name
		if i := strings.Index(name, "/"); i >= 0 {
			name = name[i+1:]
		}
		files[name] = buf.Bytes()
	}
	return files
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
