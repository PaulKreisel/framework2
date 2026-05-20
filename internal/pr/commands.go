package pr

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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

	repo, err := nameWithOwner(entry.Path)
	if err != nil {
		return err
	}

	branch, err := gitCurrentBranch(entry.Path)
	if err != nil {
		return err
	}
	if branch == "" || branch == "HEAD" {
		return fmt.Errorf("dir %s is on a detached HEAD; check out a branch before creating a PR", entry.Path)
	}

	if existing, err := openPRForBranch(repo, branch); err != nil {
		return err
	} else if existing != 0 {
		return fmt.Errorf("an open PR for %s on branch %s already exists (#%d); use `ticket pr link` to attach it",
			repo, branch, existing)
	}

	// Refuse a second PR for the same (bead, dir) pair. Identity is by repo
	// because storage doesn't track the originating dir directly.
	existing, err := Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	for _, r := range existing {
		if r.Repo == repo {
			return fmt.Errorf("bead %s already has a PR attached for %s (#%d)", ticketID, repo, r.Number)
		}
	}

	base, baseDir, isDefault, err := resolveBase(db, beadsDir, ticketID, *entry)
	if err != nil {
		return err
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
	fmt.Fprintln(stdout, newID)
	return nil
}

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

// openPRForBranch returns the number of an open PR for headBranch on repo,
// or 0 if none exists.
func openPRForBranch(repo, headBranch string) (int, error) {
	out, err := ExecGH("", "pr", "list",
		"--repo", repo,
		"--head", headBranch,
		"--state", "open",
		"--json", "number")
	if err != nil {
		return 0, err
	}
	var rows []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return 0, fmt.Errorf("parse gh pr list: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Number, nil
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

