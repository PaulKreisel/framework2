package finish

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"framework2/internal/project"
)

// Background polling of `this finish` (ai-vrc.86.5).
//
// The step-driven finish path (steps.go) already does the polling-shaped work:
// every `this finish` re-runs each in-progress step's `check` and closes the
// ones that now pass, exiting non-zero with a pending summary while any remain
// open. Historically the operator had to keep re-running it by hand after each
// external state change (a Jenkins build going green, a PR being merged).
//
// When a step-driven finish leaves at least one step open, it now spawns a
// detached background poller — a `ticket finish-poll` process, Setsid'd off the
// session like the pipeline/terminate async runners — that re-invokes
// `this finish` on an interval until the ticket finishes. The poller exits as
// soon as the env folder disappears (cleanup ran) or after a safety ceiling.
//
// WHEN POLLING HELPS vs. WHEN IT DOESN'T
//
//	Helps:  steps whose `check` flips on EXTERNAL state the agent isn't driving
//	        — CI/Jenkins finishing, a reviewer merging a PR, a deploy settling.
//	        These would otherwise sit idle waiting for a human to re-run finish.
//	Doesn't: steps that need local work to progress (a `complete` that keeps
//	        failing, code that must be written). Re-running the same check on a
//	        timer won't change the outcome; the poller just burns ticks until
//	        the safety ceiling. Such steps are better driven by the agent.
//
// Concurrency safety is the `flock` requirement of the ticket: each finish run
// (manual or polled) takes finish-<id>.lock NON-BLOCKING (see withFinishLock),
// so an interval firing while a previous finish is still executing is a clean
// no-op rather than an overlapping, half-destructive second run.

// defaultPollIntervalSeconds is the gap between polled `this finish` runs when
// THIS_FINISH_POLL_INTERVAL is unset. Five minutes is gentle on the external
// services the checks poll (Jenkins, GitHub) while still reacting promptly
// relative to how long those states take to change.
const defaultPollIntervalSeconds = 300

// pollEnv overrides the interval (in whole seconds). Invalid or non-positive
// values fall back to the default.
const pollEnv = "THIS_FINISH_POLL_INTERVAL"

// pollMaxDuration bounds a poller's lifetime as a safety net against an
// immortal background process — e.g. a check that never passes. When the
// ceiling is reached the poller exits; the user can re-run `this finish` to
// start a fresh one. Var (not const) so tests can shrink it.
var pollMaxDuration = 24 * time.Hour

// pollIntervalSeconds resolves the configured poll interval.
func pollIntervalSeconds() int {
	if v := strings.TrimSpace(os.Getenv(pollEnv)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultPollIntervalSeconds
}

// ensurePollerRunning spawns a background poller for ticketID unless one is
// already running. Best-effort and production-only: it no-ops under `go test`
// (so unit tests of the step engine never fork processes) and downgrades any
// spawn failure to a warning — a missing poller never fails the finish, it just
// means the operator falls back to re-running `this finish` by hand.
func ensurePollerRunning(db, beadsDir, ticketID string, stdout io.Writer) {
	if testing.Testing() {
		return
	}
	if beadsDir == "" {
		return
	}
	if pollerRunning(beadsDir, ticketID) {
		return
	}
	if err := spawnPoller(db, beadsDir, ticketID, stdout); err != nil {
		fmt.Fprintf(stdout, "warn: could not start finish poller for %s: %v\n", ticketID, err)
	}
}

// spawnPoller launches a detached `ticket finish-poll --ticket <id> --interval
// <n>` process. It is placed in a new session (Setsid) so a closing controlling
// terminal won't SIGHUP it, anchored in beadsDir (which outlives the worktrees
// finish removes), and its output is appended to <env>/finish-poll.log for
// debuggability (best-effort — discarded if the file can't be opened).
func spawnPoller(db, beadsDir, ticketID string, stdout io.Writer) error {
	ticketBin, err := findTicketBin()
	if err != nil {
		return fmt.Errorf("locate ticket binary: %w", err)
	}
	interval := pollIntervalSeconds()
	args := []string{}
	if db != "" {
		args = append(args, "--db", db)
	}
	args = append(args, "finish-poll", "--ticket", ticketID, "--interval", strconv.Itoa(interval))
	cmd := exec.Command(ticketBin, args...)
	cmd.Env = append(os.Environ(), "BEADS_DIR="+beadsDir)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	logPath := filepath.Join(project.Path(beadsDir, filepath.Join("envs", ticketID)), "finish-poll.log")
	if logf, lerr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); lerr == nil {
		cmd.Stdout = logf
		cmd.Stderr = logf
		defer logf.Close()
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Dir = beadsDir
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn finish poller: %w", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release finish poller: %w", err)
	}
	fmt.Fprintf(stdout, "finish: started background poller (pid=%d, interval=%ds) — `this finish` re-runs automatically while steps are pending\n", pid, interval)
	return nil
}

// RunPoll is the entrypoint of the hidden `ticket finish-poll` subcommand. It
// holds the per-ticket poll-instance lock for its whole life (so a duplicate
// poller exits immediately), then on each interval re-invokes the full finish
// flow until the ticket finishes (env folder gone), the env folder vanishes
// out from under it, or the safety ceiling is hit.
//
// A polled finish that leaves steps pending returns a non-nil error; that is
// the normal "still waiting" signal, logged and ignored. Infra errors are
// likewise logged without killing the poller — a later tick may succeed.
func RunPoll(db, beadsDir, ticketID string, intervalSeconds int, stdout io.Writer) error {
	if ticketID == "" {
		return errors.New("finish-poll: --ticket required")
	}
	if beadsDir == "" {
		return errors.New("finish-poll: BEADS_DIR not set")
	}
	if intervalSeconds <= 0 {
		intervalSeconds = defaultPollIntervalSeconds
	}

	lock, ok, err := acquirePollInstanceLock(beadsDir, ticketID)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintf(stdout, "finish-poll: another poller for %s is already running; exiting\n", ticketID)
		return nil
	}
	defer lock.release()

	envPath := project.Path(beadsDir, filepath.Join("envs", ticketID))
	interval := time.Duration(intervalSeconds) * time.Second
	maxIters := max(int(pollMaxDuration/interval), 1)

	for i := range maxIters {
		// Sleep first: the finish that spawned us just ran a tick, so there is
		// nothing to gain from re-running immediately.
		if !TestSkipSleep {
			time.Sleep(interval)
		}
		if finished(envPath) {
			fmt.Fprintf(stdout, "finish-poll: %s finished; poller exiting\n", ticketID)
			return nil
		}
		fmt.Fprintf(stdout, "finish-poll: tick %d — running this finish for %s\n", i+1, ticketID)
		if err := Run(db, beadsDir, ticketID, false, stdout); err != nil {
			// Expected while steps remain (pending summary); also covers infra
			// hiccups. Keep polling — the next tick may make progress.
			fmt.Fprintf(stdout, "finish-poll: %v\n", err)
		}
		// Run returning nil is either completion (env folder now gone) or a
		// lock-contention no-op; the env-folder probe distinguishes them and
		// lets us exit promptly on completion rather than after another sleep.
		if finished(envPath) {
			fmt.Fprintf(stdout, "finish-poll: %s finished; poller exiting\n", ticketID)
			return nil
		}
	}
	fmt.Fprintf(stdout, "finish-poll: reached max lifetime for %s; exiting (re-run `this finish` to resume polling)\n", ticketID)
	return nil
}

// finished reports whether the ticket's env folder is gone — the signal that
// the finish flow's cleanup ran (or the ticket was finished another way).
func finished(envPath string) bool {
	_, err := os.Stat(envPath)
	return errors.Is(err, os.ErrNotExist)
}
