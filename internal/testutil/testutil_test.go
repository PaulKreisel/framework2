package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(RunTestMain(m)) }

func TestSharedBdDB(t *testing.T) {
	path := SharedBdDB(t)
	if path == "" {
		t.Fatal("got empty path")
	}
	mode, db, ok := readMetadata(path)
	if !ok {
		t.Fatalf("expected metadata.json under %s", path)
	}
	if mode != "server" {
		t.Errorf("dolt_mode=%q, want %q", mode, "server")
	}
	if _, err := os.Stat(filepath.Join(path, "dolt", db)); err != nil {
		t.Errorf("expected dolt/%s data dir under %s: %v", db, path, err)
	}
	if _, err := os.Stat(filepath.Join(path, "dolt-server.port")); err != nil {
		t.Errorf("expected dolt-server.port under %s: %v", path, err)
	}
	if got := os.Getenv("BEADS_DIR"); got != path {
		t.Fatalf("BEADS_DIR = %q, want %q", got, path)
	}
	out, err := exec.Command("bd", "ready", "--json").CombinedOutput()
	if err != nil {
		t.Fatalf("bd ready against shared DB: %v\n%s", err, out)
	}
}
