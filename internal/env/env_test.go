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

	got, err := env.Create("", beadsDir, ticket)
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

func TestCreateIdempotent(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket := createBead(t, "env-idempotent")

	first, err := env.Create("", beadsDir, ticket)
	if err != nil {
		t.Fatalf("Create #1: %v", err)
	}
	second, err := env.Create("", beadsDir, ticket)
	if err != nil {
		t.Fatalf("Create #2: %v", err)
	}
	if first != second {
		t.Errorf("paths differ: %q vs %q", first, second)
	}
	assertMarker(t, second, ticket)
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(beadsDir, "envs", ticket)) })
}

func TestCreateUnknownTicketErrors(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)

	_, err := env.Create("", beadsDir, "tst-doesnotexist")
	if err == nil {
		t.Fatal("Create succeeded, want error")
	}
	// Folder must not have been created.
	if _, statErr := os.Stat(filepath.Join(beadsDir, "envs", "tst-doesnotexist")); statErr == nil {
		t.Error("env directory created despite failed ticket lookup")
	}
}

func TestCreateMissingBeadsDir(t *testing.T) {
	testutil.SharedBdDB(t)
	ticket := createBead(t, "env-missing-beadsdir")

	_, err := env.Create("", "", ticket)
	if err == nil {
		t.Fatal("Create succeeded, want error about missing BEADS_DIR")
	}
	if !strings.Contains(err.Error(), "BEADS_DIR") {
		t.Errorf("error = %v, want to mention BEADS_DIR", err)
	}
}

func TestEnvSubcommand(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket := createBead(t, "env-subcommand")

	stdout, stderr, code := testutil.RunTicket(t, "env", ticket)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	got := strings.TrimSpace(stdout)
	want := filepath.Join(beadsDir, "envs", ticket)
	if got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	assertMarker(t, got, ticket)
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(beadsDir, "envs", ticket)) })
}

func TestEnvViaGlobalTicketFlag(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket := createBead(t, "env-global-flag")

	stdout, stderr, code := testutil.RunTicket(t, "--ticket", ticket, "env")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	got := strings.TrimSpace(stdout)
	want := filepath.Join(beadsDir, "envs", ticket)
	if got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	assertMarker(t, got, ticket)
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(beadsDir, "envs", ticket)) })
}

func TestEnvConflictingIDForms(t *testing.T) {
	testutil.SharedBdDB(t)
	ticket := createBead(t, "env-conflict")

	_, stderr, code := testutil.RunTicket(t, "--ticket", ticket, "env", ticket)
	if code == 0 {
		t.Fatalf("succeeded with both --ticket and positional, want usage error; stderr=%q", stderr)
	}
	if !strings.Contains(stderr, "usage") {
		t.Errorf("stderr = %q, want a usage error", stderr)
	}
}

// TestCreateRefusesFinishStepBead: a bead carrying the finish-step label is
// step-bead bookkeeping, not a ticket; Create must refuse it and leave no
// env folder behind.
func TestCreateRefusesFinishStepBead(t *testing.T) {
	beadsDir := testutil.SharedBdDB(t)
	ticket := createBead(t, "env-step-bead")
	if out, err := exec.Command("bd", "label", "add", ticket, "finish-step").CombinedOutput(); err != nil {
		t.Fatalf("bd label add: %v: %s", err, out)
	}

	_, err := env.Create("", beadsDir, ticket)
	if err == nil {
		t.Fatal("Create succeeded on a finish-step bead, want refusal")
	}
	if !strings.Contains(err.Error(), "finish-step") {
		t.Errorf("error = %v, want it to mention finish-step", err)
	}
	if _, statErr := os.Stat(filepath.Join(beadsDir, "envs", ticket)); statErr == nil {
		t.Error("env directory created despite finish-step refusal")
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(beadsDir, "envs", ticket)) })
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
