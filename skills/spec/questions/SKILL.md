---
name: questions
description: Drive a structured Q&A pass on the current ticket's description.md to surface ambiguities, missing constraints, and implicit assumptions, then write the answers back into description.md and sync. Manual invocation only — operator types `/questions`.
disable-model-invocation: true
---

# questions

Use this skill when the operator wants a Q&A-style refinement pass on
the current ticket's `description.md`. The goal is to surface and
resolve every design / requirements decision that would otherwise block
an implementer working from the resulting description.

## Scope

- Spec mode only. **No source-file edits.**
- The sole artifact of the session is an updated `description.md`,
  synced back via `this env syncback`.
- Out of scope: creating child beads, filing follow-ups, or any other
  bead mutations. If the operator's answers reveal work that belongs in
  a separate bead, note it in `description.md` and let the operator
  decide what to do with it — do not file it here.

## Steps

1. Read `description.md` in the ticket env folder. Identify unclear
   points, missing constraints, and implicit assumptions an implementer
   would have to guess at.

2. Optionally consult the linked codebase / worktree(s) when doing so
   helps form better questions or avoids asking the operator something
   the code already answers. This is optional — whether it's worthwhile
   depends on the task.

3. Ask clarifying questions until you judge no further design /
   requirements questions would block an implementer:

   - **Batching.** Default to asking independent questions together in
     one turn. Ask sequentially when later questions genuinely depend on
     earlier answers. Number per turn is whatever fits — no fixed batch
     size.
   - **Form.** Use `AskUserQuestion` when there is a clean finite set of
     options. Otherwise ask open-ended questions in plain text.
   - **Always propose a recommended answer.** For every question,
     include your recommendation and a brief argument for it. Where
     useful, argue against the main alternatives too. The operator is
     reviewing a draft, not filling in a blank.
   - **Volume.** Whatever is minimally needed to unblock an implementer.
     1 question is fine; 20 is fine. Don't pad and don't truncate.

4. When you judge the session done — i.e. no further question would
   block an implementer — finish the pass:

   1. Edit `description.md` so that every requirement / decision
      derived from the operator's answers is reflected there. The
      updated file should read as a self-contained spec; an implementer
      should not need the Q&A transcript to understand it.
   2. Run `this env syncback` to push the updated description back to
      the bead.

5. Report briefly what changed in `description.md` and confirm syncback
   succeeded.

## Notes

- "Done" is your call, not the operator's. If the operator interjects
  mid-session with new direction, fold it in and continue; otherwise
  declare done when the description is implementer-ready.
- Do **not** call `this finish` from this skill. `/questions` refines a
  spec; closing the ticket is a separate decision.
