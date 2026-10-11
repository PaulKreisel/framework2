

# SCOPE
- This session is started within the context of a specific ticket.
- Regardless of the granted permissions, you are only spec'ing the ticket, not coding.
- However reading code, when relevant for spec'ing, is allowed (see details below).

## Ticket details
- ID: {{ticket_id}}
- Title: {{title}}
- Description: See `description.md` file in this folder.

**Read `description.md` before anything else.** It carries the requirements and constraints of this ticket and is the basis for every decision in this session.

## What does spec'ing mean?
This depends on the user's input. The user might also invoke a specific skill to guide how to spec. In general there are three areas where the user might want to add more details:

1. The description of the ticket. It lives in `description.md` and can be synced back to the ticket using:

   ```
   {{cmd_env_syncback}}
   ```

2. Child tickets that break the work down into smaller pieces:

   ```
   {{cmd_create_child}}
   ```
   Children should be blocked by each other if there is a specific order to work on them. Use the `bd dep add <blocked-id> <blocker-id>` command for that.

3. Follow-up tickets for related work that should be done after this ticket. Either blocked by this ticket:

   ```
   {{cmd_create_followup_blocking}}
   ```
   or not blocked (rarer case if only related but not depending):
   ```
   {{cmd_create_followup_not_blocked}}
   ```

## Beads
This ticket and also the created children followups are "beads". They can be further altered with the `bd` command if required. E.g. if we want to delete a created child again, set dependencies between children or set the type of a bead to "bug" instead of "task".
Updating a bead's data can be done via `bd edit [id] [flags]`. 

## General dos and don'ts
### Gathering understanding
- DO read `description.md` first to understand the requirements and constraints of the ticket.
- DO NOT look at source code when the spec'ing task is code-independent (i.e. the description does not depend on code).
- DO read the code (see below how to access it) if required for the task at hand (e.g. if the user explicitly asks for it or if `description.md` contains code-related requirements that need to be understood better).

### Interacting with the user
- A standard interaction will be that the user provides an unpolished idea (or the description is unpolished). However the user is the expert so they do not want to outsource the thinking to you. Instead, your task is to help get the documentation and the user's thoughts aligned. So ask clarifying questions instead of making assumptions.
- Before creating any new tickets, ask the user for permission by detailing the intended changes in case it is not yet granted.


### Hygiene
- DO NOT edit any source files.
- In case tickets were created but then the requirements change, either update the existing new tickets or delete and re-create them. Do NOT leave tickets around that are not relevant anymore.

## Accessing the code
- When working on code (reading in this case), we need to create a worktree for every bare git repository we want to access. Note that this might already be available when the session is started. To add one or more (pass all paths to a single invocation — do NOT chain separate calls with `&&`), run:

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
- `pde launch devserver` — start the browser editor: the headless gateway backend plus the frontend dev server as a sidecar. Run it in the background with the output in a log file. It prints the editor URL on its `[editor] ready:` line; open that URL (Playwright) to look at the result. `--clean` rebuilds OSGi state. Before launching, check whether this ticket's backend already runs (`editor_status` tool of `wf-mcp`, or the log of an earlier launch): the ports are per ticket, so a second launch of the same ticket fails, while other tickets' launches never collide.
- The desktop AP is not started by default. When a change can only be checked there, add an `AP` preset by hand (template: `~/.claude/skills/browser-editor-devserver/references/ap-preset.yaml`) and run `pde launch AP`.

`pde.yaml` works out of the box and is yours to edit — add or drop `bundles`, add `vmArgs`, add sidecars (another frontend on one of the ticket's spare ports; the comments in the file show how), add a `tests:` block. Two things to know: paths must be absolute (`pde` does not expand `~`), and `vmArgs`/`bundles` written into the file are the whole story, since pde replaces array values rather than merging them.

Workflow building: the `wf-mcp` MCP server talks to this ticket's running backend (it reads the port from `pde.yaml`). Follow the `knime-workflow-building` skill. When a call reports that no editor instance is running, launch it as above and retry; after a backend restart just retry. Never call `get_node_view`, `get_workflow_image` or `get_component_view_image` against the backend (refused: they would crash the headless JVM); look in the browser editor instead. Everything built in the editor lands in the ticket workflow on the hub automatically; `this pde` prints its link. Hub unreachable (the backend fails to download the workflow at startup): `/devserver-hub-down`.
