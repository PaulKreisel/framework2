# skills/ — convention

Skills under this tree are shipped automatically into every `ticket agent`
and `ticket spec` session by materialising the embedded tree into
`<env>/.claude/skills/<name>/` (see `internal/agent/skills.go`).

## Manual-invocation only

All skills shipped here are intended to be **manually invoked by the
operator** as slash commands — the operator types `/<skill-name>` in the
Claude Code input — never auto-triggered by the model based on
conversational context.

Enforce this in every SKILL.md by setting `disable-model-invocation:
true` in the YAML frontmatter. With that flag Claude Code will not load
the skill on its own initiative; only an explicit `/<name>` from the
operator triggers it.

## Layout

- `agent/<name>/SKILL.md` — loaded only by `ticket agent` sessions.
- `spec/<name>/SKILL.md` — loaded only by `ticket spec` sessions.
- `all/<name>/<files>` — loaded by both.

Skill names must be globally unique across the three buckets;
`WriteSkillsFromFS` refuses to materialise a tree that violates this.
