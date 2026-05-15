// Package testutil provides shared test infrastructure for framework2.
//
// Each test package opts into the shared bd database by adding:
//
//	func TestMain(m *testing.M) { os.Exit(testutil.RunTestMain(m)) }
//
// [RunTestMain] provisions a per-package bd DB at <repo>/testdata/<pkg>/.beads
// (gitignored), runs a long-lived `dolt sql-server` against it for the
// lifetime of the test process, and tears it down on exit. Per-package data
// dirs let `go test ./...` run packages in parallel without contending on a
// single Dolt directory.
//
// Tests then call [SharedBdDB] to discover the path (BEADS_DIR is also set
// process-wide so subprocess `bd` invocations connect to the same server via
// the dolt-server.port file).
//
// Tests must clean up every bead they create (typically with `bd close`) —
// the DB is shared across tests in a package and across runs.
package testutil

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// SharedBdDB returns the absolute path to this test process's bd database. It
// requires that [RunTestMain] has already provisioned the DB from the
// package's TestMain — there is no lazy init.
//
// As a defensive measure it re-sets BEADS_DIR in the test's scope via
// t.Setenv, so tests cannot accidentally lose it by mutating env earlier.
// That also means tests using this helper must not call t.Parallel.
func SharedBdDB(t testing.TB) string {
	t.Helper()
	path := os.Getenv("BEADS_DIR")
	if path == "" {
		t.Fatalf("testutil.SharedBdDB: BEADS_DIR is unset — does the test package's TestMain call testutil.RunTestMain?")
	}
	t.Setenv("BEADS_DIR", path)
	return path
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
