// Package jiracli implements the `jira` command: fetch one Jira issue and
// create a corresponding top-level bead in the current .beads/ db.
//
// Scope deliberately stays narrow: one Jira key in, one bead id out. Auth is
// via the legacy KNIME_ATLASSIAN_EMAIL / KNIME_ATLASSIAN_API_TOKEN env vars
// (a separate followup tracks moving credentials out of env).
package jiracli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"framework2/internal/bdcli"
	"framework2/internal/dir"
	"framework2/internal/githubcli"
	"framework2/internal/pr"
	"framework2/internal/project"
	"framework2/internal/projectconfig"
	"framework2/internal/repoclone"
	"framework2/internal/slug"
)

// DefaultBaseURL is the Atlassian site we fetch from. Tests override it via
// [Options].
const DefaultBaseURL = "https://knime-com.atlassian.net"

// httpTimeout caps each Jira call. Long enough for cold paths to the cloud,
// short enough to fail rather than hang an interactive shell.
const httpTimeout = 30 * time.Second

var keyPattern = regexp.MustCompile(`^[A-Z]+-[0-9]+$`)

// Options pins values that production reads from the environment. Tests use
// it to drive a fake Jira server and a per-test .beads/ directory.
type Options struct {
	// BaseURL overrides DefaultBaseURL.
	BaseURL string
	// BeadsDir overrides BEADS_DIR for the bd subprocess.
	BeadsDir string
	// DB, when non-empty, is forwarded as `--db <path>` to bd.
	DB string
	// HTTPClient overrides http.DefaultClient (with httpTimeout applied
	// when nil).
	HTTPClient *http.Client
	// Email + Token override the env-var auth lookup. Empty values fall back
	// to KNIME_ATLASSIAN_EMAIL / KNIME_ATLASSIAN_API_TOKEN.
	Email, Token string
	// GHRunner overrides the default `gh` shell-out used by
	// `--discover-branches`. Nil means: build a real-gh runner.
	GHRunner githubcli.GHRunner
}

// Run is the binary entrypoint. Returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunWithOptions(Options{}, args, stdout, stderr)
}

// RunWithOptions is like [Run] but lets callers pin auth, BEADS_DIR, and the
// Jira base URL. Used by tests.
func RunWithOptions(opts Options, args []string, stdout, stderr io.Writer) int {
	if opts.BeadsDir == "" {
		opts.BeadsDir = project.Dir()
	}
	if err := dispatch(opts, args, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "jira:", err)
		return 1
	}
	return 0
}

const usage = `usage: jira <KEY> [--discover-branches]

Fetch one Jira issue and create a top-level bead for it in the current .beads/
database. Prints the new bead id to stdout.

  --discover-branches   after creating the bead(s), run `+"`github branches <KEY>`"+`
                        and attach each distinct owner/repo as a worktree on
                        the root bead, resolving local bare paths via the
                        `+"`bare-repo:`"+` template in project.yaml. Each
                        discovered branch's unique open PR is then attached to
                        the root bead's metadata.prs (same as `+"`ticket pr link`"+`);
                        branches with zero or multiple open PRs are skipped
                        with a notice.

Auth: requires KNIME_ATLASSIAN_EMAIL and KNIME_ATLASSIAN_API_TOKEN.`

func dispatch(opts Options, args []string, stdout, stderr io.Writer) error {
	var (
		key             string
		sawKey          bool
		discoverBranchs bool
	)
	for _, a := range args {
		switch {
		case a == "-h" || a == "--help" || a == "help":
			fmt.Fprintln(stdout, usage)
			return nil
		case a == "--discover-branches":
			discoverBranchs = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q\n\n%s", a, usage)
		case !sawKey:
			key = a
			sawKey = true
		default:
			return fmt.Errorf("unexpected argument %q\n\n%s", a, usage)
		}
	}
	if !sawKey {
		return errors.New(usage)
	}
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("%q is not a Jira key (expected e.g. UIEXT-3141)", key)
	}

	email, token, err := credentials(opts)
	if err != nil {
		return err
	}

	ctx := context.Background()
	cfg, err := projectconfig.Load(opts.BeadsDir)
	if err != nil {
		return err
	}
	rootID, err := walkAndImport(ctx, opts, cfg, email, token, key)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(stdout, rootID); err != nil {
		return err
	}
	if discoverBranchs {
		if err := discoverAndAttach(ctx, opts, cfg, key, rootID, stderr); err != nil {
			return err
		}
	}
	return nil
}

// discoverAndAttach runs `github branches <key>` and attaches each distinct
// owner/repo to rootID as a worktree, resolving local bare-repo paths via the
// `bare-repo:` template in project.yaml. Strict: any missing template or
// missing bare path fails the whole call. After attaching, each discovered
// branch's unique open PR is linked to rootID's metadata.prs (the same effect
// as `ticket pr link`); per-branch notices go to stderr so stdout stays the
// bead id alone.
func discoverAndAttach(ctx context.Context, opts Options, cfg projectconfig.Config, key, rootID string, stderr io.Writer) error {
	if cfg.BareRepo == "" {
		return fmt.Errorf("--discover-branches: project.yaml has no `bare-repo:` template")
	}
	runner := opts.GHRunner
	if runner == nil {
		runner = githubcli.NewDefaultRunner(stderr)
	}
	matches, err := githubcli.ListBranches(ctx, runner, key)
	if err != nil {
		return fmt.Errorf("--discover-branches: %w", err)
	}
	for _, m := range matches {
		ownerRepo := m.Owner + "/" + m.Repo
		bare, err := cfg.RenderBareRepo(m.Owner, m.Repo)
		if err != nil {
			return fmt.Errorf("--discover-branches: resolve %s: %w", ownerRepo, err)
		}
		if err := repoclone.EnsureBare(bare, m.Owner, m.Repo, cfg.SetupScript); err != nil {
			return fmt.Errorf("--discover-branches: %s: %w", ownerRepo, err)
		}
		if err := dir.WorktreeForBranch(opts.DB, opts.BeadsDir, rootID, bare, m.Branch, false); err != nil {
			return fmt.Errorf("--discover-branches: attach %s@%s: %w", ownerRepo, m.Branch, err)
		}
	}
	return linkDiscoveredPRs(ctx, opts, runner, rootID, matches, stderr)
}

// linkDiscoveredPRs attaches the unique open PR of each discovered branch to
// rootID, mirroring the `ticket pr link --all` semantics: branches with zero
// or multiple open PRs are skipped with a notice, and a repo that already has
// a PR attached on the bead is skipped too (which keeps re-runs of `jira
// --discover-branches` idempotent). The (repo, branch) pairs come straight
// from branch discovery, so no local-dir PR detection is needed. PR-list
// failures are strict, consistent with the rest of the discover flow.
func linkDiscoveredPRs(ctx context.Context, opts Options, runner githubcli.GHRunner, rootID string, matches []githubcli.Branch, stderr io.Writer) error {
	for _, m := range matches {
		ownerRepo := m.Owner + "/" + m.Repo
		// Reload each iteration so earlier links are reflected in the
		// duplicate check (consistent with `pr link --all`).
		existing, err := pr.Load(opts.DB, opts.BeadsDir, rootID)
		if err != nil {
			return fmt.Errorf("--discover-branches: %w", err)
		}
		attached := false
		for _, r := range existing {
			if r.Repo == ownerRepo {
				attached = true
				break
			}
		}
		if attached {
			fmt.Fprintf(stderr, "%s@%s: PR already attached for %s, skipping\n", ownerRepo, m.Branch, ownerRepo)
			continue
		}
		nums, err := githubcli.OpenPRNumbers(ctx, runner, ownerRepo, m.Branch)
		if err != nil {
			return fmt.Errorf("--discover-branches: %w", err)
		}
		switch len(nums) {
		case 0:
			fmt.Fprintf(stderr, "%s@%s: no open PR, skipping\n", ownerRepo, m.Branch)
		case 1:
			if err := pr.AutoLink(opts.DB, opts.BeadsDir, rootID, ownerRepo, m.Branch, nums[0], stderr); err != nil {
				return fmt.Errorf("--discover-branches: link PR for %s@%s: %w", ownerRepo, m.Branch, err)
			}
		default:
			fmt.Fprintf(stderr, "%s@%s: multiple open PRs, skipping\n", ownerRepo, m.Branch)
		}
	}
	return nil
}

func credentials(opts Options) (email, token string, err error) {
	email = opts.Email
	if email == "" {
		email = os.Getenv("KNIME_ATLASSIAN_EMAIL")
	}
	token = opts.Token
	if token == "" {
		token = os.Getenv("KNIME_ATLASSIAN_API_TOKEN")
	}
	if email == "" || token == "" {
		return "", "", errors.New("KNIME_ATLASSIAN_EMAIL and KNIME_ATLASSIAN_API_TOKEN must be set")
	}
	return email, token, nil
}

// IssueRef is a minimal Jira issue reference, used for the subtasks list and
// the parent link.
type IssueRef struct {
	Key string `json:"key"`
}

// IssueLinkType captures just the name fields we need from a Jira issue link
// type. The `inward` text (e.g. "is blocked by") is what we match on.
type IssueLinkType struct {
	Name    string `json:"name"`
	Inward  string `json:"inward"`
	Outward string `json:"outward"`
}

// IssueLink is one entry in Jira's `issuelinks` field. Exactly one of
// InwardIssue / OutwardIssue is populated, depending on this issue's role in
// the link.
type IssueLink struct {
	Type         IssueLinkType `json:"type"`
	InwardIssue  *IssueRef     `json:"inwardIssue,omitempty"`
	OutwardIssue *IssueRef     `json:"outwardIssue,omitempty"`
}

// Issue is the subset of Jira's REST v2 response we consume.
type Issue struct {
	Key    string `json:"key"`
	Fields struct {
		Summary     string `json:"summary"`
		Description string `json:"description"`
		IssueType   struct {
			Name string `json:"name"`
		} `json:"issuetype"`
		// customfield_11444 is KNIME's "Steps to Reproduce" field.
		StepsToReproduce string `json:"customfield_11444"`
		// Subtasks lists classic Jira sub-tasks of this issue.
		Subtasks []IssueRef `json:"subtasks"`
		// Parent is the issue this one hangs off of in next-gen
		// parent/child relationships. nil for top-level issues.
		Parent *IssueRef `json:"parent,omitempty"`
		// IssueLinks lists generic Jira link relationships (blocks,
		// relates to, …). We only act on inward "is blocked by" links.
		IssueLinks []IssueLink `json:"issuelinks"`
	} `json:"fields"`
}

// Fetch retrieves one Jira issue. Exported for tests; production callers go
// through [Run].
func Fetch(ctx context.Context, opts Options, email, token, key string) (*Issue, error) {
	base := opts.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	reqURL := fmt.Sprintf("%s/rest/api/2/issue/%s?fields=summary,description,issuetype,customfield_11444,subtasks,parent,issuelinks", base, key)

	cctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.SetBasicAuth(email, token)
	req.Header.Set("Accept", "application/json")

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", key, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("Jira issue %s not found", key)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("Jira auth failed (%d) — check KNIME_ATLASSIAN_EMAIL / KNIME_ATLASSIAN_API_TOKEN", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("fetch %s: status %d: %s", key, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var issue Issue
	if err := json.NewDecoder(resp.Body).Decode(&issue); err != nil {
		return nil, fmt.Errorf("parse Jira response for %s: %w", key, err)
	}
	return &issue, nil
}

// MapType converts a Jira issuetype name to a beads type. Mirrors the upstream
// beads jira integration (gastownhall/beads internal/jira/fieldmapper.go) with
// a KNIME-specific addition: Regression → bug.
func MapType(jiraType string) string {
	switch jiraType {
	case "Bug", "Regression":
		return "bug"
	case "Story", "Feature", "Enhancement", "New Feature":
		return "feature"
	case "Epic":
		return "epic"
	case "Task", "Sub-task":
		return "task"
	default:
		return "task"
	}
}

// BuildDescription returns the bead description body: Jira description verbatim,
// with "## Steps to Reproduce" appended when customfield_11444 is non-empty.
func BuildDescription(issue *Issue) string {
	desc := strings.TrimRight(issue.Fields.Description, "\n")
	steps := strings.TrimSpace(issue.Fields.StepsToReproduce)
	if steps == "" {
		return desc
	}
	if desc == "" {
		return "## Steps to Reproduce\n\n" + steps
	}
	return desc + "\n\n## Steps to Reproduce\n\n" + steps
}

// createBead shells out to `bd create` and returns the new bead id. When
// parentBead is non-empty it is forwarded as `--parent`.
func createBead(opts Options, cfg projectconfig.Config, key string, issue *Issue, parentBead string) (string, error) {
	title := issue.Fields.Summary
	body := BuildDescription(issue)
	beadType := MapType(issue.Fields.IssueType.Name)
	if override, ok := cfg.MapJiraType(issue.Fields.IssueType.Name); ok {
		beadType = override
	}
	base := opts.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	externalRef := fmt.Sprintf("%s/browse/%s", strings.TrimRight(base, "/"), key)

	args := []string{
		"create", title,
		"--silent",
		"--type", beadType,
		"--priority", "2",
		"--external-ref", externalRef,
	}
	if parentBead != "" {
		args = append(args, "--parent", parentBead)
	}
	if body != "" {
		args = append(args, "--body-file", "-")
	}
	if s := slug.Derive(issue.Fields.Summary, slug.MaxLen); s != "" {
		meta, _ := json.Marshal(map[string]string{"slug": s})
		args = append(args, "--metadata", string(meta))
	}

	cmd := bdcli.Cmd(opts.BeadsDir, opts.DB, args...)
	if body != "" {
		cmd.Stdin = strings.NewReader(body)
	}
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("bd create: %v: %s", err, ee.Stderr)
		}
		return "", fmt.Errorf("bd create: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", errors.New("bd create returned empty id")
	}
	return id, nil
}

// walkAndImport imports rootKey and every descendant reachable via the
// `subtasks` field or via a reverse `parent` lookup. Issues are fetched at
// most once (in-memory cache keyed by Jira key) and each bead is created with
// `--parent` pointing at its imported parent bead, so `bd list --parent <root>`
// reproduces the Jira hierarchy. BFS order keeps parents created before their
// children, which is what bd requires.
func walkAndImport(ctx context.Context, opts Options, cfg projectconfig.Config, email, token, rootKey string) (string, error) {
	type work struct{ key, parentBead string }

	issueCache := map[string]*Issue{}
	imported := map[string]string{} // jira key -> bead id
	var rootID string

	queue := []work{{key: rootKey}}
	for len(queue) > 0 {
		w := queue[0]
		queue = queue[1:]
		if _, done := imported[w.key]; done {
			continue
		}

		issue, ok := issueCache[w.key]
		if !ok {
			fetched, err := Fetch(ctx, opts, email, token, w.key)
			if err != nil {
				return "", err
			}
			issueCache[w.key] = fetched
			issue = fetched
		}

		beadID, err := createBead(opts, cfg, w.key, issue, w.parentBead)
		if err != nil {
			return "", err
		}
		imported[w.key] = beadID
		if w.parentBead == "" {
			rootID = beadID
		}

		children, err := discoverChildren(ctx, opts, email, token, w.key, issue)
		if err != nil {
			return "", err
		}
		for _, ck := range children {
			if _, done := imported[ck]; done {
				continue
			}
			queue = append(queue, work{key: ck, parentBead: beadID})
		}
	}

	if err := wireBlockedBy(opts, imported, issueCache); err != nil {
		return "", err
	}
	return rootID, nil
}

// wireBlockedBy translates Jira's inward "is blocked by" links into
// `bd dep add` edges, but only between issues that were imported in this run.
// Links whose target is outside the imported tree are silently skipped.
func wireBlockedBy(opts Options, imported map[string]string, issueCache map[string]*Issue) error {
	for key, beadID := range imported {
		issue := issueCache[key]
		if issue == nil {
			continue
		}
		for _, link := range issue.Fields.IssueLinks {
			if link.InwardIssue == nil || link.Type.Inward != "is blocked by" {
				continue
			}
			blockerKey := link.InwardIssue.Key
			blockerBead, ok := imported[blockerKey]
			if !ok {
				continue
			}
			cmd := bdcli.Cmd(opts.BeadsDir, opts.DB, "dep", "add", beadID, blockerBead)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Errorf("bd dep add %s %s: %w: %s", beadID, blockerBead, err, strings.TrimSpace(string(out)))
			}
		}
	}
	return nil
}

// discoverChildren returns the direct children of issue: every key in its
// `subtasks` field, plus every issue Jira reports as having issue.Key as its
// parent. For Epic-typed issues, when neither subtasks nor the `parent` JQL
// search yields anything, falls back to the legacy `"Epic Link" = <KEY>` JQL
// so older projects (where stories don't carry a `parent` field) still work.
// Order is preserved (subtasks first, then reverse-parent matches, then
// epic-link matches) so test output stays deterministic.
func discoverChildren(ctx context.Context, opts Options, email, token, parentKey string, issue *Issue) ([]string, error) {
	seen := map[string]bool{parentKey: true}
	var out []string
	for _, st := range issue.Fields.Subtasks {
		if st.Key == "" || seen[st.Key] {
			continue
		}
		seen[st.Key] = true
		out = append(out, st.Key)
	}
	reverse, err := searchByJQL(ctx, opts, email, token, fmt.Sprintf("parent = %s", parentKey))
	if err != nil {
		return nil, err
	}
	for _, k := range reverse {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	if issue.Fields.IssueType.Name == "Epic" && len(out) == 0 {
		viaEpicLink, err := searchByJQL(ctx, opts, email, token, fmt.Sprintf(`"Epic Link" = %s`, parentKey))
		if err != nil {
			return nil, err
		}
		for _, k := range viaEpicLink {
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, k)
		}
	}
	return out, nil
}

// searchByJQL runs an arbitrary JQL query against /rest/api/3/search/jql and
// returns the resulting issue keys. Walks pages via the opaque nextPageToken
// until Jira reports isLast (or stops handing back a token).
func searchByJQL(ctx context.Context, opts Options, email, token, jql string) ([]string, error) {
	base := opts.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
	}

	const pageSize = 100
	var keys []string
	var nextPageToken string
	for {
		body := map[string]any{
			"jql":        jql,
			"fields":     []string{"key"},
			"maxResults": pageSize,
		}
		if nextPageToken != "" {
			body["nextPageToken"] = nextPageToken
		}
		bodyJSON, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode search request for %q: %w", jql, err)
		}

		cctx, cancel := context.WithTimeout(ctx, httpTimeout)
		req, err := http.NewRequestWithContext(cctx, http.MethodPost,
			base+"/rest/api/3/search/jql", bytes.NewReader(bodyJSON))
		if err != nil {
			cancel()
			return nil, fmt.Errorf("build search request: %w", err)
		}
		req.SetBasicAuth(email, token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("search %q: %w", jql, err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			cancel()
			return nil, fmt.Errorf("search %q: status %d: %s",
				jql, resp.StatusCode, strings.TrimSpace(string(respBody)))
		}
		var page struct {
			Issues []struct {
				Key string `json:"key"`
			} `json:"issues"`
			NextPageToken string `json:"nextPageToken"`
			IsLast        bool   `json:"isLast"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("parse search response for %q: %w", jql, err)
		}
		for _, i := range page.Issues {
			keys = append(keys, i.Key)
		}
		if page.IsLast || page.NextPageToken == "" {
			break
		}
		nextPageToken = page.NextPageToken
	}
	return keys, nil
}
