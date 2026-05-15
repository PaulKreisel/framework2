package testutil

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

// RunTestMain is the entry point that test packages should invoke from their
// own TestMain. It provisions a per-package bd database under
// <repo>/testdata/<pkg>/.beads, starts a long-lived dolt sql-server pointed at
// it, runs the package's tests, and tears the server down on exit (including
// SIGINT/SIGTERM).
//
// Each test package's TestMain should be exactly:
//
//	func TestMain(m *testing.M) { os.Exit(testutil.RunTestMain(m)) }
//
// Per-package data dirs let multiple test binaries run in parallel under
// `go test ./...` without fighting over the same Dolt data directory.
//
// The bd init is idempotent: on subsequent runs the existing server-mode
// .beads/ is reused.
func RunTestMain(m *testing.M) int {
	pkg := testBinaryPackageName()
	root, err := repoRoot()
	if err != nil {
		fail("repo root: %v", err)
	}
	beadsDir := filepath.Join(root, "testdata", pkg, ".beads")

	if err := ensureServerModeDB(beadsDir); err != nil {
		fail("provision %s: %v", beadsDir, err)
	}
	if err := startDoltServer(beadsDir); err != nil {
		fail("start dolt server in %s: %v", beadsDir, err)
	}

	stop := stopOnce(func() { stopDoltServer(beadsDir) })
	defer stop()
	installSignalHandler(stop)

	// Tests inherit these via exec.Command; subprocess bd connects via the
	// dolt-server.port file in BEADS_DIR.
	os.Setenv("BEADS_DIR", beadsDir)
	os.Setenv("BEADS_DOLT_AUTO_START", "0")

	// One serial read warms the server and forces compat-migrations to
	// complete before any test-body parallelism (per the ai-vrc.8.1 spike).
	if err := warmServer(beadsDir); err != nil {
		stop()
		fail("warm server: %v", err)
	}

	// Drop leftover per-test databases from previous interrupted runs before
	// any test starts. The pool we initialise below creates fresh ones; the
	// stale-cleanup is just so we don't accumulate them on the server forever.
	cleanStaleTestDBs(beadsDir)

	// Stock the per-test database pool so [TestDB] handles in parallel-safe
	// constant time. Sized to GOMAXPROCS — Go's default per-package test
	// parallelism — so a fully-parallel package never blocks on the pool.
	host, port, err := readServerAddr(beadsDir)
	if err != nil {
		stop()
		fail("read server addr: %v", err)
	}
	// Pool is initialised lazily on the first [TestDB] call so packages that
	// only use [SharedBdDB] (env, next, thiscli, testutil itself) don't pay
	// the bd-init startup cost. See [TestDB] for the pool config.
	poolHost, poolPort = host, port

	code := m.Run()
	dropPoolEntries()
	return code
}

// testBinaryPackageName returns the test binary's basename without the .test
// suffix, e.g. "finish" for the binary built from internal/finish.
func testBinaryPackageName() string {
	return strings.TrimSuffix(filepath.Base(os.Args[0]), ".test")
}

// ensureServerModeDB makes beadsDir a usable server-mode bd database. If a
// server-mode DB already exists it is reused as-is. Otherwise any partial
// state is wiped and bd init produces a fresh embedded DB that is then flipped
// to server mode (per the ai-vrc.8.1 spike incantation).
func ensureServerModeDB(beadsDir string) error {
	if mode, db, ok := readMetadata(beadsDir); ok && mode == "server" {
		if _, err := os.Stat(filepath.Join(beadsDir, "dolt", db)); err == nil {
			return nil
		}
	}
	if err := os.RemoveAll(beadsDir); err != nil {
		return fmt.Errorf("clean stale dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(beadsDir), 0o755); err != nil {
		return fmt.Errorf("create parent: %w", err)
	}
	env := envWith(os.Environ(), map[string]string{
		"BEADS_DIR":          beadsDir,
		"BD_NON_INTERACTIVE": "1",
	})
	initCmd := exec.Command("bd", "init",
		"--prefix", "tst",
		"--skip-agents", "--skip-hooks")
	initCmd.Env = env
	if out, err := initCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd init: %v\n%s", err, out)
	}
	if err := flipToServerMode(beadsDir); err != nil {
		return fmt.Errorf("flip to server mode: %w", err)
	}
	cfgCmd := exec.Command("bd", "config", "set", "dolt.auto-commit", "on")
	cfgCmd.Env = env
	if out, err := cfgCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd config set dolt.auto-commit on: %v\n%s", err, out)
	}
	return nil
}

func readMetadata(beadsDir string) (mode, db string, ok bool) {
	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		return "", "", false
	}
	var m struct {
		DoltMode     string `json:"dolt_mode"`
		DoltDatabase string `json:"dolt_database"`
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return "", "", false
	}
	return m.DoltMode, m.DoltDatabase, true
}

// flipToServerMode edits metadata.json and moves the on-disk Dolt directory so
// that bd will treat beadsDir as a sql-server-mode DB. Mirrors the spike's
// hand-edit recipe (jq + mv) in Go.
func flipToServerMode(beadsDir string) error {
	mdPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(mdPath)
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	dbName, _ := m["dolt_database"].(string)
	if dbName == "" {
		return errors.New("metadata.json missing dolt_database")
	}
	m["dolt_mode"] = "server"
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(mdPath, out, 0o644); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(beadsDir, "dolt"), 0o755); err != nil {
		return err
	}
	src := filepath.Join(beadsDir, "embeddeddolt", dbName)
	dst := filepath.Join(beadsDir, "dolt", dbName)
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("move %s -> %s: %w", src, dst, err)
	}
	_ = os.RemoveAll(filepath.Join(beadsDir, "embeddeddolt"))
	return nil
}

func startDoltServer(beadsDir string) error {
	cmd := exec.Command("bd", "dolt", "start")
	cmd.Env = envWith(os.Environ(), map[string]string{"BEADS_DIR": beadsDir})
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd dolt start: %v\n%s", err, out)
	}
	return nil
}

func stopDoltServer(beadsDir string) {
	cmd := exec.Command("bd", "dolt", "stop")
	cmd.Env = envWith(os.Environ(), map[string]string{"BEADS_DIR": beadsDir})
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "testutil: bd dolt stop in %s failed: %v\n%s", beadsDir, err, out)
	}
}

func warmServer(beadsDir string) error {
	cmd := exec.Command("bd", "list", "-s", "all")
	cmd.Env = envWith(os.Environ(), map[string]string{"BEADS_DIR": beadsDir})
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bd list: %v\n%s", err, out)
	}
	return nil
}

func stopOnce(fn func()) func() {
	var once sync.Once
	return func() { once.Do(fn) }
}

func installSignalHandler(stop func()) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-ch
		stop()
		// Re-raise to get the standard exit code (matches what Go would do
		// without the handler installed).
		signal.Reset(sig.(syscall.Signal))
		_ = syscall.Kill(syscall.Getpid(), sig.(syscall.Signal))
	}()
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "testutil RunTestMain: "+format+"\n", args...)
	os.Exit(1)
}
