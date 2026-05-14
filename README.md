# framework2

Successor to `~/ai/framework`, rebuilt around [beads](https://github.com/) as the source of truth.

## Design

The design decisions and the reasoning behind them are in [initial_prompt.md](initial_prompt.md). Per-task specifications live in beads in `~/ai/.beads/` — start with `bd ready`.

Agents working in this repo: see [CLAUDE.md](CLAUDE.md) for the working rules.

## Layout

```
cmd/         one directory per binary
internal/    shared internals (added as needed)
```

For per-command usage, run the binary with `--help` — this README intentionally does not duplicate it.

## Build

```
make install
make test
```
