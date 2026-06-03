package finish

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"framework2/internal/project"
)

// Two per-ticket advisory locks live under <beadsDir>/.locks (the same
// directory dir.withWorktreeLock uses):
//
//   - finish-<id>.lock      held only for the duration of one finish run,
//                           taken NON-BLOCKING so a second concurrent
//                           `this finish` — manual or from the background
//                           poller — bails immediately instead of overlapping
//                           the destructive rebase/close/cleanup work. See
//                           [withFinishLock].
//   - finish-poll-<id>.lock held for a background poller's ENTIRE lifetime so
//                           at most one poller exists per ticket. See
//                           [acquirePollInstanceLock].
//
// flock semantics: per open file description, released automatically on
// process exit, so a crashed holder never leaves a stale lock behind.

func locksDir(beadsDir string) string { return project.Path(beadsDir, ".locks") }

// lockSlug keeps a ticket id safe as a filename component. Bead ids
// (`ai-vrc.86.5`) are already filename-safe; this only guards against a stray
// path separator.
func lockSlug(id string) string {
	return strings.NewReplacer("/", "_", string(filepath.Separator), "_").Replace(id)
}

func openLockFile(beadsDir, name string) (*os.File, error) {
	dir := locksDir(beadsDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file %s: %w", name, err)
	}
	return f, nil
}

// withFinishLock runs fn while holding a NON-BLOCKING exclusive flock on
// finish-<ticketID>.lock. If another finish already holds it, fn does NOT run
// and ran=false is returned — the caller treats that as a no-op skip. An empty
// beadsDir disables locking (fn always runs) so ad-hoc callers keep working.
func withFinishLock(beadsDir, ticketID string, fn func() error) (ran bool, err error) {
	if beadsDir == "" {
		return true, fn()
	}
	f, err := openLockFile(beadsDir, "finish-"+lockSlug(ticketID)+".lock")
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return false, nil
		}
		return false, fmt.Errorf("acquire finish lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return true, fn()
}

// pollInstanceLock is an flock held for a poller's whole lifetime (kept open in
// the holding process); release() drops it.
type pollInstanceLock struct{ f *os.File }

// acquirePollInstanceLock takes a NON-BLOCKING exclusive flock on
// finish-poll-<id>.lock. ok=false (with err=nil) means another poller already
// holds it. The caller must release() the returned lock when it acquires one.
func acquirePollInstanceLock(beadsDir, ticketID string) (lock *pollInstanceLock, ok bool, err error) {
	f, err := openLockFile(beadsDir, "finish-poll-"+lockSlug(ticketID)+".lock")
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("acquire poll lock: %w", err)
	}
	return &pollInstanceLock{f: f}, true, nil
}

func (p *pollInstanceLock) release() {
	if p == nil || p.f == nil {
		return
	}
	_ = syscall.Flock(int(p.f.Fd()), syscall.LOCK_UN)
	_ = p.f.Close()
}

// pollerRunning reports whether a poller currently holds the instance lock for
// ticketID. It probes by trying to acquire the lock and immediately releasing
// it on success. On error it returns false (can't tell — let a spawn attempt
// proceed; the spawned poller self-deduplicates via the same lock).
func pollerRunning(beadsDir, ticketID string) bool {
	lock, ok, err := acquirePollInstanceLock(beadsDir, ticketID)
	if err != nil {
		return false
	}
	if !ok {
		return true
	}
	lock.release()
	return false
}
