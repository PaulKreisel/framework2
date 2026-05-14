package env_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/env"
	"framework2/internal/testutil"
)

func TestCreateUnderBeadsDir(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket := createBead(t, "env-default-root")

	got, err := env.Create("", beadsDir, ticket, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := filepath.Join(beadsDir, "envs", ticket)
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	assertMarker(t, got, ticket)
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(beadsDir, "envs", ticket)) })
}

func TestCreateWithRootOverride(t *testing.T) {
	testutil.SharedBdDB(t)
	ticket := createBead(t, "env-root-override")
	root := t.TempDir()

	got, err := env.Create("", "", ticket, root)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := filepath.Join(root, ticket)
	if got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	assertMarker(t, got, ticket)
}

func TestCreateIdempotent(t *testing.T) {
	testutil.SharedBdDB(t)
	ticket := createBead(t, "env-idempotent")
	root := t.TempDir()

	first, err := env.Create("", "", ticket, root)
	if err != nil {
		t.Fatalf("Create #1: %v", err)
	}
	second, err := env.Create("", "", ticket, root)
	if err != nil {
		t.Fatalf("Create #2: %v", err)
	}
	if first != second {
		t.Errorf("paths differ: %q vs %q", first, second)
	}
	assertMarker(t, second, ticket)
}

func TestCreateUnknownTicketErrors(t *testing.T) {
	testutil.SharedBdDB(t)
	root := t.TempDir()

	_, err := env.Create("", "", "tst-doesnotexist", root)
	if err == nil {
		t.Fatal("Create succeeded, want error")
	}
	// Folder must not have been created.
	if _, statErr := os.Stat(filepath.Join(root, "tst-doesnotexist")); statErr == nil {
		t.Error("env directory created despite failed ticket lookup")
	}
}

func TestCreateMissingBeadsDir(t *testing.T) {
	testutil.SharedBdDB(t)
	ticket := createBead(t, "env-missing-beadsdir")

	_, err := env.Create("", "", ticket, "")
	if err == nil {
		t.Fatal("Create succeeded, want error about missing BEADS_DIR")
	}
	if !strings.Contains(err.Error(), "BEADS_DIR") {
		t.Errorf("error = %v, want to mention BEADS_DIR", err)
	}
}

func TestEnvSubcommand(t *testing.T) {
	testutil.SharedBdDB(t)
	ticket := createBead(t, "env-subcommand")
	root := t.TempDir()

	stdout, stderr, code := testutil.RunTicket(t, "env", ticket, "--root", root)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	got := strings.TrimSpace(stdout)
	want := filepath.Join(root, ticket)
	if got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	assertMarker(t, got, ticket)
}

func TestEnvViaGlobalTicketFlag(t *testing.T) {
	testutil.SharedBdDB(t)
	ticket := createBead(t, "env-global-flag")
	root := t.TempDir()

	stdout, stderr, code := testutil.RunTicket(t, "--ticket", ticket, "env", "--root", root)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	got := strings.TrimSpace(stdout)
	want := filepath.Join(root, ticket)
	if got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	assertMarker(t, got, ticket)
}

func TestEnvConflictingIDForms(t *testing.T) {
	testutil.SharedBdDB(t)
	ticket := createBead(t, "env-conflict")
	root := t.TempDir()

	_, stderr, code := testutil.RunTicket(t, "--ticket", ticket, "env", ticket, "--root", root)
	if code == 0 {
		t.Fatalf("succeeded with both --ticket and positional, want usage error; stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "usage") {
		t.Errorf("stderr = %q, want a usage error", stderr)
	}
}

func assertMarker(t *testing.T, envPath, ticket string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(envPath, env.MarkerFile))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != ticket {
		t.Errorf("marker contains %q, want %q", got, ticket)
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
