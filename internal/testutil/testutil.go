// Package testutil provides shared test infrastructure for framework2.
//
// The central piece is [SharedBdDB], which exposes a persistent bd database
// living inside the repo at testdata/.beads (gitignored). The DB is created on
// first use via `bd init` and reused on subsequent test runs to avoid paying
// the 10-30s embedded-Dolt startup cost every time.
//
// Tests are responsible for cleaning up the beads they create (typically via
// `bd close`) so the next test sees a quiescent DB.
package testutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// SharedBdDB returns the absolute path to the persistent shared bd database.
// On first call per process, runs `bd init` if the DB does not yet exist.
//
// As a side effect, this sets BEADS_DIR for the calling test (scoped via
// t.Setenv) so that subprocess `bd` invocations target the shared test DB
// instead of the developer's real one.
//
// Tests using this helper must not call t.Parallel — t.Setenv forbids it,
// and the shared DB is global state that parallel tests would race on.
func SharedBdDB(t testing.TB) string {
	t.Helper()
	sharedOnce.Do(func() {
		sharedPath, sharedErr = ensureSharedDB()
	})
	if sharedErr != nil {
		t.Fatalf("testutil: setup shared bd DB: %v", sharedErr)
	}
	t.Setenv("BEADS_DIR", sharedPath)
	return sharedPath
}

var (
	sharedOnce sync.Once
	sharedPath string
	sharedErr  error
)

func ensureSharedDB() (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "testdata", ".beads")
	if _, err := os.Stat(filepath.Join(path, "embeddeddolt")); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create testdata dir: %w", err)
	}
	env := envWith(os.Environ(), map[string]string{
		"BEADS_DIR":          path,
		"BD_NON_INTERACTIVE": "1",
	})
	initCmd := exec.Command("bd", "init",
		"--prefix", "tst",
		"--skip-agents", "--skip-hooks")
	initCmd.Env = env
	if out, err := initCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("bd init: %v\n%s", err, out)
	}
	cfgCmd := exec.Command("bd", "config", "set", "dolt.auto-commit", "on")
	cfgCmd.Env = env
	if out, err := cfgCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("bd config set dolt.auto-commit on: %v\n%s", err, out)
	}
	return path, nil
}

// envWith returns env with the given key/value pairs overriding any existing
// entries for those keys.
func envWith(env []string, overrides map[string]string) []string {
	out := make([]string, 0, len(env)+len(overrides))
	for _, kv := range env {
		key := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			key = kv[:i]
		}
		if _, ok := overrides[key]; ok {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}

func repoRoot() (string, error) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("cannot determine testutil source location")
	}
	dir := filepath.Dir(filename)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found above testutil package")
		}
		dir = parent
	}
}
