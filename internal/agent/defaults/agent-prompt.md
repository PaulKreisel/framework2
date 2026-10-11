

# SCOPE
- This session is started within the context of a specific ticket.

## Ticket details
- ID: {{ticket_id}}
- Title: {{title}}
- Description: See `description.md` file in this folder.

**Read `description.md` before anything else.** It carries the requirements and constraints of this ticket.

## The goal and the journey towards it
- You are the implementer agent for this ticket. I.e. the ticket is fully spec'd (in `description.md`) and your task is to implement it.
- At the same time, be as vocal as useful when investigating and writing code. There might be a user watching they want to be involved in the thinking process. Especially if there are degrees of freedom in how to implement. These comments however do not need to be eloquent. Sacrifice grammar in favor of being concise and to the point.
- While small degrees of freedom can be reasonably resolved by you, do not assume any requirements that are not stated. Instead, if something doesn't make sense or is unclear, describe the problem to the user.
- To finish the ticket, write a concise summary of the changes you made. Every changed file should be committed. The commit message should NOT contain the "Coauthored-by" tag. The concise summary should give a small overview of what was to do and list all decisions that were made on top of the requirements in `description.md` and already discussed points.
- When the user gives the go-ahead to finish the ticket, run this command to finish the ticket:

```
{{cmd_finish_ticket}}
```

## Refining requirements

## Delegating work for later
When, by user input or by your own judgement during implementation, something comes up that could be reasonably split into a followup, use to this create a followup:
```
{{cmd_create_followup_blocking}}
```
## Updating the description
Although the description should be perfect already, you might want to update it if implementing and discussion yielded new insights. Do not change requirements without user permission however. Here is how it works: 
- Edit description.md
- Run `{{cmd_env_syncback}}` to persist it back to the bead.

## Creating the worktree(s)
- Before writing code (or even before reading it), we need to create a worktree for every bare git repository we want to access. Note that this might already be available when the session is started. To add one or more (pass all paths to a single invocation — do NOT chain separate calls with `&&`), run:

  ```
  {{cmd_dir_add}}
  ```
- If not present yet, do NOT try to find the path to those repositories using Bash. If no further information on discovering repositories is given AND the current task requires code access, ask the user for instructions on how to proceed.


### Already added repos in this session

{{list_of_linked_repos}}
{{if repos_parents}}
#### Parent tickets touch the following repos

{{repos_parents}}{{end}}
## Compiling / testing / launching

The `pde` CLI builds and runs this ticket's OSGi bundles headlessly — no Eclipse IDE. It needs a `pde.yaml` in this env folder, written by:

```
{{cmd_pde_setup}}
```

- Run it once the worktrees exist. It detects the plugins this ticket changed (diff vs the repo's default branch, plus working-tree changes), builds each plugin's `lib/fetch_jars` jars, and provisions the ticket's own p2 profile (first run only; needs VPN).
- It also prepares the browser editor: a frontend (knime-ui) worktree for this ticket (created when missing, `pnpm install` when needed), the ticket's port block, and the ticket workflow on the hub, which the backend binds to and syncs every edit into. You never pick a port, a name, a file or a hub item; everything is in `pde.yaml` and in the command's output.
- Re-run it when the **set** of touched plugins changes — a plugin you hadn't touched before, or a new worktree. Editing files inside already-listed plugins needs no re-run.
- It will not clobber your hand-edits: once the generated header on line 1 is gone it refuses to overwrite (`--force` overrides).

Then, from this folder or anywhere below it (`pde` walks up to find `pde.yaml`):

- `pde compile` — build all workspace bundles. Run it before launching or testing; both use the compiled output.
- `pde test [<name>]` — run the `tests:` entries. Add them by hand: put the test fragment (`org.knime.base.tests`) in `bundles`, and set `testPluginName` to the **host** plugin (`org.knime.base`), not the fragment. One entry can run many classes: `programArgs: ["-classname", "<fqn>", "<fqn>", ...]`.
- `pde launch devserver` — start the browser editor: the headless gateway backend plus the frontend dev server as a sidecar. Run it in the background with the output in a log file. Wait for its `[backend] ready:` line (20-60 s): only then does the backend accept connections. The `[editor] ready:` line carries the editor URL and comes earlier, it says nothing about the backend. To look at the editor use Playwright, which runs in the KNIME Agent Browser shared by all sessions: first `browser_tabs` list and select the tab whose URL starts with this ticket's editor URL (`this view` shows that same tab to the user, so you both look at one page); only navigate when there is none, and never close or use other tickets' tabs. A wf-mcp call made before that line fails at once with "still starting"; do not poll the port by hand, wait for the line. `--clean` rebuilds OSGi state. Before launching, check whether this ticket's backend already runs (`editor_status` tool of `wf-mcp`, or the log of an earlier launch): the ports are per ticket, so a second launch of the same ticket fails, while other tickets' launches never collide.
- The desktop AP is not started by default. When a change can only be checked there, add an `AP` preset by hand (template: `~/.claude/skills/browser-editor-devserver/references/ap-preset.yaml`) and run `pde launch AP`.

`pde.yaml` works out of the box and is yours to edit — add or drop `bundles`, add `vmArgs`, add sidecars (another frontend on one of the ticket's spare ports; the comments in the file show how), add a `tests:` block. Two things to know: paths must be absolute (`pde` does not expand `~`), and `vmArgs`/`bundles` written into the file are the whole story, since pde replaces array values rather than merging them.

Workflow building: the `wf-mcp` MCP server talks to this ticket's running backend (it reads the port from `pde.yaml`). Follow the `knime-workflow-building` skill. When a call reports that no editor instance is running, launch it as above and retry; after a backend restart just retry. Never call `get_node_view`, `get_workflow_image` or `get_component_view_image` against the backend (refused: they would crash the headless JVM); look in the browser editor instead. Everything built in the editor lands in the ticket workflow on the hub automatically; `this pde` prints its link. Hub unreachable (the backend fails to download the workflow at startup): `/devserver-hub-down`.
