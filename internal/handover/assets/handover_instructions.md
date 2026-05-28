# Handover JSONL → Beads Exploration

Walkthrough of how to take a beads handover JSONL export, load it into a fresh
embedded beads database, and produce a per-subtask overview table cross-referenced
with the commits on the linked GitHub PRs.

## Prerequisites

| Tool | What it is | Install / docs |
| --- | --- | --- |
| `bd` (beads) | Graph-based issue tracker for coding agents (Dolt-backed) | https://github.com/steveyegge/beads |
| `gh` | GitHub's official CLI | https://cli.github.com/ |
| Python 3 | For `overview.py` | — |

Authenticate `gh` once (`gh auth login`) so it can read the relevant repos.

> Throughout this document `HANDOVER.jsonl` is a placeholder for whatever the
> actual export file is called (e.g. `kn-gly-20260522-102512.jsonl`). Substitute
> your file's name everywhere it appears.
>
> Likewise, `kn-gly` is used as the example **root (epic) bead id**; replace it
> with the id of the parent bead in your export.

## 1. (Optional) Find the epic root id

Each line in the export is a JSON object. Issues use ids like `kn-gly`,
`kn-gly.1`, `kn-c72`. The epic root is the stem the children hang off.
You can skip this step — `overview.py` auto-detects it — but if you want to
confirm it first:

```python
import json

JSONL = 'HANDOVER.jsonl'  # ← replace with your file
ids = []
with open(JSONL) as f:
    for line in f:
        d = json.loads(line)
        if d.get('_type') != 'memory':
            ids.append(d.get('id'))

print('count:', len(ids))
print('sample:', ids[:5])
```

## 2. Set up a fresh beads database and import

`bd init` refuses to run inside an existing `.beads/` directory, so use a
scratch location:

```bash
mkdir -p /tmp/bd-handover-import
cp HANDOVER.jsonl /tmp/bd-handover-import/
cd /tmp/bd-handover-import
bd init --non-interactive --skip-hooks --skip-agents
bd import HANDOVER.jsonl
# → Imported N issues from HANDOVER.jsonl
```

The init prefix (taken from the directory name) does not matter — imported
issues keep the ids already in the JSONL.

Sanity check:

```bash
bd list --all     # flat parent → children view
```

## 3. Run `overview.py`

Run from inside the beads DB directory (so `bd` finds the right database),
passing the path to the script:

```bash
cd /tmp/bd-handover-import
python3 /path/to/overview.py kn-gly
```

See [`overview.py`](./overview.py) for the script. It:

1. Reads the child list and their `contributionIndex` from `bd`
2. Fetches commits for each PR via `gh`
3. Prints a markdown table — rows = subtasks (sorted by `contributionIndex`),
   columns = repos

The epic id argument is optional; the script auto-detects it from `bd list --all`
if omitted.

Example output (from the `kn-gly` handover):

| Subtask | Child | Title | knime-core-ui#241 | webapps-common#379 | knime-base#212 |
|---------|-------|-------|-------------------|--------------------|----------------|
| 1 | kn-gly.1 | Add new @ButtonWidget annotation | `bc1f4fd2` Add public @ButtonWidget annotation | `5c1e9432` Add progressButton renderer | `278c9370` Migrate CSV Reader autodetect button |
| 2 | kn-gly.4 | Deprecate @SimpleButtonWidget | `316fc9b0` Deprecate @SimpleButtonWidget | — | — |
| 3 | kn-gly.5 | Update CSV Reader snap files | — | — | `4e777e18` Update CSV Reader snapshots |
| 4 | kn-gly.3 | Add spinner + 1s success state | `9e388542` Forward trigger Promise resolution | `cf7aaabb` Add spinner + 1s success state | — |
| 5 | kn-gly.7 | Align ProgressButton visuals (KDS) | — | `c91bdf59` Use KdsProgressButton | — |
| 6 | kn-c72 | Make icons mandatory/configurable | `a81ae54f` Make icon configurable | `1ff73ca3` Honor configurable icon | `0955811f` Use Icon.AUTO_CONFIGURE |
| 7 | kn-gly.6 | Add @ButtonWidget example | `2782d8c0` Add @ButtonWidget example | — | — |
| 8 | kn-gly.8 | Update knime-designsystem version | — | `cea8da3b` Update KDS deps to 0.28.7 | — |
| — | kn-gly.9 | PR Version Packages | — | — | — |

A cell may contain multiple commits if the subtask required more than one change
in that repo.

## 4. Read a bead's agent conversation

To read the agent conversation for a specific bead, run from inside the beads
DB directory:

```bash
cd /tmp/bd-handover-import
python3 /path/to/conversation.py <bead-id>
```

See [`conversation.py`](./conversation.py) for the script. It prints only
`mode=agent` conversations as plain text, filtering out noise entries
("Relaunch successful" / "No response requested."). By default it shows only
the last agent run, which is almost always what you want.

The script already filters to `mode=agent` runs (the implementers). Only pass
`--all` if explicitly asked — a bead may have multiple agent runs and the last
one is almost always sufficient for a reviewer.

## Reference: keys we relied on

- Parent metadata: `metadata.prs[]` → `{repo, number, branch}`
- Child metadata: `metadata.handover.contributionIndex` (integer, camelCase)
- Commit message convention: trailing `Subtask <N>` matches the child's index
