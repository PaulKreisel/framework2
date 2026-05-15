package testutil

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// poolEntry is one slot in the per-test database pool: a bd-init'd database
// on the package's dolt sql-server, together with the pool-managed .beads/
// directory holding its metadata.json. Wiped between tests via [resetEntry].
type poolEntry struct {
	dbName   string
	beadsDir string
}

var (
	// pool is the channel of free entries. Filled by initPool, drained and
	// refilled by [TestDB] / its cleanup. Cap == pool size.
	pool       chan poolEntry
	poolPkg    string // package's .beads/ dir (for dolt sql dropping)
	poolInitOK atomic.Bool

	// Captured from [RunTestMain] so [TestDB]'s lazy initialisation can
	// reach the running server.
	poolHost, poolPort string
	poolOnce           sync.Once
	poolLazyErr        error
)

// PoolSize is the per-package pool size used when the first [TestDB] call
// triggers lazy initialisation. Small (4) on purpose: under `go test ./...`
// several packages run concurrently, each with its own dolt server, and
// sizing the pool to GOMAXPROCS per package would multiply CPU/server load
// for little gain. Tests block briefly on the channel between the 4th and
// 5th concurrent test instead — reset is ~250ms, so the wait is cheap.
var PoolSize = 6

// initPool pre-creates `size` bd databases on the package's dolt server and
// stocks the channel. Called once from [RunTestMain] after server start.
// Failure to initialise any entry fails the whole test binary.
func initPool(pkgBeadsDir, host, port string, size int) error {
	poolPkg = pkgBeadsDir
	pool = make(chan poolEntry, size)

	// Each pool entry needs a stable .beads/ dir that survives across tests
	// in the run (the in-process server is per-package). Root it under
	// pkgBeadsDir's parent so it auto-cleans on `go clean -testcache`-style
	// runs without colliding with another package's binary.
	root := filepath.Join(filepath.Dir(pkgBeadsDir), "testdb-pool")
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("clean pool root: %w", err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create pool root: %w", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, size)
	entries := make([]poolEntry, size)
	for i := range size {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			entry, err := initPoolEntry(root, host, port, i)
			if err != nil {
				errCh <- err
				return
			}
			entries[i] = entry
		}(i)
	}
	wg.Wait()
	close(errCh)
	if err, ok := <-errCh; ok {
		return err
	}
	for _, e := range entries {
		pool <- e
	}
	poolInitOK.Store(true)
	return nil
}

func initPoolEntry(root, host, port string, i int) (poolEntry, error) {
	dbName := fmt.Sprintf("testdb_pool_%d_%s", i, randSuffix())
	entryBeadsDir := filepath.Join(root, fmt.Sprintf("entry-%d", i), ".beads")
	cmd := exec.Command("bd", "init",
		"--backend", "dolt",
		"--server", "--external",
		"--server-host", host,
		"--server-port", port,
		"--database", dbName,
		"--prefix", "tst",
		"--skip-agents", "--skip-hooks",
		"-q",
	)
	cmd.Env = envWith(os.Environ(), map[string]string{
		"BEADS_DIR":          entryBeadsDir,
		"BD_NON_INTERACTIVE": "1",
	})
	if out, err := cmd.CombinedOutput(); err != nil {
		return poolEntry{}, fmt.Errorf("bd init pool entry %s: %v\n%s", dbName, err, out)
	}
	return poolEntry{dbName: dbName, beadsDir: entryBeadsDir}, nil
}

// resetEntry returns a pool entry to a clean state: drops all rows from the
// data tables on the server (metadata/schema_migrations are preserved so
// bd recognises the database as valid) and wipes derivative files inside
// the entry's .beads/ that bd would otherwise reuse across tests (notably
// issues.jsonl, which bd auto-imports when the DB looks empty).
func resetEntry(e poolEntry) error {
	stmts := []string{
		"USE `" + e.dbName + "`",
		// Data tables only. We preserve bd's configuration state
		// (metadata, schema_migrations, config, custom_statuses,
		// custom_types) so the database still satisfies bd's
		// "initialised database" check after the reset; tests don't
		// touch those rows.
		//
		// Order children before parents where FKs are involved.
		"DELETE FROM dependencies",
		"DELETE FROM labels",
		"DELETE FROM comments",
		"DELETE FROM events",
		"DELETE FROM interactions",
		"DELETE FROM issue_snapshots",
		"DELETE FROM compaction_snapshots",
		"DELETE FROM wisp_labels",
		"DELETE FROM wisp_comments",
		"DELETE FROM wisp_dependencies",
		"DELETE FROM wisp_events",
		"DELETE FROM wisps",
		"DELETE FROM issues",
		"DELETE FROM child_counters",
	}
	cmd := exec.Command("dolt", "sql", "-q", strings.Join(stmts, "; ")+";")
	cmd.Dir = filepath.Join(poolPkg, "dolt")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("reset %s: %v\n%s", e.dbName, err, out)
	}
	// Clear the auto-export JSONL and any per-test envs/ dir. Without this,
	// bd auto-imports the previous test's issues from JSONL on the next
	// write, defeating the row-isolation we just established.
	_ = os.Remove(filepath.Join(e.beadsDir, "issues.jsonl"))
	_ = os.Remove(filepath.Join(e.beadsDir, "export-state.json"))
	_ = os.Remove(filepath.Join(e.beadsDir, "last-touched"))
	_ = os.Remove(filepath.Join(e.beadsDir, "interactions.jsonl"))
	_ = os.RemoveAll(filepath.Join(e.beadsDir, "envs"))
	return nil
}

// dropPoolEntries drops every pool database from the package's server.
// Called at TestMain shutdown so the server doesn't accumulate orphaned
// databases between `go test` runs.
func dropPoolEntries() {
	if !poolInitOK.Load() {
		return
	}
	close(pool)
	var drops []string
	for e := range pool {
		drops = append(drops, "DROP DATABASE IF EXISTS `"+e.dbName+"`")
	}
	if len(drops) == 0 {
		return
	}
	cmd := exec.Command("dolt", "sql", "-q", strings.Join(drops, "; ")+";")
	cmd.Dir = filepath.Join(poolPkg, "dolt")
	_ = cmd.Run()
}

// TestDB provisions a fresh per-test bd database on the package's running
// dolt sql-server and returns the path to a .beads/ that bd subprocesses
// can target via BEADS_DIR.
//
// Each test calling TestDB gets its own database (acquired from the pool
// stocked by [RunTestMain]) so tests can run in parallel without colliding
// on rows or hitting the dolt 1.86.3 serialization-failure (Error 1213)
// pattern observed on shared writes.
//
// Caller pattern:
//
//	beadsDir := testutil.TestDB(t)
//	t.Parallel()
//
// Pass the returned beadsDir to every bd subprocess call (via [BdCmd]) or
// in-process ticketcli invocation (via [RunTicketIn] / [RunThisFromIn]).
// Tests must NOT call t.Setenv("BEADS_DIR", ...) — Setenv is incompatible
// with t.Parallel; threading the value explicitly is what makes parallel
// per-test databases possible.
func TestDB(t *testing.T) string {
	t.Helper()
	poolOnce.Do(func() {
		pkgBeadsDir := os.Getenv("BEADS_DIR")
		if pkgBeadsDir == "" {
			poolLazyErr = fmt.Errorf("BEADS_DIR is unset — does the test package's TestMain call testutil.RunTestMain?")
			return
		}
		if poolHost == "" || poolPort == "" {
			h, p, err := readServerAddr(pkgBeadsDir)
			if err != nil {
				poolLazyErr = err
				return
			}
			poolHost, poolPort = h, p
		}
		poolLazyErr = initPool(pkgBeadsDir, poolHost, poolPort, PoolSize)
	})
	if poolLazyErr != nil {
		t.Fatalf("testutil.TestDB: %v", poolLazyErr)
	}
	entry := <-pool
	if err := resetEntry(entry); err != nil {
		// Best-effort: return the entry to the pool even though it's dirty
		// so we don't deadlock other tests; t.Fatal will tear this test
		// down anyway.
		pool <- entry
		t.Fatalf("testutil.TestDB: reset pool entry: %v", err)
	}
	t.Cleanup(func() { pool <- entry })
	return entry.beadsDir
}

func randSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// readServerAddr reads the dolt server host/port for the package's bd db.
// host falls back to 127.0.0.1 (matching bd's default and what RunTestMain
// passes to `bd dolt start`).
func readServerAddr(beadsDir string) (host, port string, err error) {
	portBytes, err := os.ReadFile(filepath.Join(beadsDir, "dolt-server.port"))
	if err != nil {
		return "", "", fmt.Errorf("read port file: %w", err)
	}
	return "127.0.0.1", strings.TrimSpace(string(portBytes)), nil
}

// cleanStaleTestDBs drops leftover testdb_* databases from the package's
// running dolt server. Called once from RunTestMain to clear any leaks from
// previous runs that were interrupted before per-test cleanup ran.
func cleanStaleTestDBs(pkgBeadsDir string) {
	cmd := exec.Command("dolt", "sql", "-r", "csv", "-q",
		`SELECT schema_name FROM information_schema.schemata WHERE schema_name LIKE 'testdb_%';`)
	cmd.Dir = filepath.Join(pkgBeadsDir, "dolt")
	out, err := cmd.Output()
	if err != nil {
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var drops []string
	for i, line := range lines {
		if i == 0 {
			continue // header
		}
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		drops = append(drops, "DROP DATABASE IF EXISTS `"+name+"`;")
	}
	if len(drops) == 0 {
		return
	}
	drop := exec.Command("dolt", "sql", "-q", strings.Join(drops, " "))
	drop.Dir = filepath.Join(pkgBeadsDir, "dolt")
	_ = drop.Run()
}

// BdCmd builds an *exec.Cmd that invokes `bd` with the given args and the
// BEADS_DIR env override pointed at beadsDir. Test helpers should use this
// instead of `exec.Command("bd", ...)` so per-test databases work under
// t.Parallel.
func BdCmd(beadsDir string, args ...string) *exec.Cmd {
	cmd := exec.Command("bd", args...)
	if beadsDir != "" {
		cmd.Env = envWith(os.Environ(), map[string]string{"BEADS_DIR": beadsDir})
	}
	return cmd
}

// CreateBead creates a top-level bead with the given title and registers a
// `bd close` cleanup. Returns the new bead id. Uses beadsDir for routing.
func CreateBead(t *testing.T, beadsDir, title string) string {
	t.Helper()
	return CreateBeadFull(t, beadsDir, title, "", "")
}

// CreateChildBead creates a bead with --parent parent and registers cleanup.
func CreateChildBead(t *testing.T, beadsDir, title, parent string) string {
	t.Helper()
	return CreateBeadFull(t, beadsDir, title, "", parent)
}

// CreateBeadFull is the underlying helper: optional description (-d) and
// parent. Registers `bd close` cleanup. Returns the new bead id.
func CreateBeadFull(t *testing.T, beadsDir, title, description, parent string) string {
	t.Helper()
	args := []string{"create", title, "--silent"}
	if description != "" {
		args = append(args, "-d", description)
	}
	if parent != "" {
		args = append(args, "--parent", parent)
	}
	cmd := BdCmd(beadsDir, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bd create %q: %v\nstderr: %s", title, err, stderr.String())
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		t.Fatalf("bd create %q: empty id", title)
	}
	t.Cleanup(func() { _ = BdCmd(beadsDir, "close", id).Run() })
	return id
}

