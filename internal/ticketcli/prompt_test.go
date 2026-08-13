// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package ticketcli_test

import (
	"strings"
	"testing"

	"framework2/internal/agent"
	"framework2/internal/testutil"
)

func TestTicketAgentPrintDefaultPrompt(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	out, errOut, code := testutil.RunTicketIn(t, beadsDir, "agent", "--print-default-prompt")
	if code != 0 {
		t.Fatalf("ticket agent --print-default-prompt: exit=%d stderr=%q", code, errOut)
	}
	if out != agent.DefaultPromptTemplate(agent.ModeAgent) {
		t.Errorf("stdout did not match the registered default agent template:\n--- got ---\n%q\n--- want ---\n%q",
			out, agent.DefaultPromptTemplate(agent.ModeAgent))
	}
}

func TestTicketSpecPrintDefaultPrompt(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	out, errOut, code := testutil.RunTicketIn(t, beadsDir, "spec", "--print-default-prompt")
	if code != 0 {
		t.Fatalf("ticket spec --print-default-prompt: exit=%d stderr=%q", code, errOut)
	}
	if out != agent.DefaultPromptTemplate(agent.ModeSpec) {
		t.Errorf("stdout did not match the registered default spec template:\n--- got ---\n%q\n--- want ---\n%q",
			out, agent.DefaultPromptTemplate(agent.ModeSpec))
	}
}

func TestTicketAgentListPromptFragments(t *testing.T) {
	t.Parallel()
	beadsDir := testutil.TestDB(t)
	out, errOut, code := testutil.RunTicketIn(t, beadsDir, "agent", "--list-prompt-fragments")
	if code != 0 {
		t.Fatalf("ticket agent --list-prompt-fragments: exit=%d stderr=%q", code, errOut)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	want := agent.FragmentNames()
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d; out=%q", len(lines), len(want), out)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}
