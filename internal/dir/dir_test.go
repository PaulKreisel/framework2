// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package dir_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"framework2/internal/dir"
	"framework2/internal/testutil"
)

func TestDirLinkGitRepoWithRemote(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "dir-link-remote")
	path := gitRepo(t, true, false)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, path)
	if code != 0 {
		t.Fatalf("link exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v, want 1", entries)
	}
	got := entries[0]
	want := dir.Entry{Path: path, Repo: "git@example.com:org/repo.git", Branch: "main"}
	if got != want {
		t.Errorf("entry = %+v, want %+v", got, want)
	}
}

func TestDirLinkGitRepoWithoutRemote(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "dir-link-local")
	path := gitRepo(t, false, false)

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, path)
	if code != 0 {
		t.Fatalf("link exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v, want 1", entries)
	}
	got := entries[0]
	if got.Path != path {
		t.Errorf("path=%q want %q", got.Path, path)
	}
	if got.Repo != "" {
		t.Errorf("repo=%q want empty", got.Repo)
	}
	if got.LocalRepo == "" {
		t.Error("localRepo empty, want git toplevel")
	}
	if got.Branch != "main" {
		t.Errorf("branch=%q want main", got.Branch)
	}
}

func TestDirLinkNonGitPath(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "dir-link-nongit")
	path := t.TempDir()

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, path)
	if code != 0 {
		t.Fatalf("link exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Fatalf("entries=%+v, want 1", entries)
	}
	got := entries[0]
	want := dir.Entry{Path: path}
	if got != want {
		t.Errorf("entry = %+v, want %+v", got, want)
	}
}

func TestDirLinkDetachedHead(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "dir-link-detached")
	path := gitRepo(t, false, true)

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, path)
	if code == 0 {
		t.Fatalf("link succeeded, want error")
	}
	if !strings.Contains(stderr, "detached") {
		t.Errorf("stderr=%q, want to mention 'detached'", stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 0 {
		t.Errorf("entries=%+v, want none", entries)
	}
}

func TestDirLinkAlreadyLinked(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "dir-link-dup")
	path := gitRepo(t, true, false)

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, path); code != 0 {
		t.Fatalf("first link failed: exit=%d stderr=%q", code, stderr)
	}
	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, path)
	if code == 0 {
		t.Fatalf("second link succeeded, want error")
	}
	if !strings.Contains(stderr, "already linked") {
		t.Errorf("stderr=%q, want to mention 'already linked'", stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 1 {
		t.Errorf("entries=%+v, want exactly 1", entries)
	}
}

func TestDirRemoveExisting(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "dir-remove")
	path := gitRepo(t, true, false)

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "link", ticket, path); code != 0 {
		t.Fatalf("link: exit=%d stderr=%q", code, stderr)
	}
	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "remove", ticket, path); code != 0 {
		t.Fatalf("remove: exit=%d stderr=%q", code, stderr)
	}
	entries := listEntries(t, beadsDir, ticket)
	if len(entries) != 0 {
		t.Errorf("entries=%+v, want none", entries)
	}
}

func TestDirRemoveNonExistent(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "dir-remove-missing")

	_, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "remove", ticket, "/nonexistent/path")
	if code == 0 {
		t.Fatal("remove succeeded, want error")
	}
	if !strings.Contains(stderr, "not linked") {
		t.Errorf("stderr=%q, want to mention 'not linked'", stderr)
	}
}

func listEntries(t *testing.T, beadsDir, ticketID string) []dir.Entry {
	t.Helper()
	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "dir", "list", ticketID)
	if code != 0 {
		t.Fatalf("dir list: exit=%d stderr=%q", code, stderr)
	}
	var entries []dir.Entry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("parse list output %q: %v", stdout, err)
	}
	return entries
}

func gitRepo(t *testing.T, withRemote, detached bool) string {
	t.Helper()
	d := t.TempDir()
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	runGit := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = d
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	runGit("init", "-q", "-b", "main")
	if withRemote {
		runGit("remote", "add", "origin", "git@example.com:org/repo.git")
	}
	runGit("commit", "--allow-empty", "-q", "-m", "init")
	if detached {
		sha := runGit("rev-parse", "HEAD")
		runGit("checkout", "-q", "--detach", sha)
	}
	return d
}
