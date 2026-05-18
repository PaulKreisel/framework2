# skills/ — convention

Skills under this tree are shipped automatically into every `ticket agent`
and `ticket spec` session by materialising the embedded tree into
`<env>/.claude/skills/<name>/` (see `internal/agent/skills.go`).

## Manual-invocation only

All skills shipped here are intended to be **manually invoked by the
operator** (slash-command style — `/<skill-name>`), never auto-triggered
by the model based on conversational context.

This isn't enforced by Claude Code — the `description:` frontmatter is
what Claude reads to decide whether to auto-invoke. Authors of new
skills here should phrase that field so it reads as "Use when the user
asks to ..." rather than "Use when ..." — i.e. predicated on an explicit
operator request, not on inferred intent.

## Layout

- `agent/<name>/SKILL.md` — loaded only by `ticket agent` sessions.
- `spec/<name>/SKILL.md` — loaded only by `ticket spec` sessions.
- `all/<name>/<files>` — loaded by both.

Skill names must be globally unique across the three buckets;
`WriteSkillsFromFS` refuses to materialise a tree that violates this.
