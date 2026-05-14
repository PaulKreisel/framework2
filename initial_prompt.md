# Initial design conversation

framework2 started with a long ideation phase before any code was written. This file captures the conceptual decisions and the reasoning behind them. Detailed specifications live in the corresponding beads — this is the *why*, not the *what*.

## Origin

The previous framework (`~/ai/framework`) was a layered shell-based system organized around Jira tickets. Each ticket owned its own YAML state, its own bd database, its own worktrees, and its own launchers. Over time, that coupled the framework too tightly to Jira and fragmented state across N per-ticket silos.

framework2 is a clean restart. No code is being ported from the old framework. The old tree remains readable at `~/ai/framework/` for reference only.

## Core principles

- **Go, not bash.** Testability and maintainability are the deciding factors.
- **Beads as the source of truth.** Every TODO, every plan step, every design open question is a bead. We do not maintain markdown TODO lists for persistent work.
- **Agents are per-bead, not per-topic.** When a session is launched, the unit of work is the bead — not the Jira ticket, not the codebase area.
- **MVP-first.** Ship the smallest useful thing. Resist generality.
- **Realistic testing.** Tests use real `bd` and real git repositories in tmpdirs. No mocks.

## Beads and worktrees

A bead can reference one or more directories on disk. Some are git worktrees of work repos, some aren't git at all. The `ticket dir` subcommand records these as `bead.metadata.dirs = [{ path, repo?, localRepo?, branch? }]`.

framework2 deliberately does *not* create worktrees as part of linking. A colleague may have already created the branch; the bead only needs to know which directory to point at. Worktree creation, if and when it's automated, will be a separate tool.

## Multi-parent branch base (open)

When a bead has two parents that each modified the same repo, the branch base for the child is ambiguous: rebase one onto the other, or branch from `origin/master`? The current direction is **rebase one onto the other, with discipline on closing**: a bead is only closed once the code's been reviewed enough to safely build on it. If something turns out wrong after closing, the recovery path is:

- Reopen the bead if nothing depends on it yet, or
- Create a follow-up bead.

This direction is documented but not yet implemented or tested.

## Where beads live

Two databases, two roles:

- `~/knime/.beads/` (prefix `kn`) — work. Jira tickets are represented as *placeholder beads* in this DB; Jira is not the organizing principle.
- `~/ai/.beads/` (prefix `ai`) — personal projects, including framework2 itself.

Routing is automatic via bd's cwd walk-up — anything under `~/knime/` resolves to the knime DB; under `~/ai/` resolves to the personal DB. No flags needed.

Both DBs have Dolt auto-commit on (`bd config set dolt.auto-commit on`). Every write produces a Dolt commit; the data history lives inside `.beads/embeddeddolt/` and is queryable via `bd history` / `bd diff`. Each DB is its own self-contained versioned store — no separate plain `git init` was needed around it.

Cross-DB dependencies do not exist in bd. That's accepted: a knime bead cannot `blocks` a personal bead. bd's `ship`/`federation` commands exist for cross-workspace coordination but are out of scope for now.

## Tooling notes

- **`bd worktree` is the wrong fit for our model.** It assumes bd lives inside the same repo whose worktrees it manages. Our model has bd global, with worktrees spawned across many unrelated repos. `ticket dir` does the dir-tracking instead.
- **Shell out to `bd`.** All bd interaction goes via `bd <verb> --json` on the command line. Do not import bd as a Go library — keeps the dependency surface tiny and the failure modes obvious.
- **`bd init` flags worth knowing.** `BD_NON_INTERACTIVE=1`, `--skip-agents`, `--skip-hooks` — needed in fixtures to keep init from creating an `AGENTS.md` or installing git hooks.

## Testing philosophy

Each test spins up a throwaway bd database in a tmpdir and real git repos in tmpdirs, then runs the real `ticket` binary against them. No mocks. `bd init` is slow (10–30s for embedded Dolt) — that cost is paid honestly, either per-test or once per package via `TestMain`. The choice is left to whoever builds the test harness; it should be documented in code.
