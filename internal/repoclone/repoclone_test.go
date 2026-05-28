package repoclone_test

import (
	"os"
	"path/filepath"
	"testing"

	"framework2/internal/repoclone"
)

// TestEnsureBareNoOpWhenPresent verifies that EnsureBare is a no-op (no clone,
// no setup-script run) when the bare path already exists, even if the setup
// script would fail. This guards the "freshly-cloned case only" rule.
func TestEnsureBareNoOpWhenPresent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	bare := filepath.Join(dir, "already-here.git")
	if err := os.Mkdir(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	// /bin/false would exit non-zero — proves we never call it.
	if err := repoclone.EnsureBare(bare, "knime", "x", "/bin/false"); err != nil {
		t.Errorf("EnsureBare on existing path errored: %v", err)
	}
}
