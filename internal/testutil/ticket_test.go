// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package testutil

import (
	"strings"
	"testing"
)

func TestRunTicketUsage(t *testing.T) {
	stdout, stderr, code := RunTicket(t)
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "usage:") {
		t.Errorf("stderr missing usage; got %q", stderr)
	}
}

func TestRunTicketHelp(t *testing.T) {
	stdout, stderr, code := RunTicket(t, "--help")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "usage:") {
		t.Errorf("stdout missing usage; got %q", stdout)
	}
}
