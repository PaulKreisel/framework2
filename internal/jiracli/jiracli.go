// Package jiracli implements the `jira` command: fetch one Jira issue and
// create a corresponding top-level bead in the current .beads/ db.
//
// Scope deliberately stays narrow: one Jira key in, one bead id out. Auth is
// via the legacy KNIME_ATLASSIAN_EMAIL / KNIME_ATLASSIAN_API_TOKEN env vars
// (a separate followup tracks moving credentials out of env).
package jiracli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"framework2/internal/bdcli"
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
}

// Run is the binary entrypoint. Returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunWithOptions(Options{}, args, stdout, stderr)
}

// RunWithOptions is like [Run] but lets callers pin auth, BEADS_DIR, and the
// Jira base URL. Used by tests.
func RunWithOptions(opts Options, args []string, stdout, stderr io.Writer) int {
	if err := dispatch(opts, args, stdout); err != nil {
		fmt.Fprintln(stderr, "jira:", err)
		return 1
	}
	return 0
}

const usage = `usage: jira <KEY>

Fetch one Jira issue and create a top-level bead for it in the current .beads/
database. Prints the new bead id to stdout.

Auth: requires KNIME_ATLASSIAN_EMAIL and KNIME_ATLASSIAN_API_TOKEN.`

func dispatch(opts Options, args []string, stdout io.Writer) error {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			fmt.Fprintln(stdout, usage)
			return nil
		}
	}
	if len(args) != 1 {
		return errors.New(usage)
	}
	key := args[0]
	if !keyPattern.MatchString(key) {
		return fmt.Errorf("%q is not a Jira key (expected e.g. UIEXT-3141)", key)
	}

	email, token, err := credentials(opts)
	if err != nil {
		return err
	}

	rootID, err := walkAndImport(context.Background(), opts, email, token, key)
	if err != nil {
		return err
	}
	_, werr := fmt.Fprintln(stdout, rootID)
	return werr
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
	} `json:"fields"`
}

// Fetch retrieves one Jira issue. Exported for tests; production callers go
// through [Run].
func Fetch(ctx context.Context, opts Options, email, token, key string) (*Issue, error) {
	base := opts.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	reqURL := fmt.Sprintf("%s/rest/api/2/issue/%s?fields=summary,description,issuetype,customfield_11444,subtasks,parent", base, key)

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
func createBead(opts Options, key string, issue *Issue, parentBead string) (string, error) {
	title := issue.Fields.Summary
	body := BuildDescription(issue)
	beadType := MapType(issue.Fields.IssueType.Name)
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
func walkAndImport(ctx context.Context, opts Options, email, token, rootKey string) (string, error) {
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

		beadID, err := createBead(opts, w.key, issue, w.parentBead)
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
	return rootID, nil
}

// discoverChildren returns the direct children of issue: every key in its
// `subtasks` field, plus every issue Jira reports as having issue.Key as its
// parent. Order is preserved (subtasks first, then reverse-parent matches not
// already covered) so test output stays deterministic.
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
	reverse, err := searchChildKeys(ctx, opts, email, token, parentKey)
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
	return out, nil
}

// searchChildKeys runs a JQL search for `parent = <parentKey>` and returns
// the resulting issue keys. Walks pages until Jira reports it has returned
// everything.
func searchChildKeys(ctx context.Context, opts Options, email, token, parentKey string) ([]string, error) {
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
	startAt := 0
	for {
		jql := url.QueryEscape(fmt.Sprintf("parent = %s", parentKey))
		u := fmt.Sprintf("%s/rest/api/2/search?jql=%s&fields=key&startAt=%d&maxResults=%d",
			base, jql, startAt, pageSize)

		cctx, cancel := context.WithTimeout(ctx, httpTimeout)
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, u, nil)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("build search request: %w", err)
		}
		req.SetBasicAuth(email, token)
		req.Header.Set("Accept", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("search children of %s: %w", parentKey, err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			cancel()
			return nil, fmt.Errorf("search children of %s: status %d: %s",
				parentKey, resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var page struct {
			StartAt int `json:"startAt"`
			Total   int `json:"total"`
			Issues  []struct {
				Key string `json:"key"`
			} `json:"issues"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("parse search response for %s: %w", parentKey, err)
		}
		for _, i := range page.Issues {
			keys = append(keys, i.Key)
		}
		if len(page.Issues) == 0 {
			break
		}
		startAt += len(page.Issues)
		if startAt >= page.Total {
			break
		}
	}
	return keys, nil
}
