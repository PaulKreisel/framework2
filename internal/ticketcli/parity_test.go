// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package ticketcli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"framework2/internal/env"
	"framework2/internal/testutil"
)

// TestSubcommandFormParity is the drift guard between `ticket` and `this`.
//
// For each id-consuming ticket subcommand, three invocation forms must
// produce identical output:
//
//  1. ticket <sub> <id> [args...]              # positional id
//  2. ticket --ticket <id> <sub> [args...]     # global flag id
//  3. this <sub> [args...]                     # id resolved from .ticket-id
//
// When you add a new id-consuming subcommand to ticketcli, add a row here.
// If you forget, this test will not catch the *new* subcommand — but it will
// catch any regression in the global-flag plumbing that all existing
// subcommands depend on. Treat the table below as part of the dispatcher
// contract.
func TestSubcommandFormParity(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)

	cases := []struct {
		name string
		// subArgs is the subcommand args after the id slot.
		// e.g. for `dir list <id>` it is {"dir", "list"} and the id is
		// appended in form 1; for `dir link <id> <path>` it is
		// {"dir", "link"} and the args become {..., <id>, <path>}.
		sub     []string
		tail    []string // additional positional args after the id
		wantOut func(t *testing.T, out string)
	}{
		{
			name: "dir list",
			sub:  []string{"dir", "list"},
			wantOut: func(t *testing.T, out string) {
				if !strings.Contains(out, "[]") {
					t.Errorf("expected empty list output, got %q", out)
				}
			},
		},
		{
			name: "env",
			sub:  []string{"env"},
			wantOut: func(t *testing.T, out string) {
				if strings.TrimSpace(out) == "" {
					t.Error("expected env path on stdout, got empty")
				}
			},
		},
		// `next` was previously exercised here, but its output goes through
		// `bd ready`, which non-deterministically appends a "Tip: Install the
		// beads plugin..." line — making the form-vs-form comparison flake.
		// Parity for the global-flag plumbing is already covered by the
		// rows above; readd a `next` row only after the tip can be silenced.
		// `next agent` is fine to include — it doesn't go through bd ready.
		{
			name: "next agent",
			sub:  []string{"next", "agent"},
			wantOut: func(t *testing.T, out string) {
				// No descendants, no waiting agents — empty output.
				if strings.TrimSpace(out) != "" {
					t.Errorf("expected empty output, got %q", out)
				}
			},
		},
		{
			name: "ready",
			sub:  []string{"ready"},
			wantOut: func(t *testing.T, out string) {
				// `bd label add` echoes "✓ Added label ..." to stderr only;
				// stdout stays empty.
				if strings.TrimSpace(out) != "" {
					t.Errorf("expected empty stdout, got %q", out)
				}
			},
		},
		{
			name: "notready",
			sub:  []string{"notready"},
			wantOut: func(t *testing.T, out string) {
				if strings.TrimSpace(out) != "" {
					t.Errorf("expected empty stdout, got %q", out)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ticket := testutil.CreateBead(t, beadsDir, "parity-"+strings.ReplaceAll(tc.name, " ", "-"))
			t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(beadsDir, "envs", ticket)) })

			form1 := buildArgs(tc.sub, ticket, tc.tail, "")
			form2 := buildArgs(tc.sub, "", tc.tail, ticket)
			out1, err1, code1 := testutil.RunTicketIn(t, beadsDir, form1...)
			if code1 != 0 {
				t.Fatalf("form 1 (positional) failed: exit=%d stderr=%q args=%v", code1, err1, form1)
			}
			out2, err2, code2 := testutil.RunTicketIn(t, beadsDir, form2...)
			if code2 != 0 {
				t.Fatalf("form 2 (--ticket flag) failed: exit=%d stderr=%q args=%v", code2, err2, form2)
			}

			thisArgs := append([]string{}, tc.sub...)
			thisArgs = append(thisArgs, tc.tail...)

			// For form 3 we need an env folder with the .ticket-id marker.
			envPath, cerr := env.Create("", beadsDir, ticket)
			if cerr != nil {
				t.Fatalf("env.Create for form 3: %v", cerr)
			}
			out3, err3, code3 := testutil.RunThisFromIn(t, beadsDir, envPath, thisArgs...)
			if code3 != 0 {
				t.Fatalf("form 3 (this) failed: exit=%d stderr=%q args=%v", code3, err3, thisArgs)
			}

			tc.wantOut(t, out1)
			if out1 != out2 {
				t.Errorf("form 1 vs 2 stdout drift:\n form1 = %q\n form2 = %q", out1, out2)
			}
			if out2 != out3 {
				t.Errorf("form 2 vs 3 stdout drift:\n form2 = %q\n form3 = %q", out2, out3)
			}
		})
	}
}

// buildArgs assembles ticket-command args. When ticketFlag is set, prepends
// `--ticket <ticketFlag>` globally and omits the id positional. When
// positionalID is set, places the id in the per-subcommand positional slot.
// Exactly one of ticketFlag/positionalID must be set.
func buildArgs(sub []string, positionalID string, tail []string, ticketFlag string) []string {
	var out []string
	if ticketFlag != "" {
		out = append(out, "--ticket", ticketFlag)
	}
	out = append(out, sub...)
	if positionalID != "" {
		out = append(out, positionalID)
	}
	out = append(out, tail...)
	return out
}
