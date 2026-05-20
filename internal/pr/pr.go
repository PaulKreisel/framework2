// Package pr implements the `ticket pr` subcommand group: attaching GitHub
// pull requests to a bead, refreshing their state, and auto-closing the bead
// once every attached PR is merged.
//
// Every GitHub interaction goes through the `gh` CLI. The gh / git callers are
// package-level vars so tests can substitute stubs (real `gh` is unavailable
// in CI and would hit the network anyway).
package pr

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"framework2/internal/bdcli"
)

// Record is one GitHub PR attached to a bead. Stored on the bead under
// metadata.prs as a JSON array.
type Record struct {
	Repo   string `json:"repo"`   // canonical "owner/name" reported by gh
	Number int    `json:"number"` // PR number
	Branch string `json:"branch"` // head branch at attach time
}

// URL returns the canonical https://github.com/<repo>/pull/<number> URL.
func (r Record) URL() string {
	return fmt.Sprintf("https://github.com/%s/pull/%d", r.Repo, r.Number)
}

// Load returns the PR records on ticketID. Returns nil (no error) when the
// bead has no metadata.prs entry yet.
func Load(db, beadsDir, ticketID string) ([]Record, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "show", ticketID).Output()
	if err != nil {
		return nil, fmt.Errorf("bd show %s: %w", ticketID, withStderr(err))
	}
	var rows []struct {
		Metadata map[string]json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse bd show %s: %w", ticketID, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("ticket not found: %s", ticketID)
	}
	raw, ok := rows[0].Metadata["prs"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var recs []Record
	if err := json.Unmarshal(raw, &recs); err != nil {
		return nil, fmt.Errorf("parse prs metadata for %s: %w", ticketID, err)
	}
	return recs, nil
}

// save replaces the metadata.prs array on ticketID with recs. Other metadata
// keys are preserved (bd update --metadata merges at the key level).
func save(db, beadsDir, ticketID string, recs []Record) error {
	if recs == nil {
		recs = []Record{}
	}
	payload, err := json.Marshal(map[string]any{"prs": recs})
	if err != nil {
		return err
	}
	out, err := bdcli.Cmd(beadsDir, db, "update", ticketID, "--metadata", string(payload)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("update ticket %s prs: %w: %s", ticketID, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// append adds rec to ticketID's PR records.
func appendRecord(db, beadsDir, ticketID string, rec Record) error {
	recs, err := Load(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	recs = append(recs, rec)
	return save(db, beadsDir, ticketID, recs)
}

// urlRegexp matches https://github.com/<owner>/<name>/pull/<number>[...].
var urlRegexp = regexp.MustCompile(`^https?://github\.com/([^/]+)/([^/]+)/pull/(\d+)(?:[/?#].*)?$`)

// ParseURL extracts (owner/name, number) from a GitHub PR URL.
func ParseURL(rawURL string) (repo string, number int, err error) {
	m := urlRegexp.FindStringSubmatch(strings.TrimSpace(rawURL))
	if m == nil {
		return "", 0, fmt.Errorf("not a github PR URL: %q", rawURL)
	}
	num, err := strconv.Atoi(m[3])
	if err != nil {
		return "", 0, fmt.Errorf("parse PR number from %q: %w", rawURL, err)
	}
	owner := m[1]
	name := strings.TrimSuffix(m[2], ".git")
	return owner + "/" + name, num, nil
}

// ExecGH runs `gh <args...>` (optionally chdir'd into dir) and returns
// stdout. Stubbed by tests.
var ExecGH = defaultExecGH

func defaultExecGH(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	if err != nil {
		return out, ghError(args, err)
	}
	return out, nil
}

// ExecGit runs `git -C <dir> <args...>` and returns stdout. Stubbed by tests.
// dir may be empty (rare; gh-style relative invocation).
var ExecGit = defaultExecGit

func defaultExecGit(dir string, args ...string) ([]byte, error) {
	full := args
	if dir != "" {
		full = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", full...)
	out, err := cmd.Output()
	if err != nil {
		return out, gitError(args, err)
	}
	return out, nil
}

func ghError(args []string, err error) error {
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		return fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
	}
	return fmt.Errorf("gh %s: %w", strings.Join(args, " "), err)
}

func gitError(args []string, err error) error {
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
	}
	return fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
}

func withStderr(err error) error {
	if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return err
}

// nameWithOwner reads the GitHub repo identity (`owner/name`) for the gh repo
// associated with dir. Errors if the dir's remote isn't a GitHub repo.
func nameWithOwner(dir string) (string, error) {
	out, err := ExecGH(dir, "repo", "view", "--json", "nameWithOwner")
	if err != nil {
		return "", err
	}
	var v struct {
		NameWithOwner string `json:"nameWithOwner"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", fmt.Errorf("parse gh repo view output: %w", err)
	}
	if v.NameWithOwner == "" {
		return "", errors.New("gh repo view returned an empty nameWithOwner; is the dir's remote a GitHub repo?")
	}
	return v.NameWithOwner, nil
}

// defaultBranch reads the GitHub repo's default branch (e.g. "main") for the
// repo associated with dir.
func defaultBranch(dir string) (string, error) {
	out, err := ExecGH(dir, "repo", "view", "--json", "defaultBranchRef")
	if err != nil {
		return "", err
	}
	var v struct {
		DefaultBranchRef struct {
			Name string `json:"name"`
		} `json:"defaultBranchRef"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", fmt.Errorf("parse gh repo view --json defaultBranchRef: %w", err)
	}
	if v.DefaultBranchRef.Name == "" {
		return "", errors.New("gh repo view did not report a defaultBranchRef")
	}
	return v.DefaultBranchRef.Name, nil
}

// PRView is the subset of `gh pr view` we consume.
type PRView struct {
	Number      int    `json:"number"`
	State       string `json:"state"`       // OPEN / CLOSED / MERGED
	IsDraft     bool   `json:"isDraft"`
	HeadRefName string `json:"headRefName"` // branch
	URL         string `json:"url"`
	Title       string `json:"title"`
}

// prView fetches a PR by repo+number. Returns (view, exists, err): exists is
// false when gh reports the PR could not be found (deleted / wrong id).
func prView(repo string, number int) (PRView, bool, error) {
	out, err := ExecGH("", "pr", "view",
		"--repo", repo, strconv.Itoa(number),
		"--json", "number,state,isDraft,headRefName,url,title")
	if err != nil {
		// gh exits non-zero with "no pull requests found" when the PR is
		// deleted or unknown. Treat that as "not found" rather than a hard
		// error so refresh can surface it.
		if isGHNotFound(err) {
			return PRView{}, false, nil
		}
		return PRView{}, false, err
	}
	var v PRView
	if err := json.Unmarshal(out, &v); err != nil {
		return PRView{}, false, fmt.Errorf("parse gh pr view %s#%d: %w", repo, number, err)
	}
	return v, true, nil
}

// isGHNotFound reports whether err looks like gh's "PR not found" failure
// (deleted PR, wrong id). gh's exit message is matched on stderr substring.
func isGHNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "could not find") ||
		strings.Contains(s, "no pull requests found") ||
		strings.Contains(s, "GraphQL: Could not resolve")
}

// stateLabel maps a PRView to the short label `pr list` prints.
func stateLabel(v PRView) string {
	switch strings.ToUpper(v.State) {
	case "MERGED":
		return "merged"
	case "CLOSED":
		return "closed"
	case "OPEN":
		if v.IsDraft {
			return "draft"
		}
		return "open"
	default:
		return strings.ToLower(v.State)
	}
}
