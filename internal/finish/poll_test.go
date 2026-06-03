package finish_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"framework2/internal/finish"
	"framework2/internal/testutil"
)

// TestFinishSkipsWhenAnotherHoldsLock is the ticket's concurrency requirement:
// while one finish holds the per-ticket lock, a second invocation exits
// immediately (a no-op) instead of overlapping the destructive work. We model
// the "first finish still running" by holding the lock file ourselves, then
// assert a `this finish` does nothing — no step beads, ticket still open, env
// intact — and reports the skip. Releasing the lock and re-running then
// completes normally, mirroring "leaving the first to complete normally".
func TestFinishSkipsWhenAnotherHoldsLock(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "lock-skip-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	mustMakeEnv(t, beadsDir, child)
	envPath := filepath.Join(beadsDir, "envs", child)

	// A check that always passes: if the skip ever leaked into a real run, the
	// step would close and the ticket would finish — which the assertions below
	// would catch.
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: only\n"+
		"      check: \"true\"\n")

	// Hold the per-ticket finish lock on our own fd, exactly as a still-running
	// finish would. A separate open file description conflicts even within this
	// process, so finish.Run's non-blocking acquire must fail and skip.
	locksDir := filepath.Join(beadsDir, ".locks")
	if err := os.MkdirAll(locksDir, 0o755); err != nil {
		t.Fatalf("mkdir .locks: %v", err)
	}
	lockPath := filepath.Join(locksDir, "finish-"+child+".lock")
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open lock file: %v", err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("flock: %v", err)
	}

	stdout, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child)
	if code != 0 {
		t.Fatalf("skipped finish should exit 0, got %d (stderr=%q)", code, stderr)
	}
	if !strings.Contains(stdout, "already in progress") {
		t.Errorf("expected skip notice in stdout, got %q", stdout)
	}
	// Nothing happened: no step beads, ticket still open, env intact.
	if steps := childrenWithLabel(t, beadsDir, child, finish.StepLabel); len(steps) != 0 {
		t.Errorf("skipped finish created step beads: %v", steps)
	}
	assertStatus(t, beadsDir, child, "open")
	if _, err := os.Stat(envPath); err != nil {
		t.Errorf("env folder removed by a skipped finish: %v", err)
	}

	// Release the lock; the next finish now runs to completion.
	_ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	_ = lf.Close()

	if _, stderr, code := testutil.RunTicketIn(t, beadsDir, "finish", child); code != 0 {
		t.Fatalf("finish after lock release exit=%d stderr=%q", code, stderr)
	}
	assertStatus(t, beadsDir, child, "closed")
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Errorf("env folder %s still present after successful finish (err=%v)", envPath, err)
	}
}

// TestPollLoopFinishesWithoutManualReruns covers the polling payoff: a step
// whose check passes only on its SECOND run is driven to completion by the
// background poll loop alone, with no manual re-invocation. RunPoll re-runs the
// finish flow each tick (sleep skipped via TestSkipSleep) until the env folder
// disappears, then exits.
func TestPollLoopFinishesWithoutManualReruns(t *testing.T) {
	finish.TestSkipSleep = true
	t.Cleanup(func() { finish.TestSkipSleep = false })

	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "poll-loop-child")
	bare := bareRepo(t, "main")

	mustRunTicket(t, beadsDir, "dir", "worktree", child, bare)
	mustMakeEnv(t, beadsDir, child)
	childWT := worktreePath(bare, child)
	envPath := filepath.Join(beadsDir, "envs", child)

	// Check fails the first time (creating its marker) and passes the second —
	// so a single finish leaves the step in-progress and only the poller's next
	// tick closes it. Marker lives outside the worktree so the tree stays clean.
	marker := filepath.Join(t.TempDir(), "m")
	writeStepsYAML(t, bare, ""+
		"- condition: \"true\"\n"+
		"  steps:\n"+
		"    - title: eventually\n"+
		"      check: "+failFirst(marker)+"\n")

	var buf bytes.Buffer
	if err := finish.RunPoll("", beadsDir, child, 1, &buf); err != nil {
		t.Fatalf("RunPoll: %v\n%s", err, buf.String())
	}

	// The poller closed the step and ran full cleanup with zero manual reruns.
	assertStatus(t, beadsDir, child, "closed")
	if _, err := os.Stat(childWT); !os.IsNotExist(err) {
		t.Errorf("worktree %s still present after poller finished (err=%v)", childWT, err)
	}
	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Errorf("env folder %s still present after poller finished (err=%v)", envPath, err)
	}
	if !strings.Contains(buf.String(), "poller exiting") {
		t.Errorf("expected poller to log its exit, got:\n%s", buf.String())
	}
}

// TestPollLoopSecondPollerExitsImmediately covers the one-poller-per-ticket
// guard: a second RunPoll, while the instance lock is held, exits at once
// instead of double-polling.
func TestPollLoopSecondPollerExitsImmediately(t *testing.T) {
	beadsDir := testutil.TestDB(t)
	child := testutil.CreateBead(t, beadsDir, "poll-dup-child")

	// Hold the poll-instance lock as a live poller would.
	locksDir := filepath.Join(beadsDir, ".locks")
	if err := os.MkdirAll(locksDir, 0o755); err != nil {
		t.Fatalf("mkdir .locks: %v", err)
	}
	lockPath := filepath.Join(locksDir, "finish-poll-"+child+".lock")
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open poll lock: %v", err)
	}
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatalf("flock: %v", err)
	}
	t.Cleanup(func() { _ = syscall.Flock(int(lf.Fd()), syscall.LOCK_UN); _ = lf.Close() })

	var buf bytes.Buffer
	if err := finish.RunPoll("", beadsDir, child, 1, &buf); err != nil {
		t.Fatalf("RunPoll (duplicate): %v", err)
	}
	if !strings.Contains(buf.String(), "already running") {
		t.Errorf("expected duplicate poller to report and exit, got:\n%s", buf.String())
	}
}
