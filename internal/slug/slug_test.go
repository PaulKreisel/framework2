// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package slug_test

import (
	"strings"
	"testing"

	"framework2/internal/slug"
)

func TestDerive(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		in     string
		maxLen int
		want   string
	}{
		{"plain", "Some Title Here", 0, "some-title-here"},
		{"lowercases", "ALL CAPS THING", 0, "all-caps-thing"},
		{"collapses punctuation", "foo!!!  bar,,, baz", 0, "foo-bar-baz"},
		{"strips leading and trailing hyphens", "---hello---", 0, "hello"},
		{"length cap", strings.Repeat("a", 60), 50, strings.Repeat("a", 50)},
		{"length cap trims trailing dash", "abcdefghij-needsmore", 11, "abcdefghij"},
		{"non-ascii falls through", "héllo wörld", 0, "h-llo-w-rld"},
		{"only punctuation yields empty", "!!! ??? ...", 0, ""},
		{"digits survive", "Issue 42 fix", 0, "issue-42-fix"},
		{"default maxLen when zero", strings.Repeat("a", 200), 0, strings.Repeat("a", slug.MaxLen)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := slug.Derive(tc.in, tc.maxLen); got != tc.want {
				t.Errorf("Derive(%q, %d) = %q, want %q", tc.in, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestBranchPrefix(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"bug":         "fix",
		"fix":         "fix",
		"feature":     "enh",
		"feat":        "enh",
		"enhancement": "enh",
		"task":        "todo",
		"chore":       "todo",
		"epic":        "todo",
		"decision":    "todo",
		"":            "todo",
		"  BUG  ":     "fix",
	}
	for in, want := range cases {
		if got := slug.BranchPrefix(in); got != want {
			t.Errorf("BranchPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBranch(t *testing.T) {
	t.Parallel()
	if got := slug.Branch("enh", "ai-42", "the-slug"); got != "enh/ai-42-the-slug" {
		t.Errorf("with slug: got %q", got)
	}
	if got := slug.Branch("todo", "ai-42", ""); got != "todo/ai-42" {
		t.Errorf("empty slug: got %q", got)
	}
}
