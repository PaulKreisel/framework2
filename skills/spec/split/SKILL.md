---
name: split
description: Use this skill when the user wants to break the current ticket down into child beads. Helps propose a set of vertical, tracer-bullet slices (not horizontal layers), confirm with the user, then create the children. Manual invocation only — operator types `/split`.
disable-model-invocation: true
---

# split

Use this skill when the operator wants to split the current ticket into
child beads during a `ticket spec` session.

## Scope

- Spec mode only. **No source-file edits.** This skill mutates beads
  (creating children, wiring blockers) and optionally `description.md`
  — never code.
- Outcome of the session: a set of child beads (and optional blocker
  edges) representing the agreed split, plus an optional updated
  `description.md`.

## Core principle: vertical slices (tracer bullets)

Each proposed child must be a **thin end-to-end slice** that delivers a
working increment of value — cutting through all the layers needed for
that increment — **not** a horizontal layer of work ("do the DB", "do
the UI", "write the tests").

- Frame each proposal as a user-visible / testable outcome, not "layer
  X of feature Y".
- Reject or rewrite candidates that are purely a layer with no demoable
  end state.
- Aim for the smallest first slice that proves the architecture
  end-to-end (a "walking skeleton"), then layer richer slices on top.

If a candidate child can only be described as "the backend part" or
"the frontend part" of something, it's the wrong shape — combine it
with the rest of its slice or drop it.

## Steps

1. **Re-read `description.md`** in the ticket env folder to ground the
   split in the actual scope. Do not split based on assumed scope.

2. **Check for existing children.** Run `bd children <ticket-id>` (the
   current ticket id is in `.ticket-id`). If children already exist,
   list them to the operator and ask whether to **extend**, **edit**,
   or **replace** them — do not blindly add more on top.

   - Prefer editing or deleting stale prior-pass children over leaving
     orphans (`bd edit <id>` / `bd delete <id>`).

3. **Propose a candidate set of vertical slices.** For each:
   - A short **title**.
   - A **one-line description** of the slice.
   - A note on **what makes it a complete tracer bullet** — what is
     demoable / testable at the end of it.

   Present the set as a numbered list so the operator can edit, drop,
   reorder, or add slices easily.

4. **Confirm with the operator.** Iterate on the proposal until they
   approve. Do not create any beads before approval.

5. **Create the agreed children.** For each approved slice:

   ```
   this create child "<title>" --description "<body>"
   ```

   `<body>` should be a self-contained mini-spec for the slice: what
   the slice delivers, what makes it end-to-end, and any constraints
   inherited from the parent that the implementer needs.

6. **Wire up blockers if there's a natural order.** If slice B
   genuinely depends on slice A landing first, add the edge:

   ```
   bd dep add <B-id> <A-id>
   ```

   Only wire dependencies that are real ordering constraints, not just
   preferred order — over-wiring blocks parallel work.

7. **Offer to update `description.md`** if the split exposed missing
   or wrong content in the parent's spec. Do not silently rewrite it
   — describe the proposed edit, get approval, then edit and run
   `this env syncback`.

8. **Report the created bead ids** back to the operator.

## Notes

- Do **not** call `this finish` from this skill. `/split` plans and
  files children; closing the parent is a separate decision.
- If the operator says mid-flight "actually this shouldn't be split",
  abandon cleanly without creating anything.
