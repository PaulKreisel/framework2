package pr

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"

	"framework2/internal/bdcli"
	"framework2/internal/create"
	"framework2/internal/dir"
	"framework2/internal/finish"
)

// List prints the bead's PR records as a table. State is fetched live from
// GitHub for each PR.
func List(db, beadsDir, ticketID string, stdout io.Writer) error {
	recs, err := Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	entries, err := dir.Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	dirByRepo, err := mapDirsByRepo(entries)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "DIR\tREPO\tPR\tBRANCH\tSTATE\tURL")
	for _, r := range recs {
		dirPath := dirByRepo[r.Repo]
		if dirPath == "" {
			dirPath = "—"
		}
		state := liveState(r)
		fmt.Fprintf(tw, "%s\t%s\t#%d\t%s\t%s\t%s\n",
			dirPath, r.Repo, r.Number, r.Branch, state, r.URL())
	}
	return tw.Flush()
}

// liveState fetches the current PR state via gh. Network/auth errors surface
// as "<error>" so the table row still renders.
func liveState(r Record) string {
	v, ok, err := prView(r.Repo, r.Number)
	if err != nil {
		return "<error>"
	}
	if !ok {
		return "deleted"
	}
	return stateLabel(v)
}

// mapDirsByRepo returns a lookup of "owner/name" -> first matching dir path.
// Dirs whose remote isn't a GitHub repo are skipped (gh returns an error, not
// a recognisable repo). When multiple dirs match the same repo, the first one
// wins — `pr list` only needs a representative.
func mapDirsByRepo(entries []dir.Entry) (map[string]string, error) {
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.Path == "" {
			continue
		}
		if _, err := os.Stat(e.Path); err != nil {
			// Vanished dir — skip rather than fail. `pr list` should not
			// break just because one ancestor worktree was removed.
			continue
		}
		name, err := nameWithOwner(e.Path)
		if err != nil {
			continue
		}
		if _, ok := out[name]; !ok {
			out[name] = e.Path
		}
	}
	return out, nil
}

// Create creates a new GitHub PR for the dir at selector and records it on
// the bead. ready=true overrides the draft default.
func Create(db, beadsDir, ticketID, selector string, ready bool, stdout io.Writer) error {
	entries, err := dir.Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("ticket %s has no linked dirs", ticketID)
	}
	entry, err := dir.PickEntry(ticketID, entries, selector)
	if err != nil {
		return err
	}
	if _, err := os.Stat(entry.Path); err != nil {
		return fmt.Errorf("linked dir is gone: %s: %w", entry.Path, err)
	}
	existing, err := Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	repo, branch, nums, err := entryOpenPRs(*entry)
	if err != nil {
		return err
	}
	return createForEntry(db, beadsDir, ticketID, *entry, repo, branch, nums, existing, ready, stdout)
}

// CreateAll runs `pr create` for every "eligible" linked dir on ticketID. A
// dir counts as eligible when it has at least one commit ahead of the base
// branch resolved via the parent chain. Dirs that already have a PR attached
// for their repo are skipped with a notice (so `pr create --all` is safe to
// re-run). Per-dir failures are reported and the function returns an error
// when any dir failed, but it still tries every eligible dir.
func CreateAll(db, beadsDir, ticketID string, ready bool, stdout io.Writer) error {
	entries, err := dir.Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("ticket %s has no linked dirs", ticketID)
	}
	eligible, err := eligibleDirs(db, beadsDir, ticketID, entries)
	if err != nil {
		return err
	}
	if len(eligible) == 0 {
		return errors.New("no linked dirs are ahead of their base branch")
	}
	var failures []string
	for _, entry := range eligible {
		// Reload existing records each iteration so successful Creates from
		// earlier dirs are reflected in the duplicate check (though each
		// dir's repo is distinct in practice, this keeps the invariants
		// consistent if a dir links multiple repos in some future shape).
		existing, err := Load(db, beadsDir, ticketID)
		if err != nil {
			return err
		}
		repo, err := nameWithOwner(entry.Path)
		if err != nil {
			fmt.Fprintf(stdout, "%s: error: %v\n", entry.Path, err)
			failures = append(failures, entry.Path)
			continue
		}
		if hasRepoRecord(existing, repo) {
			fmt.Fprintf(stdout, "%s: PR already attached for %s, skipping\n", entry.Path, repo)
			continue
		}
		branch, nums, err := branchOpenPRs(entry, repo)
		if err != nil {
			fmt.Fprintf(stdout, "%s: error: %v\n", entry.Path, err)
			failures = append(failures, entry.Path)
			continue
		}
		// More than one open PR on the branch: ambiguous, skip with a notice
		// (a bulk run has nothing to fall back to).
		if len(nums) >= 2 {
			fmt.Fprintf(stdout, "%s: multiple open PRs for %s on branch %s (%s), skipping\n",
				entry.Path, repo, branch, prNumberList(nums))
			continue
		}
		fmt.Fprintf(stdout, "%s:\n", entry.Path)
		if err := createForEntry(db, beadsDir, ticketID, entry, repo, branch, nums, existing, ready, stdout); err != nil {
			// No commits ahead of base is an expected, non-fatal state for a
			// bulk run (the dir simply isn't ready) — skip without failing.
			if errors.Is(err, errNoCommits) {
				fmt.Fprintf(stdout, "%s: no commits to create a PR for, skipping\n", entry.Path)
				continue
			}
			fmt.Fprintf(stdout, "%s: error: %v\n", entry.Path, err)
			failures = append(failures, entry.Path)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("pr create --all: %d dir(s) failed: %s",
			len(failures), strings.Join(failures, ", "))
	}
	return nil
}

// createForEntry attaches a PR for a single, already-resolved linked dir
// entry, given its repo, current branch, and the open PRs on that branch. It
// is the shared body used by Create and CreateAll. existing is the bead's
// current PR records; the same-repo duplicate check runs against it.
//
// When exactly one open PR already exists for the branch, it is linked
// instead of creating a new one. Two or more is ambiguous and hard-errors
// (callers that can skip — e.g. CreateAll — decide that before calling here).
// Zero open PRs creates a new PR as before.
func createForEntry(db, beadsDir, ticketID string, entry dir.Entry, repo, branch string, nums []int, existing []Record, ready bool, stdout io.Writer) error {
	if len(nums) >= 2 {
		return fmt.Errorf("multiple open PRs for %s on branch %s (%s); link one explicitly with `ticket pr link <pr-url>`",
			repo, branch, prNumberList(nums))
	}
	if len(nums) == 1 {
		return autoLinkPR(db, beadsDir, ticketID, repo, branch, nums[0], existing, stdout)
	}
	// Refuse a second PR for the same (bead, dir) pair. Identity is by repo
	// because storage doesn't track the originating dir directly.
	for _, r := range existing {
		if r.Repo == repo {
			return fmt.Errorf("bead %s already has a PR attached for %s (#%d)", ticketID, repo, r.Number)
		}
	}

	base, baseDir, isDefault, err := resolveBase(db, beadsDir, ticketID, entry)
	if err != nil {
		return err
	}

	// Refuse to create a PR with no commits — GitHub rejects "No commits
	// between <base> and <head>" and we'd rather surface that cleanly than
	// let the gh error leak through (and, for --all, abort the whole run).
	ahead, err := commitsAheadOfBase(entry.Path, base, isDefault)
	if err != nil {
		return err
	}
	if ahead == 0 {
		return fmt.Errorf("%w (%s..%s)", errNoCommits, base, branch)
	}

	// Push the current branch upstream (force-with-lease handles both the
	// new-branch and diverged-upstream cases; FF stays cheap because the
	// remote ref is either an ancestor or absent).
	if err := pushForceWithLease(entry.Path, branch); err != nil {
		return err
	}

	// Push the ancestor's base branch when it isn't on the remote yet. The
	// repo's default branch is assumed to be present already; we never push
	// from a worktree that wasn't created by an ancestor.
	if !isDefault {
		exists, err := branchOnRemote(baseDir, base)
		if err != nil {
			return err
		}
		if !exists {
			if err := pushForceWithLease(baseDir, base); err != nil {
				return err
			}
		}
	}

	title, err := beadTitle(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	if title == "" {
		title = ticketID
	}
	args := []string{"pr", "create",
		"--title", title,
		"--body", "",
		"--base", base,
		"--head", branch,
	}
	if ready {
		args = append(args, "--no-draft")
	} else {
		args = append(args, "--draft")
	}
	out, err := ExecGH(entry.Path, args...)
	if err != nil {
		return err
	}
	url := strings.TrimSpace(string(out))
	urlLines := strings.Split(url, "\n")
	url = strings.TrimSpace(urlLines[len(urlLines)-1])

	gotRepo, num, err := ParseURL(url)
	if err != nil {
		return fmt.Errorf("parse gh pr create output %q: %w", url, err)
	}
	// gh might canonicalise the repo (forks etc.). Prefer the URL's owner/name.
	if gotRepo != "" {
		repo = gotRepo
	}

	if err := appendRecord(db, beadsDir, ticketID, Record{
		Repo:   repo,
		Number: num,
		Branch: branch,
	}); err != nil {
		return err
	}
	fmt.Fprintln(stdout, url)
	return nil
}

// hasRepoRecord reports whether any of recs targets the given repo.
func hasRepoRecord(recs []Record, repo string) bool {
	for _, r := range recs {
		if r.Repo == repo {
			return true
		}
	}
	return false
}

// Show opens the PR URL for the dir at selector in the operator's browser.
// When the ticket has exactly one linked dir, selector may be empty.
func Show(db, beadsDir, ticketID, selector string, stdout io.Writer) error {
	entries, err := dir.Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("ticket %s has no linked dirs", ticketID)
	}
	entry, err := dir.PickEntry(ticketID, entries, selector)
	if err != nil {
		return err
	}
	if _, err := os.Stat(entry.Path); err != nil {
		return fmt.Errorf("linked dir is gone: %s: %w", entry.Path, err)
	}
	recs, err := Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	rec, ok, err := recordForEntry(*entry, recs)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("dir %s has no PR attached on ticket %s", entry.Path, ticketID)
	}
	fmt.Fprintln(stdout, rec.URL())
	return OpenURL(rec.URL())
}

// ShowAll opens the PR URLs for every eligible linked dir. Dirs that are
// eligible (≥1 commit ahead of base) but have no PR attached are skipped
// with a notice on stdout.
func ShowAll(db, beadsDir, ticketID string, stdout io.Writer) error {
	entries, err := dir.Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("ticket %s has no linked dirs", ticketID)
	}
	eligible, err := eligibleDirs(db, beadsDir, ticketID, entries)
	if err != nil {
		return err
	}
	if len(eligible) == 0 {
		return errors.New("no linked dirs are ahead of their base branch")
	}
	recs, err := Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	var failures []string
	for _, entry := range eligible {
		rec, ok, err := recordForEntry(entry, recs)
		if err != nil {
			fmt.Fprintf(stdout, "%s: error: %v\n", entry.Path, err)
			failures = append(failures, entry.Path)
			continue
		}
		if !ok {
			fmt.Fprintf(stdout, "%s: no PR attached, skipping\n", entry.Path)
			continue
		}
		fmt.Fprintln(stdout, rec.URL())
		if err := OpenURL(rec.URL()); err != nil {
			fmt.Fprintf(stdout, "%s: open failed: %v\n", entry.Path, err)
			failures = append(failures, entry.Path)
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("pr show --all: %d dir(s) failed: %s",
			len(failures), strings.Join(failures, ", "))
	}
	return nil
}

// recordForEntry returns the PR record matching entry's repo (and ok=false
// when none matches). Errors propagate only from the nameWithOwner gh call.
func recordForEntry(entry dir.Entry, recs []Record) (Record, bool, error) {
	repo, err := nameWithOwner(entry.Path)
	if err != nil {
		return Record{}, false, err
	}
	for _, r := range recs {
		if r.Repo == repo {
			return r, true, nil
		}
	}
	return Record{}, false, nil
}

// eligibleDirs returns the entries from `entries` that have at least one
// commit ahead of their resolved base branch. Entries whose path is missing
// or whose branch is detached are skipped silently — they can't have a
// non-zero commits-ahead by definition. resolveBase errors propagate (a
// missing ancestor worktree is a real configuration problem worth surfacing).
func eligibleDirs(db, beadsDir, ticketID string, entries []dir.Entry) ([]dir.Entry, error) {
	var out []dir.Entry
	for _, e := range entries {
		if _, err := os.Stat(e.Path); err != nil {
			continue
		}
		branch, err := gitCurrentBranch(e.Path)
		if err != nil || branch == "" || branch == "HEAD" {
			continue
		}
		base, _, _, err := resolveBase(db, beadsDir, ticketID, e)
		if err != nil {
			return nil, err
		}
		n, err := commitsAhead(e.Path, base)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			out = append(out, e)
		}
	}
	return out, nil
}

// errNoCommits marks the "PR base already contains every commit on HEAD" case.
// createForEntry returns it (wrapped with the base/branch) so callers can tell
// it apart: CreateAll skips the dir, single Create surfaces a clean message.
var errNoCommits = errors.New("no commits to create a PR for")

// commitsAheadOfBase counts the commits on dirPath's HEAD that the PR's base
// does not already contain — the same emptiness GitHub checks before allowing
// a PR. For the repo's default branch the authoritative base is the remote
// tip, which can be ahead of a stale local ref, so we fetch it first. For an
// ancestor (non-default) base the local branch ref is authoritative:
// createForEntry force-pushes it as-is, so the remote ends up matching it.
func commitsAheadOfBase(dirPath, base string, isDefault bool) (int, error) {
	ref := base
	if isDefault {
		if _, err := ExecGit(dirPath, "fetch", "origin", base); err != nil {
			return 0, err
		}
		ref = "FETCH_HEAD"
	}
	return commitsAhead(dirPath, ref)
}

// commitsAhead returns the number of commits in dirPath's HEAD that are not
// reachable from base.
func commitsAhead(dirPath, base string) (int, error) {
	out, err := ExecGit(dirPath, "rev-list", "--count", base+"..HEAD")
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("parse rev-list --count output %q: %w", s, err)
	}
	return n, nil
}

// OpenURL launches the operator's default browser on the given URL. Tests
// substitute this to record calls without spawning a process.
var OpenURL = defaultOpenURL

func defaultOpenURL(url string) error {
	cmd := exec.Command(openCommand(), url)
	return cmd.Run()
}

func openCommand() string {
	switch runtime.GOOS {
	case "darwin":
		return "open"
	default:
		return "xdg-open"
	}
}

// Link attaches an existing PR (identified by rawURL) to ticketID, associating
// it with the dir at selector.
func Link(db, beadsDir, ticketID, selector, rawURL string) error {
	entries, err := dir.Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("ticket %s has no linked dirs", ticketID)
	}
	entry, err := dir.PickEntry(ticketID, entries, selector)
	if err != nil {
		return err
	}
	repo, num, err := ParseURL(rawURL)
	if err != nil {
		return err
	}
	v, ok, err := prView(repo, num)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("PR not found on GitHub: %s", rawURL)
	}
	existing, err := Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	for _, r := range existing {
		if r.Repo == repo {
			return fmt.Errorf("bead %s already has a PR attached for %s (#%d)", ticketID, repo, r.Number)
		}
	}
	// Sanity-check that the entry's repo matches the PR's repo. nameWithOwner
	// requires gh; on dirs where it can't tell we just skip the check.
	if _, err := os.Stat(entry.Path); err == nil {
		if name, err := nameWithOwner(entry.Path); err == nil && name != repo {
			return fmt.Errorf("dir %s points at %s, but PR is for %s", entry.Path, name, repo)
		}
	}
	return appendRecord(db, beadsDir, ticketID, Record{
		Repo:   repo,
		Number: num,
		Branch: v.HeadRefName,
	})
}

// LinkAuto attaches the dir's unique open PR to ticketID without an explicit
// URL. The PR is the single open PR whose head is the dir's current branch.
// Zero or two-or-more open PRs is an error (a targeted command on one dir has
// nothing to skip to).
func LinkAuto(db, beadsDir, ticketID, selector string, stdout io.Writer) error {
	entries, err := dir.Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("ticket %s has no linked dirs", ticketID)
	}
	entry, err := dir.PickEntry(ticketID, entries, selector)
	if err != nil {
		return err
	}
	if _, err := os.Stat(entry.Path); err != nil {
		return fmt.Errorf("linked dir is gone: %s: %w", entry.Path, err)
	}
	existing, err := Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	repo, branch, nums, err := entryOpenPRs(*entry)
	if err != nil {
		return err
	}
	switch len(nums) {
	case 0:
		return fmt.Errorf("no open PR for %s on branch %s; pass a <pr-url> or create one with `ticket pr create`",
			repo, branch)
	case 1:
		return autoLinkPR(db, beadsDir, ticketID, repo, branch, nums[0], existing, stdout)
	default:
		return fmt.Errorf("multiple open PRs for %s on branch %s (%s); pass an explicit <pr-url>",
			repo, branch, prNumberList(nums))
	}
}

// LinkAll attaches the unique open PR for every "eligible" linked dir (≥1
// commit ahead of the resolved base, the same eligibility as CreateAll) that
// is not already PR-attached on the bead. Dirs whose branch has zero or two-
// or-more open PRs are skipped with a per-dir notice; already-attached dirs
// are likewise skipped. Per-dir failures are aggregated and returned, but
// every eligible dir is still attempted.
func LinkAll(db, beadsDir, ticketID string, stdout io.Writer) error {
	entries, err := dir.Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("ticket %s has no linked dirs", ticketID)
	}
	eligible, err := eligibleDirs(db, beadsDir, ticketID, entries)
	if err != nil {
		return err
	}
	if len(eligible) == 0 {
		return errors.New("no linked dirs are ahead of their base branch")
	}
	var failures []string
	for _, entry := range eligible {
		// Reload each iteration so earlier links are reflected in the
		// duplicate check (consistent with CreateAll).
		existing, err := Load(db, beadsDir, ticketID)
		if err != nil {
			return err
		}
		repo, err := nameWithOwner(entry.Path)
		if err != nil {
			fmt.Fprintf(stdout, "%s: error: %v\n", entry.Path, err)
			failures = append(failures, entry.Path)
			continue
		}
		if hasRepoRecord(existing, repo) {
			fmt.Fprintf(stdout, "%s: PR already attached for %s, skipping\n", entry.Path, repo)
			continue
		}
		branch, nums, err := branchOpenPRs(entry, repo)
		if err != nil {
			fmt.Fprintf(stdout, "%s: error: %v\n", entry.Path, err)
			failures = append(failures, entry.Path)
			continue
		}
		switch len(nums) {
		case 0:
			fmt.Fprintf(stdout, "%s: no open PR for %s on branch %s, skipping\n", entry.Path, repo, branch)
		case 1:
			fmt.Fprintf(stdout, "%s:\n", entry.Path)
			if err := autoLinkPR(db, beadsDir, ticketID, repo, branch, nums[0], existing, stdout); err != nil {
				fmt.Fprintf(stdout, "%s: error: %v\n", entry.Path, err)
				failures = append(failures, entry.Path)
			}
		default:
			fmt.Fprintf(stdout, "%s: multiple open PRs for %s on branch %s (%s), skipping\n",
				entry.Path, repo, branch, prNumberList(nums))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("pr link --all: %d dir(s) failed: %s",
			len(failures), strings.Join(failures, ", "))
	}
	return nil
}

// CreateBead creates a new bead from a PR URL as a child of currentID.
// currentID is the bead the operator is acting from (auto-detected by
// `this`, or passed positionally). No dir is linked on the new bead.
func CreateBead(db, beadsDir, currentID, rawURL string, stdout io.Writer) error {
	repo, num, err := ParseURL(rawURL)
	if err != nil {
		return err
	}
	v, ok, err := prView(repo, num)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("PR not found on GitHub: %s", rawURL)
	}
	title := "PR " + v.Title
	if v.Title == "" {
		title = "PR " + rawURL
	}
	var captured strings.Builder
	if err := create.Run(db, beadsDir, currentID, title, rawURL, create.Child, false, &captured); err != nil {
		return err
	}
	newID := strings.TrimSpace(captured.String())
	if newID == "" {
		return errors.New("create.Run returned empty id")
	}
	if err := appendRecord(db, beadsDir, newID, Record{
		Repo:   repo,
		Number: num,
		Branch: v.HeadRefName,
	}); err != nil {
		return err
	}
	if out, err := bdcli.Cmd(beadsDir, db, "label", "add", newID, Label).CombinedOutput(); err != nil {
		return fmt.Errorf("bd label add %s %s: %w: %s", newID, Label, err, strings.TrimSpace(string(out)))
	}
	fmt.Fprintln(stdout, newID)
	return nil
}

// Label is the literal label stamped on beads created via `pr create-bead`.
const Label = "pr"

// Refresh reconciles the bead's PR state with GitHub.
//
//   - Validates that every linked dir has a PR.
//   - Prints any closed-unmerged or deleted PRs (no auto-unlink).
//   - When every attached PR is merged AND no open children block the bead,
//     runs the full finish flow.
func Refresh(db, beadsDir, ticketID string, stdout io.Writer) error {
	entries, err := dir.Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	recs, err := Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}

	if err := validateDirsHavePRs(entries, recs); err != nil {
		return err
	}

	allMerged := len(recs) > 0
	var problems []string
	for _, r := range recs {
		v, ok, err := prView(r.Repo, r.Number)
		if err != nil {
			return err
		}
		if !ok {
			allMerged = false
			problems = append(problems, fmt.Sprintf("%s: deleted on GitHub", r.URL()))
			continue
		}
		switch strings.ToUpper(v.State) {
		case "MERGED":
			// keep going
		case "CLOSED":
			allMerged = false
			problems = append(problems, fmt.Sprintf("%s: closed without merge", r.URL()))
		default:
			allMerged = false
		}
	}

	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Fprintln(stdout, p)
		}
		return errors.New("refresh: PRs in non-merged terminal state")
	}

	if !allMerged {
		return nil
	}

	// Auto-close path: refuse if open children block the bead.
	children, err := openChildren(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	if len(children) > 0 {
		fmt.Fprintf(stdout, "ticket %s has open children blocking auto-close: %s\n",
			ticketID, strings.Join(children, ", "))
		return errors.New("refresh: open children block auto-close")
	}
	return finish.Run(db, beadsDir, ticketID, false, stdout)
}

// Check is a read-only verification that every linked dir on ticketID has an
// attached PR. With merged=true it additionally requires each dir's attached
// PR to have landed — MERGED on GitHub, or gone (a deleted PR is treated as
// merged-then-cleaned-up). It has no side effects (no push, no metadata
// write, no finish) and is meant to back a finish-step `check`.
//
// On failure it returns a non-nil error listing the offending dirs, which the
// CLI maps to a non-zero exit. As with Refresh, a ticket with zero linked dirs
// passes vacuously.
func Check(db, beadsDir, ticketID string, merged bool, stdout io.Writer) error {
	entries, err := dir.Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	recs, err := Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}

	if err := validateDirsHavePRs(entries, recs); err != nil {
		return err
	}
	if !merged {
		fmt.Fprintln(stdout, "every linked dir has an attached PR")
		return nil
	}

	notMerged, err := dirsWithoutMergedPR(entries, recs)
	if err != nil {
		return err
	}
	if len(notMerged) > 0 {
		return fmt.Errorf("dirs without a merged PR: %s", strings.Join(notMerged, ", "))
	}
	fmt.Fprintln(stdout, "every linked dir has a merged PR")
	return nil
}

// dirsWithoutMergedPR returns the paths of the linked dirs whose attached PR
// has not landed. A PR counts as landed when it is MERGED on GitHub, or when
// it no longer exists (deleted on GitHub is treated as merged-then-cleaned-up
// rather than as a failure). CLOSED-unmerged and still-OPEN PRs are offenders.
// It assumes validateDirsHavePRs has already confirmed every dir has a record;
// a dir whose record can't be resolved is still reported as an offender rather
// than silently passing. gh/network errors propagate.
func dirsWithoutMergedPR(entries []dir.Entry, recs []Record) ([]string, error) {
	dirByRepo, err := mapDirsByRepo(entries)
	if err != nil {
		return nil, err
	}
	recByRepo := make(map[string]Record, len(recs))
	for _, r := range recs {
		if _, ok := recByRepo[r.Repo]; !ok {
			recByRepo[r.Repo] = r
		}
	}
	var notMerged []string
	seen := map[string]bool{}
	// Walk entries (not the map) so the order is deterministic.
	for _, e := range entries {
		if seen[e.Path] {
			continue
		}
		seen[e.Path] = true
		rec, ok := recByRepo[repoFromMap(dirByRepo, e.Path)]
		if !ok {
			notMerged = append(notMerged, e.Path)
			continue
		}
		v, exists, err := prView(rec.Repo, rec.Number)
		if err != nil {
			return nil, err
		}
		// Deleted-on-GitHub: the PR is gone, which in practice means it was
		// merged and the branch cleaned up. Treat as landed.
		if !exists {
			continue
		}
		if strings.ToUpper(v.State) != "MERGED" {
			notMerged = append(notMerged, e.Path)
		}
	}
	return notMerged, nil
}

// validateDirsHavePRs verifies that every linked dir has at least one PR
// record whose repo matches it. Zero PRs across N dirs counts as N dirs
// without a PR — the error lists every offender.
func validateDirsHavePRs(entries []dir.Entry, recs []Record) error {
	if len(entries) == 0 {
		return nil
	}
	dirByRepo, err := mapDirsByRepo(entries)
	if err != nil {
		return err
	}
	repoSet := make(map[string]bool, len(recs))
	for _, r := range recs {
		repoSet[r.Repo] = true
	}
	var missing []string
	// Walk entries (not the map) so the order is deterministic.
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.Path] {
			continue
		}
		seen[e.Path] = true
		repo := repoFromMap(dirByRepo, e.Path)
		if repo == "" || !repoSet[repo] {
			missing = append(missing, e.Path)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("dirs without an attached PR: %s", strings.Join(missing, ", "))
	}
	return nil
}

// repoFromMap returns the repo for path by reverse-lookup. Returns "" if no
// repo was resolved for path (e.g. gh couldn't recognise the remote).
func repoFromMap(byRepo map[string]string, path string) string {
	for repo, p := range byRepo {
		if p == path {
			return repo
		}
	}
	return ""
}

// openChildren returns the ids of ticketID's children whose status is not
// closed.
func openChildren(db, beadsDir, ticketID string) ([]string, error) {
	out, err := bdcli.Cmd(beadsDir, db,
		"list", "--parent", ticketID, "--json", "--flat", "-n", "0").Output()
	if err != nil {
		return nil, fmt.Errorf("bd list --parent %s: %w", ticketID, withStderr(err))
	}
	out = []byte(strings.TrimSpace(string(out)))
	if len(out) == 0 {
		return nil, nil
	}
	var rows []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse bd list --parent: %w", err)
	}
	var ids []string
	for _, r := range rows {
		if r.Status == "closed" || r.ID == ticketID {
			continue
		}
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// resolveBase walks ticketID's parent chain and returns (branch, dirPath,
// isDefault) for the PR's base. dirPath is the path of the ancestor's
// linked dir on the same bare; it's "" when isDefault is true. isDefault
// is true when no ancestor matched and the repo's default branch was used.
func resolveBase(db, beadsDir, ticketID string, child dir.Entry) (branch string, dirPath string, isDefault bool, err error) {
	cur, err := dir.ParentID(db, beadsDir, ticketID)
	if err != nil {
		return "", "", false, err
	}
	for cur != "" {
		entries, err := dir.Load(db, beadsDir, cur)
		if err != nil {
			return "", "", false, err
		}
		if e, ok := matchingEntry(entries, child); ok {
			if _, err := os.Stat(e.Path); err != nil {
				return "", "", false, fmt.Errorf("ancestor %s of %s has been finished (worktree gone at %s); cannot use as PR base",
					cur, ticketID, e.Path)
			}
			return e.Branch, e.Path, false, nil
		}
		cur, err = dir.ParentID(db, beadsDir, cur)
		if err != nil {
			return "", "", false, err
		}
	}
	// Fallback: repo default branch.
	defBranch, err := defaultBranch(child.Path)
	if err != nil {
		return "", "", false, err
	}
	return defBranch, "", true, nil
}

// matchingEntry returns the entry in entries that shares the child's repo
// (matched by BareRepo when set, else by Repo URL, else by LocalRepo).
func matchingEntry(entries []dir.Entry, child dir.Entry) (dir.Entry, bool) {
	for _, e := range entries {
		switch {
		case child.BareRepo != "" && e.BareRepo == child.BareRepo:
			return e, true
		case child.Repo != "" && e.Repo == child.Repo:
			return e, true
		case child.LocalRepo != "" && e.LocalRepo == child.LocalRepo:
			return e, true
		}
	}
	return dir.Entry{}, false
}

// gitCurrentBranch returns the current branch name in dir.
func gitCurrentBranch(dir string) (string, error) {
	out, err := ExecGit(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// pushForceWithLease pushes branch from dir with --force-with-lease semantics,
// setting upstream. Works for new branches, FF advances, and diverged-history
// cases.
func pushForceWithLease(dir, branch string) error {
	_, err := ExecGit(dir, "push", "--force-with-lease", "--set-upstream", "origin", branch)
	return err
}

// branchOnRemote reports whether branch exists on origin for the repo
// associated with dir.
func branchOnRemote(dir, branch string) (bool, error) {
	out, err := ExecGit(dir, "ls-remote", "--heads", "origin", branch)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}

// openPRsForBranch returns the numbers of every open PR whose head is
// headBranch on repo. An empty slice means no open PR exists; a length of 2
// or more means the branch is not uniquely associated with a single open PR.
func openPRsForBranch(repo, headBranch string) ([]int, error) {
	out, err := ExecGH("", "pr", "list",
		"--repo", repo,
		"--head", headBranch,
		"--state", "open",
		"--json", "number")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse gh pr list: %w", err)
	}
	nums := make([]int, len(rows))
	for i, r := range rows {
		nums[i] = r.Number
	}
	return nums, nil
}

// prNumberList formats PR numbers as "#1, #2" for diagnostics.
func prNumberList(nums []int) string {
	parts := make([]string, len(nums))
	for i, n := range nums {
		parts[i] = "#" + strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
}

// entryOpenPRs resolves entry's repo identity, current branch, and the open
// PRs whose head is that branch. It is the shared front half of the
// single-dir create and auto-link flows. Bulk callers resolve the repo first
// (to skip already-attached dirs before any PR lookup) and call branchOpenPRs.
func entryOpenPRs(entry dir.Entry) (repo, branch string, nums []int, err error) {
	repo, err = nameWithOwner(entry.Path)
	if err != nil {
		return "", "", nil, err
	}
	branch, nums, err = branchOpenPRs(entry, repo)
	if err != nil {
		return "", "", nil, err
	}
	return repo, branch, nums, nil
}

// branchOpenPRs resolves entry's current branch and the open PRs whose head
// is that branch on repo.
func branchOpenPRs(entry dir.Entry, repo string) (branch string, nums []int, err error) {
	branch, err = gitCurrentBranch(entry.Path)
	if err != nil {
		return "", nil, err
	}
	if branch == "" || branch == "HEAD" {
		return "", nil, fmt.Errorf("dir %s is on a detached HEAD; check out a branch before creating or linking a PR", entry.Path)
	}
	nums, err = openPRsForBranch(repo, branch)
	if err != nil {
		return "", nil, err
	}
	return branch, nums, nil
}

// AutoLink attaches the known open PR num on repo/branch to ticketID, with
// the same same-repo duplicate guard as the no-url `pr link` paths. Used by
// `jira --discover-branches`, which already knows (repo, branch, number)
// from GitHub and needs no local-dir detection.
func AutoLink(db, beadsDir, ticketID, repo, branch string, num int, stdout io.Writer) error {
	existing, err := Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	return autoLinkPR(db, beadsDir, ticketID, repo, branch, num, existing, stdout)
}

// autoLinkPR appends a Record for the open PR num on repo/branch to the bead,
// after the same-repo duplicate guard. Shared by the no-url `pr link` paths
// and the `pr create` path that finds a pre-existing open PR.
func autoLinkPR(db, beadsDir, ticketID, repo, branch string, num int, existing []Record, stdout io.Writer) error {
	for _, r := range existing {
		if r.Repo == repo {
			return fmt.Errorf("bead %s already has a PR attached for %s (#%d)", ticketID, repo, r.Number)
		}
	}
	rec := Record{Repo: repo, Number: num, Branch: branch}
	if err := appendRecord(db, beadsDir, ticketID, rec); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "linked existing PR %s\n", rec.URL())
	return nil
}

// beadTitle returns the bead's title via `bd --json show`. "" when missing.
func beadTitle(db, beadsDir, ticketID string) (string, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "show", ticketID).Output()
	if err != nil {
		return "", fmt.Errorf("bd show %s: %w", ticketID, withStderr(err))
	}
	var rows []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return "", fmt.Errorf("parse bd show %s: %w", ticketID, err)
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].Title, nil
}

