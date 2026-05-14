package thiscli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/dir"
	"framework2/internal/env"
	"framework2/internal/testutil"
	"framework2/internal/thiscli"
)

func TestDiscoverFromEnvRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, env.MarkerFile), []byte("tst-123\n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	id, envDir, err := thiscli.Discover(dir)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if id != "tst-123" {
		t.Errorf("id = %q, want tst-123", id)
	}
	if envDir != dir {
		t.Errorf("envDir = %q, want %q", envDir, dir)
	}
}

func TestDiscoverFromNestedDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, env.MarkerFile), []byte("tst-abc\n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	id, envDir, err := thiscli.Discover(nested)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if id != "tst-abc" {
		t.Errorf("id = %q, want tst-abc", id)
	}
	if envDir != root {
		t.Errorf("envDir = %q, want %q", envDir, root)
	}
}

func TestDiscoverNoMarker(t *testing.T) {
	dir := t.TempDir()
	_, _, err := thiscli.Discover(dir)
	if err == nil {
		t.Fatal("Discover succeeded, want error")
	}
	if !strings.Contains(err.Error(), "not inside a ticket env") {
		t.Errorf("err = %v, want to mention 'not inside a ticket env'", err)
	}
}

func TestDiscoverEmptyMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, env.MarkerFile), []byte("   \n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	_, _, err := thiscli.Discover(dir)
	if err == nil {
		t.Fatal("Discover succeeded, want error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("err = %v, want to mention 'empty'", err)
	}
}

func TestThisForwardsToTicketcli(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket := createBead(t, "this-forwards")

	envPath, err := env.Create("", beadsDir, ticket)
	if err != nil {
		t.Fatalf("env.Create: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(beadsDir, "envs", ticket)) })

	stdout, stderr, code := testutil.RunThisFrom(t, envPath, "dir", "list")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	var entries []dir.Entry
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("parse stdout %q: %v", stdout, err)
	}
	if len(entries) != 0 {
		t.Errorf("entries = %+v, want empty slice", entries)
	}
}

func TestThisRejectsTicketFlag(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket := createBead(t, "this-rejects-flag")
	envPath, err := env.Create("", beadsDir, ticket)
	if err != nil {
		t.Fatalf("env.Create: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(beadsDir, "envs", ticket)) })

	_, stderr, code := testutil.RunThisFrom(t, envPath, "--ticket", "tst-other", "dir", "list")
	if code == 0 {
		t.Fatalf("succeeded despite --ticket; stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "do not pass --ticket") {
		t.Errorf("stderr = %q, want to mention 'do not pass --ticket'", stderr)
	}
}

func TestThisOutsideAnyEnv(t *testing.T) {
	testutil.SharedBdDB(t)
	dir := t.TempDir()

	_, stderr, code := testutil.RunThisFrom(t, dir, "dir", "list")
	if code == 0 {
		t.Fatalf("succeeded outside env, stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "not inside a ticket env") {
		t.Errorf("stderr = %q, want to mention 'not inside a ticket env'", stderr)
	}
}

func createBead(t *testing.T, title string) string {
	t.Helper()
	out, err := exec.Command("bd", "create", title, "--silent").Output()
	if err != nil {
		t.Fatalf("bd create: %v", err)
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("bd", "close", id).Run() })
	return id
}
