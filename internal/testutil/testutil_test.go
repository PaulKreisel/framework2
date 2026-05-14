package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSharedBdDB(t *testing.T) {
	path := SharedBdDB(t)
	if path == "" {
		t.Fatal("got empty path")
	}
	if _, err := os.Stat(filepath.Join(path, "embeddeddolt")); err != nil {
		t.Fatalf("expected embeddeddolt under %s: %v", path, err)
	}
	if got := os.Getenv("BEADS_DIR"); got != path {
		t.Fatalf("BEADS_DIR = %q, want %q", got, path)
	}
	out, err := exec.Command("bd", "ready", "--json").CombinedOutput()
	if err != nil {
		t.Fatalf("bd ready against shared DB: %v\n%s", err, out)
	}
}
