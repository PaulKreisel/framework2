# framework2

A Go toolkit built around [beads](https://github.com/) as the source of truth for tasks, worktrees, and agent sessions.

Per-task specifications live in beads in `~/ai/.beads/` — start with `bd ready`.

Agents working in this repo: see [CLAUDE.md](CLAUDE.md) for the working rules.

## Layout

```
cmd/         one directory per binary
internal/    shared internals (added as needed)
```

For per-command usage, run the binary with `--help` — this README intentionally does not duplicate it.

## Customizing the agent system prompt

`ticket agent` and `ticket spec` append a mode-specific prompt to claude on
launch. Operators can override that prompt per-`.beads`-folder by dropping a
Markdown file in `$BEADS_DIR`:

- `agent-prompt.md` — coding-mode template (used by `ticket agent`)
- `spec-prompt.md` — spec-mode template (used by `ticket spec`)

The file's contents *are* the appended prompt. Framework-maintained chunks
are pulled in via `{{fragment_name}}` placeholders — Go `text/template`
semantics, so operator-supplied Go template directives also work. The
fragment set is intentionally minimal: operators own the prose and headings
(free to reword), while the framework owns the command syntax inside them
(so e.g. `this create followup ...` stays in sync with the implementation)
and the data-backed repos block.

When no operator file is present, a built-in default template is used.

```
ticket agent --print-default-prompt     # dump the agent-mode default
ticket spec  --print-default-prompt     # dump the spec-mode default
ticket agent --list-prompt-fragments    # list registered fragment names
```

Unknown fragment names, parse errors, and unreadable files fail loudly at
agent launch — there is no silent fallback to the default once an operator
file is present.

## Build

```
make install
make test
```
