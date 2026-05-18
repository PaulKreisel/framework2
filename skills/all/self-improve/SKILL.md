---
name: self-improve
description: Capture a proposed improvement to the framework2 system prompt that started the current session, file it as a follow-up bead, and launch a fresh agent session on it in a new Terminal.app window. Manual invocation only — operator types `/self-improve <idea>`.
disable-model-invocation: true
---

# self-improve

Use this skill when the operator wants to improve the framework2 system
prompt that initiates **this kind of session** (agent or spec). It does
not implement the change in the current session — it files a follow-up
bead capturing the request and opens a new Terminal.app window running
an agent session on that bead.

> Scope: only for editing the framework-shipped prompt templates
> (`internal/agent/defaults/agent-prompt.md` and
> `internal/agent/defaults/spec-prompt.md`) — NOT for editing skills or
> any other code.

## Identify the target prompt file

Use your current mode to pick the framework2 default:

- If you are the **implementer agent** for this ticket (agent mode) →
  `internal/agent/defaults/agent-prompt.md`
- If you are **spec'ing** this ticket (spec mode) →
  `internal/agent/defaults/spec-prompt.md`

The operator may also have a per-database override at
`$BEADS_DIR/agent-prompt.md` or `$BEADS_DIR/spec-prompt.md` (it shadows
the default for `bd` databases that set one). Check whether the override
matching the current mode exists on disk. If it does, mention it in the
bead description as an additional candidate target — but do **not**
investigate which file should actually be edited, and do **not**
recommend updating one over the other. The spawned session decides
whether to touch only the default, only the override, or both (they
overlap).

## Steps

1. Read the operator's improvement idea (the slash command argument).
   Ask one brief clarifying question only if the idea is ambiguous;
   otherwise proceed.

2. Build the bead title and description.

   **Title**: a concise summary, e.g.
   `Improve <agent|spec>-prompt: <short summary>`.

   **Description body** must include:
   - The improvement idea (operator's words, verbatim or lightly
     paraphrased — do not editorialize).
   - The target framework2 file path (repo-relative).
   - If a `$BEADS_DIR/<mode>-prompt.md` override exists, list it as an
     additional candidate target with the note: *"The default and the
     override may overlap — the spawned session decides whether to
     update one or both."*
   - A note that the spawned session should link the framework2
     worktree (`this dir worktree <path-to-framework2.git>`) before
     editing if it isn't already linked.

3. File the request as a non-blocking sibling of the current ticket:

   ```
   this create followup --not-blocked "<title>" --description "<body>"
   ```

   The new bead id is printed on stdout. Capture it.

4. Pre-link the framework2 worktree to the new bead so the spawned
   session starts with code access. Locate the framework2 bare repo
   path:

   - First try `this dir list --json` and pick the entry whose
     `bareRepo` ends in `/framework2.git`.
   - If nothing matches, walk up parents via `bd --json show <id>` /
     `ticket dir list <ancestor-id>` to find one.
   - If still not found, ask the operator for the bare repo path.

   Then link it on the new bead (use `ticket`, not `this`, because the
   marker file in the current cwd points at the current ticket, not the
   new one):

   ```
   ticket dir worktree <new-id> <framework2-bare-repo>
   ```

5. Open a new macOS Terminal.app window running an agent session on the
   new bead. Use osascript exactly like this (the single quoting around
   the `do script` argument keeps the new bead id safe):

   ```
   osascript -e 'tell application "Terminal" to do script "ticket agent <new-id>"' \
            -e 'tell application "Terminal" to activate'
   ```

   The new window inherits the operator's login shell, so `ticket`,
   `BEADS_DIR`, and friends resolve the same way they do in any fresh
   terminal.

6. Report back to the operator: the new bead id, the title, the target
   file(s) listed in the description, and confirmation that a new
   Terminal.app window was opened. Then stop — the implementation
   happens in the spawned session, not here.
