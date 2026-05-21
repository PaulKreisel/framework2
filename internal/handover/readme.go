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
// semantics explicitly. The text targets a reviewer who has only `bd` +
// `git` + a markdown viewer (no framework2, no `this` CLI).
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
	fmt.Fprintln(w, "No framework2 install, no `this` CLI, and no SQLite access are")
	fmt.Fprintln(w, "required.")
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
	fmt.Fprintln(w, "Importing into your existing workspace is fine — `bd import` is upsert")
	fmt.Fprintln(w, "and safe to re-run. If you'd rather keep the bundle isolated (so it")
	fmt.Fprintln(w, "doesn't mix into your own beads), point `BEADS_DIR` at a fresh")
	fmt.Fprintln(w, "directory first:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "```")
	fmt.Fprintln(w, "export BEADS_DIR=$PWD/.beads-review")
	fmt.Fprintln(w, "BD_NON_INTERACTIVE=1 bd init --prefix review     # one-time")
	fmt.Fprintln(w, "bd import beads.jsonl")
	fmt.Fprintln(w, "```")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Without the `BEADS_DIR` override, plain `bd import beads.jsonl`")
	fmt.Fprintln(w, "merges the bundle's beads into your current workspace. The exported")
	fmt.Fprintln(w, "`metadata.dirs[]` has had host-specific paths stripped, so nothing")
	fmt.Fprintln(w, "will point at directories on the author's machine.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "**Caveat — orphan root.** The subtree root's parent is outside the")
	fmt.Fprintln(w, "bundle, so the `parent-child` link to it is silently dropped on")
	fmt.Fprintln(w, "import. The root will show with no parent breadcrumb in `bd show`;")
	fmt.Fprintln(w, "that is expected and not a sign of data loss. Dependencies between")
	fmt.Fprintln(w, "beads *inside* the bundle round-trip in full.")
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## How to browse the imported beads")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "```")
	fmt.Fprintf(w, "bd show %s              # description, comments, metadata\n", rootID)
	fmt.Fprintf(w, "bd children %s          # tree of descendants\n", rootID)
	fmt.Fprintln(w, "bd list --all             # everything imported (default omits closed)")
	fmt.Fprintf(w, "bd dep list %s          # blockers / blocks\n", rootID)
	fmt.Fprintln(w, "```")
	fmt.Fprintln(w)

	fmt.Fprintln(w, "## How to inspect the code")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Code is NOT included in this bundle — fetch it from the project's")
	fmt.Fprintln(w, "normal git remote. The top-level `commits.md` lists every commit")
	fmt.Fprintln(w, "across the subtree in chronological order; the `Ticket` column on")
	fmt.Fprintln(w, "each row says which child bead the commit was authored against,")
	fmt.Fprintln(w, "even after a parent FF-rebase has moved it onto the parent's branch.")
	fmt.Fprintln(w, "Branch names per bead are stored under `metadata.dirs[].branch` in")
	fmt.Fprintln(w, "`beads.jsonl`.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Per-commit agent attribution lives in the `refs/notes/agent-session`")
	fmt.Fprintln(w, "notes ref on the remote. Fetch it once, then view with `git log`:")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "```")
	fmt.Fprintln(w, "git clone <project-remote> <local>")
	fmt.Fprintln(w, "cd <local>")
	fmt.Fprintln(w, "git fetch origin refs/notes/agent-session:refs/notes/agent-session")
	fmt.Fprintln(w, "git fetch origin <branch>          # one of the branches from commits.md")
	fmt.Fprintln(w, "git log --notes=agent-session origin/<branch>")
	fmt.Fprintln(w, "git show <sha>                       # full diff for any row in commits.md")
	fmt.Fprintln(w, "```")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The `Ticket` column in `commits.md` matches the `ticketId` field")
	fmt.Fprintln(w, "of the JSON payload on each note, so a commit row in `commits.md`")
	fmt.Fprintln(w, "and the corresponding `git log --notes=agent-session` entry agree.")
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
