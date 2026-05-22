package finish

import "testing"

// TestRewriteSubtaskTrailerAppend covers the no-existing-trailer case: a
// blank line plus "Subtask N" is appended to the message body.
func TestRewriteSubtaskTrailerAppend(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, want string
	}{
		{
			name: "single-line message",
			in:   "feat: do thing\n",
			want: "feat: do thing\n\nSubtask 7\n",
		},
		{
			name: "multi-line body",
			in:   "feat: do thing\n\nbody line.\n",
			want: "feat: do thing\n\nbody line.\n\nSubtask 7\n",
		},
		{
			name: "trailing blanks tolerated",
			in:   "feat: do thing\n\n\n",
			want: "feat: do thing\n\nSubtask 7\n",
		},
		{
			name: "Subtask mention in body (not last line) is left alone",
			in:   "feat: do thing\n\nMentions Subtask 42 in passing.\nFinal line.\n",
			want: "feat: do thing\n\nMentions Subtask 42 in passing.\nFinal line.\n\nSubtask 7\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rewriteSubtaskTrailer(tc.in, 7, false)
			if got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestRewriteSubtaskTrailerPrepend covers the existing-trailer case: the
// regex-matching last line is replaced with "Subtask N.<existing-numbers>".
func TestRewriteSubtaskTrailerPrepend(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, want string
		index          int
	}{
		{
			name:  "single component trailer",
			in:    "feat: do thing\n\nSubtask 4\n",
			index: 7,
			want:  "feat: do thing\n\nSubtask 7.4\n",
		},
		{
			name:  "dotted trailer",
			in:    "feat: do thing\n\nSubtask 4.2\n",
			index: 7,
			want:  "feat: do thing\n\nSubtask 7.4.2\n",
		},
		{
			name:  "deep dotted trailer",
			in:    "feat: do thing\n\nSubtask 1.2.3.4\n",
			index: 9,
			want:  "feat: do thing\n\nSubtask 9.1.2.3.4\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rewriteSubtaskTrailer(tc.in, tc.index, false)
			if got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestRewriteSubtaskTrailerIdempotent: re-applying the same index to a
// message that already has "Subtask N.…" with idempotent=true must not
// double-prepend. This is what protects a resume-after-partial-failure
// from corrupting messages.
func TestRewriteSubtaskTrailerIdempotent(t *testing.T) {
	t.Parallel()
	in := "feat: do thing\n\nSubtask 7.4.2\n"
	got := rewriteSubtaskTrailer(in, 7, true)
	if got != in {
		t.Errorf("idempotent rewrite changed message:\ngot  %q\nwant %q", got, in)
	}
}

// TestRewriteSubtaskTrailerCascadeApplies: in cascade mode (idempotent=false)
// a commit that already carries "Subtask M" must get the new N prepended
// even when M == N — the two indices are independent counters at different
// layers and a coincidence between them must not be misread as a duplicate
// run.
func TestRewriteSubtaskTrailerCascadeApplies(t *testing.T) {
	t.Parallel()
	in := "feat: do thing\n\nSubtask 1\n"
	got := rewriteSubtaskTrailer(in, 1, false)
	want := "feat: do thing\n\nSubtask 1.1\n"
	if got != want {
		t.Errorf("cascade rewrite:\ngot  %q\nwant %q", got, want)
	}
}

// TestRewriteSubtaskTrailerNotATrailer: a line that's close to but not
// matching the strict regex (extra text, lowercase, punctuation) is treated
// as body text — the new trailer is appended instead of replacing it.
func TestRewriteSubtaskTrailerNotATrailer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, want string
	}{
		{
			name: "trailing punctuation",
			in:   "feat: do thing\n\nSubtask 4.\n",
			want: "feat: do thing\n\nSubtask 4.\n\nSubtask 7\n",
		},
		{
			name: "subtask lowercase",
			in:   "feat: do thing\n\nsubtask 4\n",
			want: "feat: do thing\n\nsubtask 4\n\nSubtask 7\n",
		},
		{
			name: "trailer followed by extra text on same line",
			in:   "feat: do thing\n\nSubtask 4 (followup)\n",
			want: "feat: do thing\n\nSubtask 4 (followup)\n\nSubtask 7\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rewriteSubtaskTrailer(tc.in, 7, false)
			if got != tc.want {
				t.Errorf("got %q\nwant %q", got, tc.want)
			}
		})
	}
}
