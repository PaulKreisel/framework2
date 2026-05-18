---
name: new-skill
description: Use only when the user explicitly asks to add or propose a new Claude Code skill for framework2 agent / spec sessions (manual invocation — do not auto-trigger). Files the request as a child bead via `ticket create child` after asking which agent(s) it targets.
---

# new-skill

Use this skill when the user proposes adding a new Claude Code skill that
should ship with `ticket agent` and/or `ticket spec`.

The skill is a request-capture flow: ask a few questions, then file a
child bead so the work is tracked. Do not implement the new skill in this
session — only record it.

## Steps

1. Find out what the new skill should do. Ask the user for:
   - A short kebab-case **name** for the skill.
   - A one-line **description** (the `description:` value in the SKILL.md
     frontmatter — used by Claude to decide when to invoke it).
   - The **behaviour**: what the skill should make the agent do when
     triggered. A rough sketch is fine.

2. Ask which **target bucket** the skill belongs in. Exactly one of:
   - `agent` — only loaded in `ticket agent` (coding) sessions.
   - `spec` — only loaded in `ticket spec` (spec'ing) sessions.
   - `all` — loaded in both.

   If the user is unsure, offer the choices and let them pick.

3. File the request as a child of the current ticket:

   ```
   ticket create child "<title>" --description "<body>"
   ```

   - `<title>` should be a concise summary, e.g. `Add new-skill skill: <name>`.
   - `<body>` should include:
     - Target bucket (`agent` / `spec` / `all`).
     - Proposed skill name.
     - The `description:` line for its frontmatter.
     - Notes on the intended behaviour / steps.

4. Report the new bead id back to the user.
