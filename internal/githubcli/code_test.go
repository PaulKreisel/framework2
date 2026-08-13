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
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestGroupCodeMatchesSplitsByPathContents(t *testing.T) {
	t.Parallel()
	matches := []CodeMatch{
		{Repo: "knime/knime-core", Path: "src/Unpivot.java"},
		{Repo: "knime/knime-core", Path: "src/Pivot.java"},
		{Repo: "knime/knime-ui", Path: "components/UNPIVOT_widget.ts"},
		{Repo: "knime/knime-ui", Path: "components/other.ts"},
	}
	got := groupCodeMatches(matches, []string{"Unpivot"})
	// Both repos have count 2; ties break by repo name ascending.
	want := []RepoCodeMatches{
		{
			Repo:        "knime/knime-core",
			Count:       2,
			NameMatches: []string{"src/Unpivot.java"},
			ContentOnly: []string{"src/Pivot.java"},
		},
		{
			Repo:        "knime/knime-ui",
			Count:       2,
			NameMatches: []string{"components/UNPIVOT_widget.ts"},
			ContentOnly: []string{"components/other.ts"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestGroupCodeMatchesSortsByCountDesc(t *testing.T) {
	t.Parallel()
	matches := []CodeMatch{
		{Repo: "o/small", Path: "a"},
		{Repo: "o/big", Path: "a"},
		{Repo: "o/big", Path: "b"},
		{Repo: "o/big", Path: "c"},
	}
	got := groupCodeMatches(matches, []string{"x"})
	if len(got) != 2 || got[0].Repo != "o/big" || got[1].Repo != "o/small" {
		t.Errorf("expected o/big first, got %+v", got)
	}
}

func TestGroupCodeMatchesMultipleTerms(t *testing.T) {
	t.Parallel()
	matches := []CodeMatch{
		{Repo: "o/r", Path: "src/Alpha.java"},
		{Repo: "o/r", Path: "src/Beta.java"},
		{Repo: "o/r", Path: "src/Other.java"},
	}
	got := groupCodeMatches(matches, []string{"alpha", "beta"})
	if len(got) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(got))
	}
	if !reflect.DeepEqual(got[0].NameMatches, []string{"src/Alpha.java", "src/Beta.java"}) {
		t.Errorf("name_matches = %v", got[0].NameMatches)
	}
	if !reflect.DeepEqual(got[0].ContentOnly, []string{"src/Other.java"}) {
		t.Errorf("content_only = %v", got[0].ContentOnly)
	}
}

func TestWriteCodeHumanFullMode(t *testing.T) {
	t.Parallel()
	repos := []RepoCodeMatches{
		{
			Repo:        "o/r",
			Count:       2,
			NameMatches: []string{"src/Match.java"},
			ContentOnly: []string{"src/Other.java"},
		},
	}
	var buf bytes.Buffer
	if err := writeCodeHuman(&buf, repos, 100); err != nil {
		t.Fatalf("writeCodeHuman: %v", err)
	}
	want := "o/r (2):\n" +
		"  src/Match.java\n" +
		"  src/Other.java\n" +
		"\nTotal: 2 results across 1 repos\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestWriteCodeHumanAbbreviatedMode(t *testing.T) {
	t.Parallel()
	// Total = 5; threshold = 3 -> abbreviated.
	repos := []RepoCodeMatches{
		{
			Repo:        "o/has-name-and-content",
			Count:       3,
			NameMatches: []string{"src/Match.java"},
			ContentOnly: []string{"a", "b"},
		},
		{
			Repo:        "o/content-only",
			Count:       2,
			NameMatches: []string{},
			ContentOnly: []string{"x", "y"},
		},
	}
	var buf bytes.Buffer
	if err := writeCodeHuman(&buf, repos, 3); err != nil {
		t.Fatalf("writeCodeHuman: %v", err)
	}
	want := "o/has-name-and-content (3):\n" +
		"  src/Match.java\n" +
		"  ... +2 content-only\n" +
		"o/content-only (2 content-only)\n" +
		"\nTotal: 5 results across 2 repos\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

func TestWriteCodeHumanAbbreviatedSkipsNoContentTrailer(t *testing.T) {
	t.Parallel()
	// Repo with only name-matches must not print "+0 content-only".
	repos := []RepoCodeMatches{{
		Repo:        "o/r",
		Count:       2,
		NameMatches: []string{"a", "b"},
		ContentOnly: []string{},
	}}
	var buf bytes.Buffer
	if err := writeCodeHuman(&buf, repos, 0); err != nil { // threshold 0 forces abbreviated
		t.Fatalf("writeCodeHuman: %v", err)
	}
	if strings.Contains(buf.String(), "content-only") {
		t.Errorf("unexpected content-only trailer:\n%s", buf.String())
	}
}

func TestWriteCodeHumanEmpty(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := writeCodeHuman(&buf, nil, 100); err != nil {
		t.Fatalf("writeCodeHuman: %v", err)
	}
	if buf.String() != "No results found.\n" {
		t.Errorf("got %q", buf.String())
	}
}

func TestWriteCodeJSONIncludesContentOnlyEvenWhenAbbreviated(t *testing.T) {
	t.Parallel()
	repos := []RepoCodeMatches{{
		Repo:        "o/r",
		Count:       2,
		NameMatches: []string{"a"},
		ContentOnly: []string{"b"},
	}}
	var buf bytes.Buffer
	if err := writeCodeJSON(&buf, repos); err != nil {
		t.Fatalf("writeCodeJSON: %v", err)
	}
	var got struct {
		Total int               `json:"total"`
		Repos []RepoCodeMatches `json:"repos"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if got.Total != 2 {
		t.Errorf("total = %d", got.Total)
	}
	if len(got.Repos) != 1 || !reflect.DeepEqual(got.Repos[0].ContentOnly, []string{"b"}) {
		t.Errorf("content_only dropped: %+v", got.Repos)
	}
}

func TestWriteCodeJSONEmpty(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	if err := writeCodeJSON(&buf, nil); err != nil {
		t.Fatalf("writeCodeJSON: %v", err)
	}
	var got struct {
		Total int               `json:"total"`
		Repos []RepoCodeMatches `json:"repos"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, buf.String())
	}
	if got.Total != 0 || len(got.Repos) != 0 {
		t.Errorf("expected empty, got %+v", got)
	}
}

// fakeCodeRunner records the per-org search calls and returns canned hits.
type fakeCodeRunner struct {
	t       *testing.T
	mu      sync.Mutex
	orgs    []string
	hits    map[string][]ghSearchCodeItem // org -> items
	calls   [][]string
	wantGH  []string // expected suffix of args after the standard --json,...
}

func (f *fakeCodeRunner) run(_ context.Context, args []string) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	f.mu.Unlock()
	switch {
	case len(args) >= 2 && args[0] == "api" && args[1] == "user/orgs":
		return []byte(strings.Join(f.orgs, "\n") + "\n"), nil
	case len(args) >= 2 && args[0] == "search" && args[1] == "code":
		// Find --owner value.
		var owner string
		for i, a := range args {
			if a == "--owner" && i+1 < len(args) {
				owner = args[i+1]
			}
		}
		// If wantGH is set, verify the tail of args contains it as a contiguous slice.
		if len(f.wantGH) > 0 {
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, strings.Join(f.wantGH, " ")) {
				f.t.Errorf("gh extras missing from args %v; want %v", args, f.wantGH)
			}
		}
		items := f.hits[owner]
		return json.Marshal(items)
	}
	f.t.Fatalf("fakeCodeRunner: unexpected args %v", args)
	return nil, nil
}

func TestRunCodeHumanOutput(t *testing.T) {
	t.Parallel()
	fr := &fakeCodeRunner{
		t:    t,
		orgs: []string{"orgA", "orgB"},
		hits: map[string][]ghSearchCodeItem{
			"orgA": {
				{Path: "src/Unpivot.java", Repository: struct {
					NameWithOwner string `json:"nameWithOwner"`
				}{NameWithOwner: "orgA/repo1"}},
				{Path: "src/Other.java", Repository: struct {
					NameWithOwner string `json:"nameWithOwner"`
				}{NameWithOwner: "orgA/repo1"}},
			},
			"orgB": {
				{Path: "docs/notes.md", Repository: struct {
					NameWithOwner string `json:"nameWithOwner"`
				}{NameWithOwner: "orgB/repo2"}},
			},
		},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithOptions(Options{GHRunner: fr.run},
		[]string{"code", "Unpivot", "--org", "orgA", "--org", "orgB"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	// orgA/repo1 should come first (count 2), orgB/repo2 second (count 1).
	want := "orgA/repo1 (2):\n" +
		"  src/Unpivot.java\n" +
		"  src/Other.java\n" +
		"orgB/repo2 (1):\n" +
		"  docs/notes.md\n" +
		"\nTotal: 3 results across 2 repos\n"
	if stdout.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", stdout.String(), want)
	}
}

func TestRunCodeJSONOutput(t *testing.T) {
	t.Parallel()
	fr := &fakeCodeRunner{
		t:    t,
		orgs: []string{"o"},
		hits: map[string][]ghSearchCodeItem{
			"o": {
				{Path: "src/Match.java", Repository: struct {
					NameWithOwner string `json:"nameWithOwner"`
				}{NameWithOwner: "o/r"}},
			},
		},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithOptions(Options{GHRunner: fr.run},
		[]string{"code", "match", "--org", "o", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	var got struct {
		Total int               `json:"total"`
		Repos []RepoCodeMatches `json:"repos"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, stdout.String())
	}
	if got.Total != 1 || len(got.Repos) != 1 || got.Repos[0].Repo != "o/r" {
		t.Errorf("unexpected JSON: %+v", got)
	}
	if !reflect.DeepEqual(got.Repos[0].NameMatches, []string{"src/Match.java"}) {
		t.Errorf("name_matches: %v", got.Repos[0].NameMatches)
	}
}

func TestRunCodeForwardsGhExtras(t *testing.T) {
	t.Parallel()
	fr := &fakeCodeRunner{
		t:      t,
		orgs:   []string{"o"},
		hits:   map[string][]ghSearchCodeItem{},
		wantGH: []string{"--language", "python"},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithOptions(Options{GHRunner: fr.run},
		[]string{"code", "term", "--org", "o", "--", "--language", "python"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	// Sanity: at least one search-code call was made carrying the extras.
	sawSearch := false
	for _, c := range fr.calls {
		if len(c) >= 2 && c[0] == "search" && c[1] == "code" {
			sawSearch = true
			if !slices.Contains(c, "--language") || !slices.Contains(c, "python") {
				t.Errorf("search call missing gh extras: %v", c)
			}
		}
	}
	if !sawSearch {
		t.Error("no `search code` call recorded")
	}
}

func TestRunCodeNoResults(t *testing.T) {
	t.Parallel()
	fr := &fakeCodeRunner{
		t:    t,
		orgs: []string{"o"},
		hits: map[string][]ghSearchCodeItem{},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithOptions(Options{GHRunner: fr.run},
		[]string{"code", "term", "--org", "o"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if stdout.String() != "No results found.\n" {
		t.Errorf("got %q", stdout.String())
	}
}

func TestRunCodeRejectsBadArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, want string
		args       []string
	}{
		{"no term", "usage:", []string{"code"}},
		{"unknown flag", "unknown flag", []string{"code", "x", "--bogus"}},
		{"threshold missing value", "--threshold requires a value", []string{"code", "x", "--threshold"}},
		{"threshold not int", "is not an integer", []string{"code", "x", "--threshold", "abc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := RunWithOptions(Options{GHRunner: func(context.Context, []string) ([]byte, error) {
				t.Errorf("runner should not be invoked")
				return nil, nil
			}}, tc.args, &stdout, &stderr)
			if code == 0 {
				t.Fatalf("expected nonzero exit, stderr=%q", stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.want) {
				t.Errorf("stderr=%q missing %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestRunCodeHelp(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"code", "--help"}, {"code", "-h"}} {
		var stdout, stderr bytes.Buffer
		code := RunWithOptions(Options{GHRunner: func(context.Context, []string) ([]byte, error) {
			t.Errorf("runner should not be invoked")
			return nil, nil
		}}, args, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("exit=%d stderr=%q", code, stderr.String())
		}
		if !strings.Contains(stdout.String(), "usage: github code") {
			t.Errorf("missing usage in %q", stdout.String())
		}
	}
}

func TestRunCodeAbbreviatesAboveThreshold(t *testing.T) {
	t.Parallel()
	// 4 results across 1 repo; 1 name-match, 3 content-only. Threshold 2 -> abbreviated.
	repo := struct {
		NameWithOwner string `json:"nameWithOwner"`
	}{NameWithOwner: "o/r"}
	fr := &fakeCodeRunner{
		t:    t,
		orgs: []string{"o"},
		hits: map[string][]ghSearchCodeItem{
			"o": {
				{Path: "src/needle.go", Repository: repo},
				{Path: "src/a.go", Repository: repo},
				{Path: "src/b.go", Repository: repo},
				{Path: "src/c.go", Repository: repo},
			},
		},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithOptions(Options{GHRunner: fr.run},
		[]string{"code", "needle", "--org", "o", "--threshold", "2"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "... +3 content-only") {
		t.Errorf("expected abbreviated content-only line, got:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "src/a.go") {
		t.Errorf("content-only paths should be collapsed, got:\n%s", stdout.String())
	}
}

func TestRunCodePropagatesRunnerError(t *testing.T) {
	t.Parallel()
	runner := func(_ context.Context, args []string) ([]byte, error) {
		return nil, fmt.Errorf("gh search exploded")
	}
	var stdout, stderr bytes.Buffer
	code := RunWithOptions(Options{GHRunner: runner},
		[]string{"code", "x", "--org", "o"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected nonzero exit")
	}
	if !strings.Contains(stderr.String(), "gh search exploded") {
		t.Errorf("stderr=%q missing inner error", stderr.String())
	}
}
