package handover

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"framework2/internal/agent"
)

// writeConversationMD renders one bead's conversation.md into
// <dirPath>/tickets/<id>/conversation.md, per the schema in ai-vrc.57.2.
func writeConversationMD(dirPath string, b *Bead, beads map[string]*Bead) error {
	out := filepath.Join(dirPath, "tickets", b.ID, "conversation.md")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	title := b.Title
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(w, "# %s · %s\n\n", b.ID, title)
	fmt.Fprintf(w, "Status: %s · Owner: %s · Created: %s · Updated: %s\n\n",
		nz(b.Status), nz(b.Owner), nz(b.Created), nz(b.Updated))

	fmt.Fprintf(w, "> Parent: %s\n", parentBreadcrumb(b, beads))
	fmt.Fprintf(w, "> Children: %s\n\n", childrenBreadcrumb(b, beads))

	if desc := beadDescription(b); desc != "" {
		fmt.Fprintln(w, "## Description")
		fmt.Fprintln(w)
		fmt.Fprintln(w, linkBeadIDs(desc, beads))
		fmt.Fprintln(w)
	}

	agents := beadAgents(b)
	if len(agents) == 0 {
		fmt.Fprintln(w, "## Sessions")
		fmt.Fprintln(w)
		fmt.Fprintln(w, "_No sessions recorded._")
		return nil
	}
	fmt.Fprintln(w, "## Sessions")
	fmt.Fprintln(w)
	for i, a := range agents {
		writeSession(w, i+1, a, beads)
	}
	return nil
}

func writeSession(w *bufio.Writer, idx int, a agent.AgentEntry, beads map[string]*Bead) {
	mode := a.Mode
	if mode == "" {
		mode = "agent"
	}
	fmt.Fprintf(w, "### Session %d · %s · session `%s` · started %s\n\n",
		idx, mode, nz(a.SessionID), nz(a.StartedAt))
	if len(a.Conversation) == 0 {
		fmt.Fprintln(w, "_No conversation entries._")
		fmt.Fprintln(w)
		return
	}
	for _, c := range a.Conversation {
		writeEntry(w, c, beads)
	}
}

func writeEntry(w *bufio.Writer, c agent.ConvEntry, beads map[string]*Bead) {
	header := "**" + c.Type + "**"
	if c.ContextTokens > 0 {
		header += fmt.Sprintf("  _(ctx %s)_", agent.FormatContextTokens(c.ContextTokens))
	}
	fmt.Fprintln(w, header)
	fmt.Fprintln(w)
	switch c.Type {
	case agent.ConvTypeUser:
		fmt.Fprintln(w, blockquote(linkBeadIDs(c.Prompt, beads)))
	case agent.ConvTypeAssistantComment, agent.ConvTypeAssistantReply:
		fmt.Fprintln(w, linkBeadIDs(c.Text, beads))
	case agent.ConvTypeAskUserQuestion:
		writeAskUserQuestion(w, c.Input, beads)
	case agent.ConvTypeExitPlanMode:
		fmt.Fprintln(w, "```markdown")
		fmt.Fprintln(w, strings.TrimRight(c.Plan, "\n"))
		fmt.Fprintln(w, "```")
	case agent.ConvTypeStopFailure:
		fmt.Fprintf(w, "**StopFailure:** %s\n", nz(c.Matcher))
		if c.Message != "" {
			fmt.Fprintln(w)
			fmt.Fprintln(w, "```")
			fmt.Fprintln(w, strings.TrimRight(c.Message, "\n"))
			fmt.Fprintln(w, "```")
		}
	default:
		// Lossless fallback: dump the entry as fenced JSON so schema drift
		// doesn't lose information.
		raw, _ := json.MarshalIndent(c, "", "  ")
		fmt.Fprintln(w, "```json")
		fmt.Fprintln(w, string(raw))
		fmt.Fprintln(w, "```")
	}
	fmt.Fprintln(w)
}

// askUserPayload is the subset of the AskUserQuestion tool_input we render.
// We deliberately preserve unknown fields by keeping the rest in extra so
// the schema-drift fallback never trips when the tool gains new options.
type askUserPayload struct {
	Questions []struct {
		Question    string `json:"question"`
		Header      string `json:"header"`
		MultiSelect bool   `json:"multiSelect"`
		Options     []struct {
			Label       string `json:"label"`
			Description string `json:"description"`
		} `json:"options"`
	} `json:"questions"`
	Answers map[string]string `json:"answers"`
}

func writeAskUserQuestion(w *bufio.Writer, input json.RawMessage, beads map[string]*Bead) {
	if len(input) == 0 || string(input) == "null" {
		fmt.Fprintln(w, "_(no question payload)_")
		return
	}
	var p askUserPayload
	if err := json.Unmarshal(input, &p); err != nil || len(p.Questions) == 0 {
		// Fallback: show the raw JSON so we don't lose anything.
		fmt.Fprintln(w, "```json")
		fmt.Fprintln(w, string(input))
		fmt.Fprintln(w, "```")
		return
	}
	for _, q := range p.Questions {
		fmt.Fprintf(w, "**Q:** %s\n\n", linkBeadIDs(q.Question, beads))
		for _, o := range q.Options {
			fmt.Fprintf(w, "- **%s** — %s\n", o.Label, linkBeadIDs(o.Description, beads))
		}
		if ans, ok := p.Answers[q.Question]; ok && ans != "" {
			fmt.Fprintf(w, "\n**A:** %s\n", linkBeadIDs(ans, beads))
		}
		fmt.Fprintln(w)
	}
}

func beadDescription(b *Bead) string {
	d, _ := b.Raw["description"].(string)
	return d
}

// beadAgents decodes metadata.agents[] from the raw export row through
// agent.AgentEntry — same shape used by the live system, so the renderer
// stays schema-aligned with the rest of the codebase.
func beadAgents(b *Bead) []agent.AgentEntry {
	md, ok := b.Raw["metadata"].(map[string]any)
	if !ok {
		return nil
	}
	rawAgents, ok := md["agents"]
	if !ok || rawAgents == nil {
		return nil
	}
	buf, err := json.Marshal(rawAgents)
	if err != nil {
		return nil
	}
	var out []agent.AgentEntry
	if err := json.Unmarshal(buf, &out); err != nil {
		return nil
	}
	return out
}

func parentBreadcrumb(b *Bead, beads map[string]*Bead) string {
	if b.ParentID == "" {
		return "none"
	}
	if _, in := beads[b.ParentID]; in {
		return fmt.Sprintf("[%s](../%s/conversation.md)", b.ParentID, b.ParentID)
	}
	return b.ParentID
}

func childrenBreadcrumb(b *Bead, beads map[string]*Bead) string {
	if len(b.Children) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(b.Children))
	for _, c := range b.Children {
		if _, in := beads[c]; in {
			parts = append(parts, fmt.Sprintf("[%s](../%s/conversation.md)", c, c))
		} else {
			parts = append(parts, c)
		}
	}
	return strings.Join(parts, ", ")
}

// beadIDRe matches `ai-XYZ.123.45` style ids. Word boundaries on both ends
// keep it from chewing into surrounding punctuation; the prefix is lowercase
// letters because every bd setup we run uses that shape.
var beadIDRe = regexp.MustCompile(`\b([a-z][a-z0-9]+(?:\.[0-9]+)+)\b`)

// linkBeadIDs wraps every bd id in s with a relative markdown link to
// that ticket's conversation.md, suitable for a sibling under
// tickets/<id>/ (uses `../<id>/conversation.md`).
func linkBeadIDs(s string, beads map[string]*Bead) string {
	return linkBeadIDsWithPrefix(s, beads, "../")
}

// linkBeadIDsFromRoot wraps every bd id in s for a file at the bundle
// root (uses `tickets/<id>/conversation.md`).
func linkBeadIDsFromRoot(s string, beads map[string]*Bead) string {
	return linkBeadIDsWithPrefix(s, beads, "tickets/")
}

// linkBeadIDsWithPrefix wraps every bd id in s with a relative markdown
// link to that ticket's conversation.md, if the id is in the subtree.
// Plain occurrences of ids outside the subtree are left as-is. We
// deliberately don't touch occurrences inside fenced code blocks to keep
// diff hunks readable — that trade-off avoids a heavier markdown-aware
// walker.
func linkBeadIDsWithPrefix(s string, beads map[string]*Bead, prefix string) string {
	if s == "" || len(beads) == 0 {
		return s
	}
	// Split on fenced code blocks (```...```), only rewrite outside them.
	parts := strings.Split(s, "```")
	for i := range parts {
		if i%2 == 1 {
			continue // inside a fenced block
		}
		parts[i] = beadIDRe.ReplaceAllStringFunc(parts[i], func(m string) string {
			if _, ok := beads[m]; ok {
				return fmt.Sprintf("[%s](%s%s/conversation.md)", m, prefix, m)
			}
			return m
		})
	}
	return strings.Join(parts, "```")
}

func blockquote(s string) string {
	if s == "" {
		return ">"
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

func nz(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}
