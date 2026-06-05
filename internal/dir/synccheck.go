package dir

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// syncState classifies one dir's outcome in [SyncCheck].
type syncState int

const (
	syncOK   syncState = iota // exists, has an upstream, and is in sync
	syncSkip                  // nothing to verify (gone, detached, or no upstream)
	syncFail                  // exists with an upstream but diverged (or unverifiable)
)

// SyncCheck verifies that every linked dir on ticketID that *can* be checked is
// in sync with its branch's configured upstream after fetching. For each dir it:
//
//   - skips dirs whose path is gone, that are not on a branch, or whose branch
//     has no configured upstream — nothing to verify, so they pass,
//   - otherwise runs `git fetch` and requires 0 commits ahead AND 0 behind the
//     upstream.
//
// In other words: if a dir and its remote upstream both exist, they must match;
// everything else passes. It is read-only — fetch only, no push/pull/merge —
// and is meant to back a finish-step `check` (sibling to `pr check`). On any
// diverged (or unverifiable) dir it returns a non-nil error listing every
// offender, which the CLI maps to a non-zero exit. A ticket with zero linked
// dirs passes vacuously.
func SyncCheck(db, beadsDir, ticketID string, out io.Writer) error {
	entries, err := load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}

	var offenders []string
	for _, e := range entries {
		branch, detail, state := checkEntrySync(e)
		fmt.Fprintf(out, "%s (%s): %s\n", e.Path, branch, detail)
		if state == syncFail {
			offenders = append(offenders, fmt.Sprintf("  %s (%s): %s", e.Path, branch, detail))
		}
	}
	if len(offenders) > 0 {
		return fmt.Errorf("dirs not in sync with upstream:\n%s", strings.Join(offenders, "\n"))
	}
	return nil
}

// checkEntrySync fetches and inspects one entry. It returns the branch name (or
// "?" when it can't be read), a human-readable detail string, and the outcome
// state. A missing path, detached HEAD, or absent upstream is reported as
// syncSkip (it passes); only an existing dir whose branch diverges from — or
// can't be compared against — its upstream is syncFail.
func checkEntrySync(e Entry) (branch, detail string, state syncState) {
	if _, statErr := os.Stat(e.Path); statErr != nil {
		return "?", "path is gone, skipped", syncSkip
	}
	branch, hasBranch := gitBranch(e.Path)
	if !hasBranch || branch == "HEAD" {
		return "?", "not a repo or detached HEAD, skipped", syncSkip
	}
	if _, hasUpstream := runGit(e.Path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); !hasUpstream {
		return branch, "no upstream, skipped", syncSkip
	}
	if out, err := exec.Command("git", "-C", e.Path, "fetch").CombinedOutput(); err != nil {
		return branch, fmt.Sprintf("git fetch failed: %s", strings.TrimSpace(string(out))), syncFail
	}
	ahead, behind, err := aheadBehind(e.Path)
	if err != nil {
		return branch, err.Error(), syncFail
	}
	if ahead != 0 || behind != 0 {
		return branch, fmt.Sprintf("ahead %d, behind %d", ahead, behind), syncFail
	}
	return branch, "in sync", syncOK
}

// aheadBehind returns how far HEAD is ahead of and behind its upstream, using
// `git rev-list --left-right --count @{upstream}...HEAD` (left=upstream=behind,
// right=HEAD=ahead).
func aheadBehind(path string) (ahead, behind int, err error) {
	out, err := exec.Command("git", "-C", path, "rev-list", "--left-right", "--count", "@{upstream}...HEAD").Output()
	if err != nil {
		return 0, 0, fmt.Errorf("git rev-list @{upstream}...HEAD: %w", withStderr(err))
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("unexpected rev-list output %q", string(out))
	}
	behind, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("parse behind count %q: %w", parts[0], err)
	}
	ahead, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("parse ahead count %q: %w", parts[1], err)
	}
	return ahead, behind, nil
}
