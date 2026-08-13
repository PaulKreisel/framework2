// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package ticketcli_test

import (
	"strings"
	"testing"

	"framework2/internal/testutil"
)

// TestTicketAgentPromptRejectsResume asserts that `--prompt` paired with
// `--resume` is refused before any agent launch — a resumed transcript
// already has a turn 1 and a fresh auto-submitted user turn would be jarring.
func TestTicketAgentPromptRejectsResume(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "agent-prompt-resume-clash")

	_, errOut, code := testutil.RunTicketIn(t, beadsDir,
		"agent", ticket, "--prompt", "hi",
		"--resume", "00000000-0000-4000-8000-000000000000")
	if code == 0 {
		t.Fatalf("expected non-zero exit; stderr=%q", errOut)
	}
	if !strings.Contains(errOut, "--prompt and --resume") {
		t.Errorf("stderr = %q, want mention of '--prompt and --resume'", errOut)
	}
}

// TestTicketAgentPromptRejectsDryRun asserts that `--prompt` paired with
// `--dry-run` is refused — dry-run only prints the system prompt and never
// reaches the user-turn slot.
func TestTicketAgentPromptRejectsDryRun(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "agent-prompt-dryrun-clash")

	_, errOut, code := testutil.RunTicketIn(t, beadsDir,
		"agent", ticket, "--prompt", "hi", "--dry-run")
	if code == 0 {
		t.Fatalf("expected non-zero exit; stderr=%q", errOut)
	}
	if !strings.Contains(errOut, "--prompt and --dry-run") {
		t.Errorf("stderr = %q, want mention of '--prompt and --dry-run'", errOut)
	}
}

// TestTicketSpecPromptRejectsDryRun mirrors the agent-mode rejection on the
// spec surface.
func TestTicketSpecPromptRejectsDryRun(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	ticket := testutil.CreateBead(t, beadsDir, "spec-prompt-dryrun-clash")

	_, errOut, code := testutil.RunTicketIn(t, beadsDir,
		"spec", ticket, "--prompt", "hi", "--dry-run")
	if code == 0 {
		t.Fatalf("expected non-zero exit; stderr=%q", errOut)
	}
	if !strings.Contains(errOut, "--prompt and --dry-run") {
		t.Errorf("stderr = %q, want mention of '--prompt and --dry-run'", errOut)
	}
}

// TestTicketAgentPromptMissingValue asserts the bare `--prompt` (no value)
// errors cleanly rather than silently consuming the next positional.
func TestTicketAgentPromptMissingValue(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	testutil.CreateBead(t, beadsDir, "agent-prompt-no-value")

	_, errOut, code := testutil.RunTicketIn(t, beadsDir, "agent", "--prompt")
	if code == 0 {
		t.Fatalf("expected non-zero exit; stderr=%q", errOut)
	}
	if !strings.Contains(errOut, "--prompt requires a value") {
		t.Errorf("stderr = %q, want '--prompt requires a value'", errOut)
	}
}
