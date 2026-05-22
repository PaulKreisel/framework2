//go:build integration

package githubcli

import (
	"bytes"
	"strings"
	"testing"
)

// TestCodeIntegrationAgainstRealGH shells out to the real `gh` binary and
// runs `github code` end-to-end. Guarded by the `integration` build tag, so
// it is skipped by default (`go test ./...`). Run with:
//
//	go test -tags=integration ./internal/githubcli/ -run TestCodeIntegrationAgainstRealGH
//
// Requires an authenticated gh and membership in at least one org with code.
// The term is one that's almost guaranteed to be present somewhere ("README").
func TestCodeIntegrationAgainstRealGH(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"code", "README", "--threshold", "10"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "Total:") && !strings.Contains(out, "No results found.") {
		t.Errorf("unexpected output (no Total / no-results footer):\n%s", out)
	}
}
