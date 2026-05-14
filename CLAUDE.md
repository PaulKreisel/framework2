# framework2 — for agents

Read [initial_prompt.md](initial_prompt.md) before starting work in this repo. It captures the design decisions and the reasoning behind them; the per-task *specifications* live in beads.

## Hard rules

- **Beads are the source of truth.** Every TODO is a bead in `~/ai/.beads/`. Do not maintain `TODO.md` files or in-conversation task lists for persistent work.
- **Talk to `bd` via the CLI, with `--json` for parsing.** Do not import bd as a Go library.
- **Go for everything new.** No new shell scripts.
- **Tests are realistic.** Use a real `bd` and real git repos in tmpdirs. No mocks. See [TESTING.md](TESTING.md) for the test framework and patterns.

## Working with beads

| Action | Command |
|---|---|
| What can I work on? | `bd ready` |
| Read a bead | `bd show <id>` |
| Mark a bead done | `bd close <id>` |
| New bead | `bd create "Title" -t task -p N` |
| Add dependency | `bd dep add <child> <parent>` |
| Follow-up discovered mid-task | `bd dep add <new> <current> --type discovered-from` |

## Where things live

- Source: `~/ai/framework2/`
- bd DB (auto-resolved via cwd walk-up): `~/ai/.beads/`
- Binaries install to `~/bin/` via `make install` (see the Makefile)

## Commit discipline

Include the bead ID in every commit message that touches code for that bead, so work stays traceable across the repo.
