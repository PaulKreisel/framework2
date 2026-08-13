// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package githubcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildBranchQuery(t *testing.T) {
	t.Parallel()
	q := buildBranchQuery("knime", []string{"knime-core", "knime-ui"})
	wants := []string{
		`r0: repository(owner: "knime", name: "knime-core")`,
		`r1: repository(owner: "knime", name: "knime-ui")`,
		`refPrefix: "refs/heads/"`,
		`first: 100`,
	}
	for _, w := range wants {
		if !strings.Contains(q, w) {
			t.Errorf("query missing %q\nquery: %s", w, q)
		}
	}
}

func TestBuildBranchQueryEscapesRepoName(t *testing.T) {
	t.Parallel()
	// Repo names with embedded quotes shouldn't break the GraphQL document.
	// %q in the builder takes care of escaping.
	q := buildBranchQuery("o", []string{`weird"name`})
	if !strings.Contains(q, `name: "weird\"name"`) {
		t.Errorf("repo name not escaped:\n%s", q)
	}
}

func TestParseBranchResponseFilters(t *testing.T) {
	t.Parallel()
	raw := []byte(`{
	  "data": {
	    "r0": {
	      "name": "knime-core",
	      "refs": {"nodes": [
	        {"name": "main"},
	        {"name": "fix/UIEXT-3141-foo"},
	        {"name": "feature/uiext-3141-bar"}
	      ]}
	    },
	    "r1": {
	      "name": "knime-ui",
	      "refs": {"nodes": [
	        {"name": "develop"}
	      ]}
	    }
	  }
	}`)
	got, err := parseBranchResponse(raw, "knime", "UIEXT-3141")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Branch < got[j].Branch })
	want := []Branch{
		{Owner: "knime", Repo: "knime-core", Branch: "feature/uiext-3141-bar"},
		{Owner: "knime", Repo: "knime-core", Branch: "fix/UIEXT-3141-foo"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestParseBranchResponseEmptyData(t *testing.T) {
	t.Parallel()
	got, err := parseBranchResponse([]byte(`{"data": {}}`), "knime", "anything")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no matches, got %+v", got)
	}
}

func TestParseBranchResponseNullRepo(t *testing.T) {
	t.Parallel()
	// A null alias (e.g. permission denied for a single repo) must be skipped
	// silently rather than panicking the parser.
	raw := []byte(`{"data": {"r0": null, "r1": {"name": "ok", "refs": {"nodes": [{"name": "fix/x"}]}}}}`)
	got, err := parseBranchResponse(raw, "o", "fix")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []Branch{{Owner: "o", Repo: "ok", Branch: "fix/x"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

// fakeRunner is a programmable [GHRunner] for end-to-end dispatch tests.
type fakeRunner struct {
	t            *testing.T
	mu           sync.Mutex
	orgs         []string
	reposByOrg   map[string][]repoInfo
	graphqlReply map[string]map[string][]string // owner -> repo -> branch names
	calls        []string
}

func (f *fakeRunner) run(_ context.Context, args []string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(args, " "))
	f.mu.Unlock()

	switch {
	case len(args) >= 2 && args[0] == "api" && args[1] == "user/orgs":
		return []byte(strings.Join(f.orgs, "\n") + "\n"), nil
	case len(args) >= 2 && args[0] == "repo" && args[1] == "list":
		org := args[2]
		b, err := json.Marshal(f.reposByOrg[org])
		if err != nil {
			return nil, err
		}
		return b, nil
	case len(args) >= 2 && args[0] == "api" && args[1] == "graphql":
		// Find the -f query=... arg and decode which repos it asked for.
		var query string
		for _, a := range args {
			if rest, ok := strings.CutPrefix(a, "query="); ok {
				query = rest
				break
			}
		}
		owner := ""
		repos := []string{}
		// Parse owner/name pairs out of the query string. The builder
		// produces `owner: "X", name: "Y"`. A toy parser is enough here.
		for chunk := range strings.SplitSeq(query, "repository(") {
			if !strings.Contains(chunk, "owner:") {
				continue
			}
			o, n := extractOwnerName(chunk)
			if o == "" {
				continue
			}
			owner = o
			repos = append(repos, n)
		}
		data := map[string]any{}
		for i, repo := range repos {
			nodes := []map[string]string{}
			for _, br := range f.graphqlReply[owner][repo] {
				nodes = append(nodes, map[string]string{"name": br})
			}
			data[fmt.Sprintf("r%d", i)] = map[string]any{
				"name": repo,
				"refs": map[string]any{"nodes": nodes},
			}
		}
		return json.Marshal(map[string]any{"data": data})
	}
	f.t.Fatalf("fakeRunner: unexpected args %v", args)
	return nil, nil
}

// extractOwnerName pulls owner / name from a substring of the form
// `owner: "foo", name: "bar") { ... `.
func extractOwnerName(s string) (owner, name string) {
	const oPrefix = `owner: "`
	const nPrefix = `name: "`
	i := strings.Index(s, oPrefix)
	if i < 0 {
		return "", ""
	}
	rest := s[i+len(oPrefix):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return "", ""
	}
	owner = rest[:j]
	rest = rest[j+1:]
	k := strings.Index(rest, nPrefix)
	if k < 0 {
		return owner, ""
	}
	rest = rest[k+len(nPrefix):]
	l := strings.Index(rest, `"`)
	if l < 0 {
		return owner, ""
	}
	name = rest[:l]
	return owner, name
}

func TestRunBranchesHumanOutput(t *testing.T) {
	t.Parallel()
	now := time.Now()
	fr := &fakeRunner{
		t:    t,
		orgs: []string{"knime", "knime-community"},
		reposByOrg: map[string][]repoInfo{
			"knime": {
				{Name: "knime-core", PushedAt: now},
				{Name: "stale-repo", PushedAt: now.AddDate(-2, 0, 0)}, // older than 1y
			},
			"knime-community": {
				{Name: "community-thing", PushedAt: now},
			},
		},
		graphqlReply: map[string]map[string][]string{
			"knime": {
				"knime-core": {"main", "fix/UIEXT-9-foo", "feature/uiext-9-bar"},
			},
			"knime-community": {
				"community-thing": {"main", "topic/UIEXT-9"},
			},
		},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithOptions(Options{GHRunner: fr.run},
		[]string{"branches", "UIEXT-9"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	want := "knime-community/community-thing\ttopic/UIEXT-9\n" +
		"knime/knime-core\tfeature/uiext-9-bar\n" +
		"knime/knime-core\tfix/UIEXT-9-foo\n"
	if stdout.String() != want {
		t.Errorf("stdout mismatch.\n got: %q\nwant: %q", stdout.String(), want)
	}
	// Stale repos must not be queried.
	for _, c := range fr.calls {
		if strings.Contains(c, "stale-repo") {
			t.Errorf("stale repo leaked into GraphQL call: %s", c)
		}
	}
}

func TestRunBranchesJSONOutput(t *testing.T) {
	t.Parallel()
	now := time.Now()
	fr := &fakeRunner{
		t:    t,
		orgs: []string{"o"},
		reposByOrg: map[string][]repoInfo{
			"o": {{Name: "r", PushedAt: now}},
		},
		graphqlReply: map[string]map[string][]string{
			"o": {"r": {"main", "fix/x"}},
		},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithOptions(Options{GHRunner: fr.run},
		[]string{"branches", "fix", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	// Verify the JSON parses back to the expected slice.
	var got []Branch
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode json: %v\noutput: %s", err, stdout.String())
	}
	want := []Branch{{Owner: "o", Repo: "r", Branch: "fix/x"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v want %+v", got, want)
	}
}

func TestRunBranchesNoMatches(t *testing.T) {
	t.Parallel()
	now := time.Now()
	fr := &fakeRunner{
		t:    t,
		orgs: []string{"o"},
		reposByOrg: map[string][]repoInfo{
			"o": {{Name: "r", PushedAt: now}},
		},
		graphqlReply: map[string]map[string][]string{
			"o": {"r": {"main", "develop"}},
		},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithOptions(Options{GHRunner: fr.run},
		[]string{"branches", "nope"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if stdout.String() != "No branches found.\n" {
		t.Errorf("unexpected stdout %q", stdout.String())
	}
}

func TestRunBranchesNoMatchesJSON(t *testing.T) {
	t.Parallel()
	fr := &fakeRunner{
		t:          t,
		orgs:       []string{"o"},
		reposByOrg: map[string][]repoInfo{"o": {}},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithOptions(Options{GHRunner: fr.run},
		[]string{"branches", "x", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != "[]" {
		t.Errorf("expected [], got %q", stdout.String())
	}
}

func TestRunRejectsBadArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, want string
		args       []string
	}{
		{"no subcommand", "usage:", nil},
		{"unknown subcommand", "unknown subcommand", []string{"frobnicate"}},
		{"branches without term", "usage:", []string{"branches"}},
		{"unknown flag", "unknown flag", []string{"branches", "x", "--bogus"}},
		{"extra arg", "unexpected argument", []string{"branches", "a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := RunWithOptions(Options{GHRunner: func(context.Context, []string) ([]byte, error) {
				t.Errorf("runner should not be invoked")
				return nil, nil
			}}, tc.args, &stdout, &stderr)
			if code == 0 {
				t.Fatalf("expected nonzero exit; stderr=%q", stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr=%q missing %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestRunHelp(t *testing.T) {
	t.Parallel()
	cases := [][]string{
		{"--help"},
		{"-h"},
		{"help"},
		{"branches", "--help"},
		{"branches", "-h"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := RunWithOptions(Options{GHRunner: func(context.Context, []string) ([]byte, error) {
				t.Errorf("runner should not be invoked")
				return nil, nil
			}}, args, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), "usage:") {
				t.Errorf("stdout missing usage: %q", stdout.String())
			}
		})
	}
}

func TestRunBranchesPropagatesRunnerError(t *testing.T) {
	t.Parallel()
	want := "boom: not authenticated"
	runner := func(context.Context, []string) ([]byte, error) {
		return nil, fmt.Errorf("%s", want)
	}
	var stdout, stderr bytes.Buffer
	code := RunWithOptions(Options{GHRunner: runner},
		[]string{"branches", "x"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected nonzero exit")
	}
	if !strings.Contains(stderr.String(), want) {
		t.Errorf("stderr=%q missing %q", stderr.String(), want)
	}
}
