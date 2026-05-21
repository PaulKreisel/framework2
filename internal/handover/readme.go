package handover

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// writeReadme produces the reviewer-facing README.md. It lays out the
// bundle's contents, gives the import recipe, and states the snapshot
// semantics explicitly.
func writeReadme(dirPath, rootID string, root *Bead, order []string, beads map[string]*Bead) error {
	f, err := os.Create(filepath.Join(dirPath, "README.md"))
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	defer w.Flush()

	title := "(untitled)"
	if root != nil && root.Title != "" {
		title = root.Title
	}
	now := time.Now().UTC().Format(time.RFC3339)

	fmt.Fprintf(w, "# Handover · %s\n\n", rootID)
	fmt.Fprintf(w, "**Ticket:** %s — %s\n\n", rootID, title)
	fmt.Fprintf(w, "**Subtree size:** %d bead(s)\n\n", len(order))
	fmt.Fprintf(w, "**Snapshot taken:** %s\n\n", now)

	fmt.Fprintln(w, "## What this is")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "A point-in-time, read-only package describing one ticket and its")
	fmt.Fprintln(w, "descendant subtree. It carries the bead store, agent conversation")
	fmt.Fprintln(w, "history, and per-commit attribution — enough for a reviewer with")
	fmt.Fprintln(w, "`bd` plus a markdown viewer to follow the work.")
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## How to read it")
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Start at [tickets/%s/conversation.md](tickets/%s/conversation.md);\n",
		rootID, rootID)
	fmt.Fprintln(w, "every bead-id reference inside is a relative link to that ticket's")
	fmt.Fprintln(w, "conversation, so you can walk the subtree by following the links.")
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## How to import the beads")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "```")
	fmt.Fprintln(w, "bd import beads.jsonl")
	fmt.Fprintln(w, "```")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Upsert semantics; safe to re-run. The exported metadata.dirs[] has")
	fmt.Fprintln(w, "had host-specific paths stripped, so the import will not point at")
	fmt.Fprintln(w, "any directories on the author's machine.")
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## How to inspect the code")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Code is NOT included in this bundle. Fetch the branches listed in")
	fmt.Fprintln(w, "each `tickets/<id>/commits.md` from the project's normal git remote.")
	fmt.Fprintln(w, "Per-commit agent attribution is in the `Session` column there; the")
	fmt.Fprintln(w, "same data lives in `refs/notes/agent-session` on the remote, which")
	fmt.Fprintln(w, "you can fetch with:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "```")
	fmt.Fprintln(w, "git fetch origin refs/notes/agent-session:refs/notes/agent-session")
	fmt.Fprintln(w, "git log --notes=agent-session <branch>")
	fmt.Fprintln(w, "```")
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## Snapshot semantics")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The bundle reflects state at the snapshot timestamp above. After")
	fmt.Fprintln(w, "this point the author may run `this finish`, which FF-rebases the")
	fmt.Fprintln(w, "ticket's branches onto its parent — the SHAs and notes referenced")
	fmt.Fprintln(w, "in this bundle remain valid against the remote even after rebase.")
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## What's NOT in the bundle")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "- Code (no git bundles; fetch from the project's remote).")
	fmt.Fprintln(w, "- Raw Claude transcripts (only the structured `metadata.agents[].conversation[]` view).")
	fmt.Fprintln(w, "- Host-specific paths.")
	fmt.Fprintln(w, "- Push targets and two-way comments (covered by separate epics).")
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## Subtree contents")
	fmt.Fprintln(w)
	for _, id := range order {
		b := beads[id]
		t := "(untitled)"
		if b.Title != "" {
			t = b.Title
		}
		fmt.Fprintf(w, "- [%s](tickets/%s/conversation.md) — %s\n", id, id, t)
	}
	return nil
}
