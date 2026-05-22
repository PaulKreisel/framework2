package dir

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"framework2/internal/project"
)

// withWorktreeLock serializes Worktree operations against the same beadsDir.
//
// dir.Worktree does a read-modify-write on the ticket's metadata.dirs array by
// shelling out twice (bd show, then bd update --metadata). Parallel
// invocations against the same ticket — or against tickets that cascade to a
// common ancestor — would otherwise interleave, silently dropping entries
// (last writer wins). Concurrent `git worktree add` on the same bare repo is
// also flaky.
//
// The lock is a POSIX advisory exclusive lock on <beadsDir>/.locks/worktree.lock.
// flock semantics: per-process, released automatically on process exit, so a
// crashed caller cannot leave a stale lock behind.
func withWorktreeLock(beadsDir string, fn func() error) error {
	if beadsDir == "" {
		return fn()
	}
	lockDir := project.Path(beadsDir, ".locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return fmt.Errorf("create lock dir: %w", err)
	}
	lockPath := filepath.Join(lockDir, "worktree.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquire worktree lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
