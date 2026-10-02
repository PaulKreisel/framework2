// Package ticketcli implements the `ticket` command-line dispatcher.
//
// It is extracted from cmd/ticket so tests can invoke subcommands in-process
// via [Run] without forking a subprocess. The `this` binary shares this
// dispatcher: it resolves a ticket id from a .ticket-id marker and forwards
// the call via the global `--ticket` flag, so there is no `this`-side
// subcommand knowledge to drift against `ticket`.
package ticketcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"framework2/internal/agent"
	"framework2/internal/bdcli"
	"framework2/internal/create"
	"framework2/internal/dir"
	"framework2/internal/env"
	"framework2/internal/finish"
	"framework2/internal/handover"
	"framework2/internal/next"
	"framework2/internal/pde"
	"framework2/internal/pr"
	"framework2/internal/project"
	"framework2/internal/projectconfig"
	"framework2/internal/ready"
	"framework2/internal/slug"
	"framework2/internal/teardown"
)

const usage = `usage:
  ticket [global-flags] <subcommand> [args...]

global flags:
  --db <path>      bd database path
  --ticket <id>    ticket id (alternative to per-subcommand positional)

subcommands:
  dir link [<id>] <path>             link a path to a ticket
  dir remove [<id>] <path>           unlink a path from a ticket
  dir list [<id>]                    list paths linked to a ticket
  dir worktree [<id>] <bare-repo> [<bare-repo> ...] [--branch <name>]
                                     create a worktree off each <bare-repo> for
                                     the ticket and link it (cascades through
                                     parents so ancestors gain the worktree too).
                                     Several bare repos can be passed at once;
                                     each is handled as if added by its own
                                     invocation.
                                     Branch name and worktree path follow the
                                     templates in <beadsDir>/project.yaml when
                                     present; otherwise the defaults match the
                                     pre-config behavior. See
                                     internal/projectconfig for the schema.
                                     With --branch, the explicit branch name is
                                     checked out instead of the template-derived
                                     one: origin is fetched best-effort and the
                                     worktree tracks origin/<name> if it shows
                                     up, otherwise the local branch is reused; if
                                     the branch exists in neither place it is
                                     created off the repo's default base
                                     (origin/HEAD, or local main/master). No
                                     parent cascade in this mode.
  dir status [<id>] [<selector>] [--json]
                                     show working-tree status of one linked dir
                                     and (when the parent links the same bare
                                     repo) how its branch has diverged from the
                                     parent's. selector matches path/localRepo
                                     /bareRepo; may be omitted when the ticket
                                     has exactly one linked dir.
  dir sync-check [<id>]              for each linked dir that has a branch with
                                     a configured upstream, fetch and verify it
                                     is in sync (0 ahead, 0 behind). Read-only
                                     (fetch only). Dirs that are gone, detached,
                                     or have no upstream are skipped (pass).
                                     Exit non-zero, listing the offenders, when
                                     a checkable dir diverges. Finish-step check.
  dir slug [<id>] <slug>             set the bead's slug (refused once the slug
                                     has been frozen by a branch/worktree)
  dir slug --ai [<id>]               refine the slug via Claude Haiku in place;
                                     silently no-ops if the slug is frozen
  env [<id>]                              create on-disk env folder for a ticket
  env syncback [<id>]                     push <env>/description.md back to the
                                          bead description (one-way file → bead)
  pde [<id>] [--vm-arg <arg>]... [--no-fetch-jars] [--no-target-install]
      [--dry-run] [--force]
                                          write <env>/pde.yaml so the headless
                                          pde CLI can compile/launch/test the
                                          plugins this ticket changed. Detects
                                          them per linked dir (merge-base diff
                                          vs the bare repo's default branch,
                                          plus working-tree changes), runs each
                                          plugin's lib/fetch_jars Maven build,
                                          emits the target section, those
                                          bundles plus project.yaml's
                                          pde.extra-bundles and one KNIME AP
                                          launch preset, then provisions the
                                          env's p2 profile on first run (needs
                                          VPN). Requires pde.target in
                                          <beadsDir>/project.yaml.
                                          pde finds the file by walking up, so
                                          "pde compile" works from the env
                                          folder down. --vm-arg appends to the
                                          preset (repeatable); --dry-run prints
                                          instead of writing; --force replaces a
                                          file whose generated header is gone.
  agent [<id>] [--prompt "<text>"] [--resume <session-id> | --dry-run]
                                          launch claude from a ticket's env folder
                                          in coding mode. Claims the bead; if it
                                          is already in_progress the launch
                                          joins it as a second coding agent
                                          instead: no claim, no kickoff message,
                                          claude opens to a blank input.
                                          With --prompt, the supplied text
                                          replaces the built-in kickoff message
                                          ("Implement the ticket, then set up
                                          pde and compile, test and launch it
                                          ...") claude auto-submits as turn 1
                                          (empty string behaves like an omitted
                                          flag; on a second agent it is the
                                          only way to get a turn 1).
                                          With --resume, claude reattaches to
                                          the named transcript instead of
                                          starting a fresh session; the existing
                                          agent record is updated in place.
                                          With --dry-run, print the rendered
                                          system prompt that would be appended
                                          on a fresh launch and do nothing else
                                          (no env folder, no claim, no claude).
                                          --prompt is rejected with both
                                          --resume and --dry-run (transcripts
                                          already exist / there's no agent to
                                          submit to). --resume and --dry-run
                                          are also mutually exclusive.
  agent list [<id>] [--json]              list agents that have been launched on a
                                          ticket (newest first). See WAITING
                                          REASON below for what the column /
                                          --json field carry.
  agent attach [<id>] <session-id>        focus the macOS Terminal.app tab
                                          hosting the wrapper. If no live tab
                                          owns the wrapper's tty (e.g. the
                                          terminal was killed), fall through
                                          to spawning a fresh wrapper that
                                          reattaches to the same claude
                                          transcript via --resume, in the
                                          recorded mode (agent or spec).
                                          macOS only; missing/unknown session
                                          id errors with the list of valid
                                          ids.
  agent status [<id>] <session-id> [--json]
                                          print one agent record's metadata plus
                                          the untruncated waiting reason. Unknown
                                          session id errors with the list of
                                          valid ids. See WAITING REASON below
                                          for how the reason is sourced.
  agent delete [<id>] <session-id>...     remove agent record(s) from the ticket's
                                          agent list. Operates on the record, not
                                          the process — refuses to delete any
                                          record unless --force is passed (there
                                          is no liveness probe; users must
                                          confirm intent). Unknown id errors
                                          with the list of valid ids.
  agent set-status <running|waiting> [--session <id>] [--source <kind>]
                                          update an agent record's running/waiting
                                          status. Driven by Claude Code Stop /
                                          UserPromptSubmit / PreToolUse /
                                          StopFailure hooks; if --session is
                                          omitted the session id is parsed from
                                          the JSON hook payload on stdin.
                                          --source selects how the waiting
                                          reason is extracted from the payload:
                                          ask-user-question (tool_input JSON),
                                          exit-plan-mode (tool_input.plan), or
                                          stop-failure (matcher[: message]).
  agent continue [<id>]                   resume the ticket's most recently active
                                          session in the current terminal, in its
                                          recorded mode (agent or spec). The ticket
                                          is taken from --ticket / <id> when given,
                                          otherwise from the .ticket-id marker found
                                          by walking up from the cwd. Outside a
                                          ticket env it degrades to a plain
                                          ` + "`claude --continue`" + ` launch, so it can back
                                          the machine-wide ` + "`cc`" + ` alias.
  agent resume [<id>]                     like ` + "`agent continue`" + ` but pick the session
                                          from an fzf picker listing exactly the rows
                                          ` + "`agent list`" + ` shows. Aborting the picker is a
                                          no-op. Outside a ticket env it degrades to
                                          a plain ` + "`claude --resume`" + ` launch (the ` + "`cr`" + `
                                          alias).
  agent fork [<id>]                       like ` + "`agent resume`" + ` but start a NEW session
                                          that continues from the picked session's
                                          transcript (claude --fork-session), in the
                                          picked session's mode. The parent keeps
                                          running; the new record shows the parent
                                          under FORKED FROM. Does not claim the
                                          bead. Outside a ticket env it degrades to
                                          ` + "`claude --resume --fork-session`" + ` (the
                                          ` + "`cf`" + ` alias).
  agent relaunch [<id>] [extra-args...]   from inside a session: ask the running
                                          wrapper to re-exec claude with
                                          --resume <its session id> (use after a
                                          dir link/worktree). The pid file is per
                                          ticket, so with two sessions on one
                                          ticket the kill lands on whichever
                                          claude started last.
  agent --print-default-prompt            print the built-in default coding-mode
                                          prompt template (copy as a starting
                                          point for <BEADS_DIR>/agent-prompt.md)
  agent --list-prompt-fragments           list named fragments operator templates
                                          can interpolate via {{name}}
  spec [<id>] [--prompt "<text>"] [--dry-run]
                                          launch claude from a ticket's env folder
                                          in spec mode (edits description.md and
                                          spawns related beads; does not code).
                                          With --prompt, the supplied text is
                                          auto-submitted by claude as turn 1
                                          (spec mode otherwise opens to a blank
                                          input; empty string behaves like an
                                          omitted flag). With --dry-run, print
                                          the rendered system prompt that would
                                          be appended on a fresh launch and do
                                          nothing else. --prompt is rejected
                                          with --dry-run.
  spec --print-default-prompt             print the built-in default spec-mode
                                          prompt template (copy as a starting
                                          point for <BEADS_DIR>/spec-prompt.md)
  create child <title> --description <text>
                                          new bead as a child of the current ticket.
                                          --description seeds the new bead's
                                          description (and its env description.md)
                                          and is required.
  create followup [--not-blocked] <title> --description <text>
                                          sibling of the current ticket (top-level if
                                          current has no parent). By default current
                                          blocks the new bead; --not-blocked skips
                                          that dep. --description is required (same
                                          as for create child).
  next [<id>]                             show ready descendants of a ticket
  next agent [<id>] [--all] [--min-recursion-depth <M>] [--recursion-depth <N>] [--json]
                                          list agents that are currently waiting
                                          for input across the bead subtree.
                                          M (default 0) is the shallowest hop
                                          included — 0 includes the bead itself,
                                          1 starts at direct children. N (max
                                          depth) defaults to unlimited; pair
                                          --min-recursion-depth 0 with
                                          --recursion-depth 0 to list only the
                                          root's waiting agents. With --all,
                                          running agents are listed too. See
                                          WAITING REASON below for the column
                                          / --json field semantics.
  ready [<id>]                            add the ` + "`ready`" + ` label to a ticket so it
                                          is auto-launched when its last blocker
                                          finishes. See ai-vrc.56.
  notready [<id>]                         remove the ` + "`ready`" + ` label from a ticket.
  finish [<id>]                           wrap up: rebase the ticket's worktrees
                                          onto their parent branch (FF when no
                                          divergence, real rebase otherwise),
                                          close the bead, remove the worktrees,
                                          and delete the env folder. One-way. On
                                          the step-driven path, while steps stay
                                          pending a background poller re-runs
                                          this on an interval until they close
                                          (THIS_FINISH_POLL_INTERVAL, default
                                          300s); concurrent runs are skipped via
                                          a per-ticket lock.
  finish [<id>] --continue                resume a finish whose rebase paused on
                                          conflicts: runs ` + "`git rebase --continue`" + ` in
                                          each paused worktree, then completes
                                          the rest of the flow.
  teardown [<id>]                         dismantle without landing: force-remove
                                          every linked worktree (uncommitted
                                          changes included), unlink the entries,
                                          close the bead if still open, delete
                                          the env folder. Branches stay in the
                                          bare repo. No rebase, no PR checks, no
                                          follow-up launches. Accepts already-
                                          closed beads, so stale environments
                                          can be swept in bulk.
  pr list [<id>]                          print attached GitHub PRs as a table.
                                          State is fetched live from GitHub via
                                          ` + "`gh`" + `.
  pr create [<id>] [<path>] [--ready]     create a GitHub PR for the dir at
                                          <path> (omit when the ticket has
                                          exactly one linked dir), base branch
                                          resolved via the parent chain.
                                          Pushes the current branch upstream
                                          (force-with-lease) before calling
                                          ` + "`gh pr create`" + `. The PR is opened as a
                                          draft unless --ready is passed. If
                                          exactly one open PR already exists
                                          for the branch it is linked instead;
                                          two or more is an error.
  pr create [<id>] --all [--ready]        run ` + "`pr create`" + ` for every "eligible"
                                          linked dir (≥1 commit ahead of base).
                                          Dirs already having a PR attached
                                          for their repo are skipped with a
                                          notice; a dir whose branch already
                                          has one open PR is linked, two or
                                          more is skipped with a notice;
                                          per-dir failures are reported but do
                                          not abort the rest.
  pr show [<id>] [<path>]                 open the attached PR for the dir at
                                          <path> in the default browser. With
                                          a single linked dir, <path> may be
                                          omitted.
  pr show [<id>] --all                    open the attached PRs for every
                                          "eligible" linked dir (≥1 commit
                                          ahead of base). Dirs with no PR
                                          attached are skipped with a notice.
  pr link [<id>] [<path>] [<pr-url>]      attach an existing PR to the bead,
                                          associating it with the dir at
                                          <path>. No push. With <pr-url> the
                                          PR is taken as given; without it,
                                          the dir's unique open PR (the single
                                          open PR for its current branch) is
                                          auto-detected — zero or two-or-more
                                          is an error.
  pr link [<id>] --all                    auto-link the unique open PR for
                                          every "eligible" linked dir (≥1
                                          commit ahead of base) not already
                                          PR-attached. Dirs whose branch has
                                          zero or two-or-more open PRs are
                                          skipped with a notice.
  pr create-bead [<id>] <pr-url>          create a new bead as a child of the
                                          currently-active ticket whose
                                          description is the PR URL and whose
                                          title is ` + "`PR <pr-title>`" + `. No dir is
                                          linked.
  pr check [<id>] [--merged]              read-only: exit 0 iff every linked
                                          dir has an attached PR, else exit
                                          non-zero listing the offending dirs.
                                          With --merged, additionally require
                                          each dir's PR to be merged on GitHub.
                                          No side effects; usable as a
                                          finish-step ` + "`check`" + `.
  pr refresh [<id>]                       reconcile bead state with GitHub:
                                          validate that every linked dir has
                                          a PR, surface closed-unmerged or
                                          deleted PRs (and exit non-zero),
                                          and when every attached PR is
                                          merged and no open children block,
                                          run the full ` + "`finish`" + ` flow.
  handover [<id>] [--output <path>]       produce a portable JSONL snapshot
                                          of the ticket subtree (one bead per
                                          line; host paths stripped;
                                          conversations already live under
                                          each bead's
                                          ` + "`metadata.agents[].conversation[]`" + `).
                                          Defaults to
                                          ` + "`<beadsDir>/handovers/<id>-<UTC TS>.jsonl`" + `.
                                          Code is not shipped: the reviewer
                                          fetches PRs from each bead's
                                          ` + "`metadata.prs`" + ` against the project's
                                          remote, so ticket branches must be
                                          pushed first.
                                          When the ticket itself carries a
                                          Jira external_ref, the JSONL plus
                                          three embedded review aids
                                          (` + "`handover_instructions.md`" + `,
                                          ` + "`conversation.py`" + `, ` + "`overview.py`" + `) are
                                          attached to the Jira issue. Auth via
                                          ` + "`KNIME_ATLASSIAN_EMAIL`" + ` /
                                          ` + "`KNIME_ATLASSIAN_API_TOKEN`" + `.
                                          Non-destructive: never closes beads,
                                          removes worktrees, or touches the env
                                          folder.

WAITING REASON column:
  Surfaced by ` + "`agent list`" + `, ` + "`agent status`" + ` and ` + "`next agent`" + ` for agents
  in the waiting state.

  The cell is derived from the tail of the per-record ` + "`conversation`" + `
  array (see "conversation field" below) for both agent-mode and
  spec-mode sessions. The short summary shape: Stop renders the
  assistant's final text segment, AskUserQuestion renders
  "asking: <first question>", ExitPlanMode renders
  "plan: <first non-empty line>", StopFailure renders the bare matcher
  (and ": message" in the untruncated form).

  ` + "`agent list`" + ` and ` + "`next agent`" + ` truncate the cell to one line; use
  ` + "`agent status`" + ` (or --json) for the full payload.

conversation field:
  An append-only array on the agent record that captures the per-session
  conversation history: each user prompt, each assistant text segment,
  each AskUserQuestion / ExitPlanMode / StopFailure event becomes its own
  entry with a ` + "`type`" + ` tag and a ` + "`contextTokens`" + ` snapshot. Tool calls
  and tool results are not captured. Populated for both agent-mode and
  spec-mode sessions. Main-session only; subagent payloads (` + "`agent_id`" + `
  present) are skipped. See ` + "`agent list --json`" + ` for the full shape. The
  global ` + "`contextTokens`" + ` field continues to mirror the most recent
  reading from any hook (including PostToolUse, which carries no entry
  but refreshes the snapshot).`

// Options configures a Run invocation. The zero value resolves BeadsDir
// through [project.Dir], matching the production behaviour of the
// `ticket` binary.
type Options struct {
	// BeadsDir, when non-empty, overrides BEADS_DIR for the duration of this
	// invocation: env folder lookups and child bd subprocess invocations use
	// this value instead of [project.Dir]. Tests use it to drive a per-test
	// .beads/ directory in parallel.
	BeadsDir string
}

// Run executes the ticket command-line with the given args, writing normal
// output to stdout and errors to stderr. Returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunWithOptions(Options{}, args, stdout, stderr)
}

// RunWithOptions is like [Run] but lets callers pin BEADS_DIR explicitly via
// [Options].
func RunWithOptions(opts Options, args []string, stdout, stderr io.Writer) int {
	beadsDir := opts.BeadsDir
	if beadsDir == "" {
		beadsDir = project.Dir()
	}
	if err := dispatch(beadsDir, args, stdout, stderr); err != nil {
		fmt.Fprintln(stderr, "ticket:", err)
		return 1
	}
	return 0
}

func dispatch(beadsDir string, args []string, stdout, stderr io.Writer) error {
	db, ticket, rest, err := extractGlobal(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errors.New(usage)
	}
	switch rest[0] {
	case "dir":
		return runDir(db, beadsDir, ticket, rest[1:], stdout)
	case "env":
		return runEnv(db, beadsDir, ticket, rest[1:], stdout)
	case "pde":
		return runPde(db, beadsDir, ticket, rest[1:], stdout)
	case "agent":
		return runAgent(db, beadsDir, ticket, rest[1:], agent.ModeAgent, stdout)
	case "spec":
		return runAgent(db, beadsDir, ticket, rest[1:], agent.ModeSpec, stdout)
	case "create":
		return runCreate(db, beadsDir, ticket, rest[1:], stdout)
	case "next":
		return runNext(db, beadsDir, ticket, rest[1:], stdout, stderr)
	case "ready":
		return runReady(db, beadsDir, ticket, rest[1:])
	case "notready":
		return runNotReady(db, beadsDir, ticket, rest[1:])
	case "finish":
		return runFinish(db, beadsDir, ticket, rest[1:], stdout)
	case "teardown":
		return runTeardown(db, beadsDir, ticket, rest[1:], stdout)
	case "pr":
		return runPR(db, beadsDir, ticket, rest[1:], stdout)
	case "handover":
		return runHandover(db, beadsDir, ticket, rest[1:], stdout)
	case "pipeline-async":
		// Hidden subcommand: detached runner spawned by `finish` to execute
		// pipeline.md commands after the parent process has exited. Not
		// listed in `usage` on purpose — users never invoke it directly.
		return runPipelineAsync(db, beadsDir, rest[1:], stdout)
	case "terminate-async":
		// Hidden subcommand: detached runner spawned by `finish` to close
		// the hosting Terminal.app tab (and SIGHUP the wrapper) after the
		// parent finish has returned. Not listed in `usage` on purpose.
		return runTerminateAsync(rest[1:], stdout)
	case "finish-poll":
		// Hidden subcommand: detached background poller spawned by `finish`
		// (step-driven path) that re-invokes the finish flow on an interval
		// while steps remain pending. Not listed in `usage` on purpose —
		// users never invoke it directly. See ai-vrc.86.5.
		return runFinishPoll(db, beadsDir, rest[1:], stdout)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", rest[0], usage)
	}
}

func runDir(db, beadsDir, ticket string, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: ticket dir <link|remove|list> ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "link":
		id, tail, err := resolveID(ticket, rest, 1, "usage: ticket dir link [<id>] <path>")
		if err != nil {
			return err
		}
		if err := dir.Link(db, beadsDir, id, tail[0]); err != nil {
			return err
		}
		// Only the leaf ticket — the one we just mutated — may have a live
		// wrapper to refresh. dir.Worktree cascades into ancestors, but
		// their sessions aren't running, so we never try to signal them.
		return agent.RelaunchIfActive(beadsDir, id)
	case "remove":
		id, tail, err := resolveID(ticket, rest, 1, "usage: ticket dir remove [<id>] <path>")
		if err != nil {
			return err
		}
		return dir.Remove(db, beadsDir, id, tail[0])
	case "list":
		id, _, err := resolveID(ticket, rest, 0, "usage: ticket dir list [<id>]")
		if err != nil {
			return err
		}
		return dir.List(db, beadsDir, id, stdout)
	case "worktree":
		return runDirWorktree(db, beadsDir, ticket, rest)
	case "slug":
		return runSlug(db, beadsDir, ticket, rest)
	case "status":
		return runDirStatus(db, beadsDir, ticket, rest, stdout)
	case "sync-check":
		id, _, err := resolveID(ticket, rest, 0, "usage: ticket dir sync-check [<id>]")
		if err != nil {
			return err
		}
		return dir.SyncCheck(db, beadsDir, id, stdout)
	default:
		return fmt.Errorf("unknown dir subcommand %q", sub)
	}
}

// runDirStatus handles `ticket dir status [<id>] [<selector>] [--json]`.
// selector is optional; when absent the ticket must have exactly one linked
// dir, otherwise the command refuses with the list of valid choices.
func runDirStatus(db, beadsDir, ticket string, args []string, stdout io.Writer) error {
	var jsonOut bool
	positional := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
			continue
		}
		positional = append(positional, a)
	}
	usage := "usage: ticket dir status [<id>] [<selector>] [--json]"
	// Allow zero or one trailing positional after the id is resolved.
	var id, selector string
	switch {
	case ticket != "":
		switch len(positional) {
		case 0:
		case 1:
			selector = positional[0]
		default:
			return errors.New(usage)
		}
		id = ticket
	default:
		switch len(positional) {
		case 1:
			id = positional[0]
		case 2:
			id, selector = positional[0], positional[1]
		default:
			return errors.New(usage)
		}
	}
	return dir.Status(db, beadsDir, id, selector, jsonOut, stdout)
}

// runDirWorktree handles `ticket dir worktree [<id>] <bare-repo>... [--branch <name>]`.
// Without --branch we delegate to [dir.Worktree] (template-derived branch,
// parent cascade). With --branch we delegate to [dir.WorktreeForBranch]
// (explicit name, origin fetch + remote-track detection, created off the
// default base when it exists nowhere, no cascade).
//
// Several bare repos can be passed in one call; each gets its worktree as if
// added by its own invocation (with --branch, each attaches the same branch
// name). Repos are processed in order; a failing repo does not stop the rest.
// The agent wrapper is relaunched once at the end — this is why multi-arg
// matters: chaining single-repo calls with `&&` never runs the second command
// because the first call's relaunch kills the calling session.
func runDirWorktree(db, beadsDir, ticket string, args []string) error {
	const usage = "usage: ticket dir worktree [<id>] <bare-repo> [<bare-repo> ...] [--branch <name>]"
	var branch string
	positional := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--branch":
			if i+1 >= len(args) {
				return errors.New(usage)
			}
			branch = args[i+1]
			i++
		case strings.HasPrefix(a, "--branch="):
			branch = strings.TrimPrefix(a, "--branch=")
		default:
			positional = append(positional, a)
		}
	}
	id, repos, err := resolveIDVariadic(ticket, positional, usage)
	if err != nil {
		return err
	}
	if len(repos) == 0 {
		return errors.New(usage)
	}
	var errs []error
	added := 0
	for _, repo := range repos {
		var err error
		if branch != "" {
			err = dir.WorktreeForBranch(db, beadsDir, id, repo, branch, true)
		} else {
			err = dir.Worktree(db, beadsDir, id, repo)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", repo, err))
			continue
		}
		added++
	}
	// Only the explicitly addressed ticket may have a live wrapper to
	// refresh; cascade-created ancestor worktrees belong to tickets whose
	// agents aren't running. Relaunch even after partial failure so the
	// successfully added repos reach the refreshed prompt. A repo that is
	// already linked counts as added: dir.Worktree returns nil without
	// creating anything, and the relaunch still fires so the calling
	// session picks the directory up (it may have been linked by a sibling
	// session on the same ticket).
	if added > 0 {
		if err := agent.RelaunchIfActive(beadsDir, id); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// runSlug handles `ticket dir slug [<id>] <slug>` (set the slug) and
// `ticket dir slug --ai [<id>]` (refine via Haiku in the background-spawn flow
// or interactively if invoked by hand). Both forms fail/no-op gracefully when
// the slug is already frozen.
func runSlug(db, beadsDir, ticket string, args []string) error {
	var aiMode bool
	positional := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--ai" {
			aiMode = true
			continue
		}
		positional = append(positional, a)
	}
	if aiMode {
		id, _, err := resolveID(ticket, positional, 0, "usage: ticket dir slug --ai [<id>]")
		if err != nil {
			return err
		}
		return refineSlug(db, beadsDir, id)
	}
	id, tail, err := resolveID(ticket, positional, 1, "usage: ticket dir slug [<id>] <slug>")
	if err != nil {
		return err
	}
	budget, err := branchSlugBudget(db, beadsDir, id)
	if err != nil {
		return err
	}
	newSlug := slug.Derive(tail[0], budget)
	if newSlug == "" {
		return fmt.Errorf("slug %q reduces to empty after normalisation", tail[0])
	}
	return slug.Set(db, beadsDir, id, newSlug)
}

// branchSlugBudget computes the per-bead max slug length such that the
// rendered branch fits in [slug.MaxBranchLen]. Loads the project config and
// the bead's type / Jira ancestor so the branch template can be probed with
// the actual variables that will substitute into it later.
func branchSlugBudget(db, beadsDir, ticketID string) (int, error) {
	cfg, err := projectconfig.Load(beadsDir)
	if err != nil {
		return 0, err
	}
	issueType, err := dir.IssueType(db, beadsDir, ticketID)
	if err != nil {
		return 0, err
	}
	jiraID, err := dir.AncestorJiraID(db, beadsDir, ticketID)
	if err != nil {
		return 0, err
	}
	return cfg.BranchSlugBudget(projectconfig.Vars{
		BeadID: ticketID,
		JiraID: jiraID,
		Type:   issueType,
	}), nil
}

// beadTitle returns the title of ticketID via `bd --json show`.
func beadTitle(db, beadsDir, ticketID string) (string, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "show", ticketID).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("bd show %s: %v: %s", ticketID, err, ee.Stderr)
		}
		return "", fmt.Errorf("bd show %s: %w", ticketID, err)
	}
	var rows []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].Title, nil
}

// refineSlug pulls the bead's title, asks Claude Haiku for a polished slug,
// and writes it back unless the slug has been frozen (which is the common
// case when the user has already cut a branch). On any failure we exit
// silently — refinement is an opportunistic upgrade, never required.
func refineSlug(db, beadsDir, ticketID string) error {
	info, err := slug.Get(db, beadsDir, ticketID)
	if err != nil || info.Used {
		return nil
	}
	title, err := beadTitle(db, beadsDir, ticketID)
	if err != nil || title == "" {
		return nil
	}
	budget, err := branchSlugBudget(db, beadsDir, ticketID)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), slug.RefineTimeout)
	defer cancel()
	improved := slug.Refine(ctx, title, info.Slug, budget)
	if improved == "" {
		return nil
	}
	// Re-check used: a branch may have been cut while Haiku was thinking.
	if cur, err := slug.Get(db, beadsDir, ticketID); err != nil || cur.Used {
		return nil
	}
	_ = slug.Set(db, beadsDir, ticketID, improved)
	return nil
}

func runEnv(db, beadsDir, ticket string, args []string, stdout io.Writer) error {
	if len(args) > 0 && args[0] == "syncback" {
		id, _, err := resolveID(ticket, args[1:], 0, "usage: ticket env syncback [<id>]")
		if err != nil {
			return err
		}
		return agent.Syncback(db, beadsDir, id)
	}
	id, _, err := resolveID(ticket, args, 0, "usage: ticket env [<id>]")
	if err != nil {
		return err
	}
	envPath, err := env.Create(db, beadsDir, id)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, envPath)
	return env.EnterShell(stdout, envPath)
}

func runPde(db, beadsDir, ticket string, args []string, stdout io.Writer) error {
	const usage = "usage: ticket pde [<id>] [--vm-arg <arg>]... [--no-fetch-jars] [--no-target-install] [--dry-run] [--force]"
	req := pde.Request{DB: db, BeadsDir: beadsDir}
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--vm-arg":
			if i+1 >= len(args) {
				return errors.New("--vm-arg needs a value\n" + usage)
			}
			req.VMArgs = append(req.VMArgs, args[i+1])
			i++
		case "--no-fetch-jars":
			req.SkipFetchJars = true
		case "--no-target-install":
			req.SkipTargetInstall = true
		case "--dry-run":
			req.DryRun = true
		case "--force":
			req.Force = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("unknown flag %q\n%s", args[i], usage)
			}
			positional = append(positional, args[i])
		}
	}
	id, _, err := resolveID(ticket, positional, 0, usage)
	if err != nil {
		return err
	}
	req.TicketID = id
	return pde.Run(req, stdout)
}

func runAgent(db, beadsDir, ticket string, args []string, mode agent.Mode, stdout io.Writer) error {
	// Discovery flags that don't launch claude: print the built-in default
	// template (useful as a starting point for an operator file) and list the
	// registered fragment names. Both must be checked before any positional
	// resolution so they work without a current ticket.
	for _, a := range args {
		switch a {
		case "--print-default-prompt":
			_, err := io.WriteString(stdout, agent.DefaultPromptTemplate(mode))
			return err
		case "--list-prompt-fragments":
			for _, name := range agent.FragmentNames() {
				if _, err := fmt.Fprintln(stdout, name); err != nil {
					return err
				}
			}
			return nil
		}
	}
	if mode == agent.ModeAgent && len(args) > 0 && args[0] == "relaunch" {
		// `ticket agent relaunch [<id>] [extra-args...]`
		id, extra, err := resolveIDVariadic(ticket, args[1:], "usage: ticket agent relaunch [<id>] [extra-args...]")
		if err != nil {
			return err
		}
		return agent.Relaunch(beadsDir, id, extra)
	}
	if mode == agent.ModeAgent && len(args) > 0 && args[0] == "list" {
		// `ticket agent list [<id>] [--json]`
		var jsonOut bool
		positional := make([]string, 0, len(args)-1)
		for _, a := range args[1:] {
			if a == "--json" {
				jsonOut = true
				continue
			}
			positional = append(positional, a)
		}
		id, _, err := resolveID(ticket, positional, 0, "usage: ticket agent list [<id>] [--json]")
		if err != nil {
			return err
		}
		// `agent list` is the depth-0 shape of `next agent`: just this
		// ticket, no state filter (every row shows up).
		return next.RunAgent(db, beadsDir, id, 0, 0, false, next.StateFilterNone, jsonOut, stdout)
	}
	if mode == agent.ModeAgent && len(args) > 0 && (args[0] == "continue" || args[0] == "resume" || args[0] == "fork") {
		// `ticket agent continue [<id>]` / `ticket agent resume [<id>]` /
		// `ticket agent fork [<id>]`
		return runAgentContinue(db, beadsDir, ticket, args[0], args[1:])
	}
	if mode == agent.ModeAgent && len(args) > 0 && args[0] == "set-status" {
		// `ticket agent set-status <running|waiting> [--session <id>] [--ticket <id>]`
		return runAgentSetStatus(db, beadsDir, ticket, args[1:])
	}
	if mode == agent.ModeAgent && len(args) > 0 && args[0] == "attach" {
		// `ticket agent attach [<id>] <session-id>`
		id, tail, err := resolveID(ticket, args[1:], 1, "usage: ticket agent attach [<id>] <session-id>")
		if err != nil {
			return err
		}
		return agent.Attach(db, beadsDir, id, tail[0])
	}
	if mode == agent.ModeAgent && len(args) > 0 && args[0] == "status" {
		// `ticket agent status [<id>] <session-id> [--json]`
		return runAgentStatus(db, beadsDir, ticket, args[1:], stdout)
	}
	if mode == agent.ModeAgent && len(args) > 0 && args[0] == "delete" {
		// `ticket agent delete [<id>] <session-id>... [--force]`
		return runAgentDelete(db, beadsDir, ticket, args[1:])
	}
	// --resume <session-id> turns `ticket agent` into the resume path used to
	// re-launch a wrapper whose terminal was killed. It's only valid in
	// ModeAgent (spec sessions don't share a transcript with a coding agent).
	// --dry-run short-circuits to printing the rendered system prompt and is
	// mutually exclusive with --resume.
	// --prompt "<text>" supplies the trailing positional that claude
	// auto-submits as turn 1; rejected on --resume / --dry-run (transcripts
	// already exist / there's no agent to submit to).
	var resume string
	var dryRun bool
	var initialPrompt string
	positional := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--dry-run":
			dryRun = true
		case a == "--resume":
			if i+1 >= len(args) {
				return errors.New("--resume requires a session id")
			}
			resume = args[i+1]
			i++
		case strings.HasPrefix(a, "--resume="):
			resume = strings.TrimPrefix(a, "--resume=")
		case a == "--prompt":
			if i+1 >= len(args) {
				return errors.New("--prompt requires a value")
			}
			initialPrompt = args[i+1]
			i++
		case strings.HasPrefix(a, "--prompt="):
			initialPrompt = strings.TrimPrefix(a, "--prompt=")
		default:
			positional = append(positional, a)
		}
	}
	usage := "usage: ticket agent [<id>] [--prompt \"<text>\"] [--resume <session-id> | --dry-run]"
	if mode == agent.ModeSpec {
		usage = "usage: ticket spec [<id>] [--prompt \"<text>\"] [--resume <session-id> | --dry-run]"
	}
	if dryRun && resume != "" {
		return errors.New("--dry-run and --resume are mutually exclusive")
	}
	if initialPrompt != "" && resume != "" {
		return errors.New("--prompt and --resume are mutually exclusive")
	}
	if initialPrompt != "" && dryRun {
		return errors.New("--prompt and --dry-run are mutually exclusive")
	}
	id, _, err := resolveID(ticket, positional, 0, usage)
	if err != nil {
		return err
	}
	if dryRun {
		prompt, err := agent.DryRunPrompt(db, beadsDir, id, mode)
		if err != nil {
			return err
		}
		_, err = io.WriteString(stdout, prompt)
		return err
	}
	// `ticket agent <blocked-id>` (fresh launch, agent mode) cascades into
	// ready-stamping + launching unblocked roots instead of failing the
	// claim. Resumes and spec sessions skip this — they don't claim either.
	// See internal/ready for the rules.
	if mode == agent.ModeAgent && resume == "" {
		handled, err := ready.HandleBlocked(db, beadsDir, id, stdout)
		if err != nil {
			return err
		}
		if handled {
			return nil
		}
	}
	return agent.RunWithOptions(db, beadsDir, id, mode, agent.RunOptions{
		ResumeSessionID: resume,
		InitialPrompt:   initialPrompt,
	})
}

// CwdForDiscovery returns the directory `agent continue` / `agent resume`
// start their .ticket-id walk-up from. Exposed as a var so tests can pin a
// tmpdir instead of inheriting the test process's cwd.
var CwdForDiscovery = os.Getwd

// runAgentContinue handles `ticket agent continue [<id>]` (sub == "continue"),
// `ticket agent resume [<id>]` (sub == "resume") and `ticket agent fork
// [<id>]` (sub == "fork") — the commands the machine-wide `cc` / `cr` / `cf`
// aliases point at.
//
// Ticket resolution is deliberately laxer than every other subcommand: an
// explicit --ticket / <id> wins, but with neither we fall back to the
// .ticket-id marker discovered by walking up from the cwd (the same marker
// `this` uses). Not being inside a ticket env is not an error — it means the
// alias was used somewhere ordinary, so we hand over to a bare claude launch
// and the pre-framework behaviour is preserved.
func runAgentContinue(db, beadsDir, ticket, sub string, args []string) error {
	usage := fmt.Sprintf("usage: ticket agent %s [<id>]", sub)
	id, _, err := resolveOptionalID(ticket, args, 0, usage)
	if err != nil {
		return err
	}
	if id == "" {
		cwd, err := CwdForDiscovery()
		if err != nil {
			return err
		}
		discovered, _, err := env.Discover(cwd)
		if err != nil && !errors.Is(err, env.ErrNoTicketEnv) {
			return err
		}
		if discovered == "" {
			// Not in a ticket env: be `claude --continue` / `claude --resume`
			// / `claude --resume --fork-session`.
			if sub == "fork" {
				return agent.ExecClaude([]string{"--resume", "--fork-session"})
			}
			return agent.ExecClaude([]string{"--" + sub})
		}
		id = discovered
	}
	if sub == "continue" {
		return agent.ContinueLatest(db, beadsDir, id)
	}
	// Build the picker's input with the `agent list` renderer itself so the
	// rows are, by construction, the rows `this agent list` prints.
	var table bytes.Buffer
	if err := next.RunAgent(db, beadsDir, id, 0, 0, false, next.StateFilterNone, false, &table); err != nil {
		return err
	}
	if sub == "fork" {
		err = agent.PickAndFork(db, beadsDir, id, table.String())
	} else {
		err = agent.PickAndResume(db, beadsDir, id, table.String())
	}
	if errors.Is(err, agent.ErrPickCancelled) {
		return nil
	}
	return err
}

// runAgentStatus handles `ticket agent status [<id>] <session-id> [--json]`.
// Prints the matched record's full metadata plus the untruncated waiting
// reason. Lookup is strict — a typo errors with the list of valid session
// ids on the ticket, matching the shape used by `agent attach`/`agent delete`.
func runAgentStatus(db, beadsDir, ticket string, args []string, stdout io.Writer) error {
	usage := "usage: ticket agent status [<id>] <session-id> [--json]"
	var jsonOut bool
	positional := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--json" {
			jsonOut = true
			continue
		}
		positional = append(positional, a)
	}
	id, tail, err := resolveID(ticket, positional, 1, usage)
	if err != nil {
		return err
	}
	return agent.Status(db, beadsDir, id, tail[0], jsonOut, stdout)
}

// runAgentDelete handles `ticket agent delete [<id>] <session-id>... [--force]`.
// At least one session id is required. With --force a still-running record can
// be deleted (the underlying process is untouched either way).
func runAgentDelete(db, beadsDir, ticket string, args []string) error {
	usage := "usage: ticket agent delete [<id>] <session-id>... [--force]"
	var force bool
	positional := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--force" {
			force = true
			continue
		}
		positional = append(positional, a)
	}
	id, tail, err := resolveIDVariadic(ticket, positional, usage)
	if err != nil {
		return err
	}
	if len(tail) == 0 {
		return errors.New(usage)
	}
	return agent.DeleteAgents(db, beadsDir, id, tail, force)
}

// runAgentSetStatus handles
// `ticket agent set-status <state> [--session <id>] [--source <kind>]`.
// The `ticket` argument is the resolved value of the global --ticket flag and
// scopes the lookup when provided; otherwise the agent record is located by
// session id across all open beads. When --session is omitted (the hook
// invocation path) session_id is parsed out of the Claude Code hook JSON
// payload on stdin.
//
// --source selects how the matching conversation entry is built from the
// stdin payload:
//
//   - "" (default): Stop hook — the run of assistant text segments is
//     reconciled from the transcript (no header entry).
//   - "ask-user-question": PreToolUse payload's raw `tool_input` JSON
//     (questions+options) is stored verbatim in an `ask_user_question` entry.
//   - "exit-plan-mode": PreToolUse payload's `tool_input.plan` markdown
//     becomes an `exit_plan_mode` entry.
//   - "stop-failure": StopFailure payload's `matcher` (error type) and
//     `message` become a `stop_failure` entry.
func runAgentSetStatus(db, beadsDir, ticket string, args []string) error {
	usage := "usage: ticket agent set-status <running|waiting> [--session <id>] [--source <kind>]"
	var session, source string
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--session":
			if i+1 >= len(args) {
				return errors.New(usage)
			}
			session = args[i+1]
			i++
		case strings.HasPrefix(a, "--session="):
			session = strings.TrimPrefix(a, "--session=")
		case a == "--source":
			if i+1 >= len(args) {
				return errors.New(usage)
			}
			source = args[i+1]
			i++
		case strings.HasPrefix(a, "--source="):
			source = strings.TrimPrefix(a, "--source=")
		default:
			positional = append(positional, a)
		}
	}
	if len(positional) != 1 {
		return errors.New(usage)
	}
	status := positional[0]
	if status != agent.StatusRunning && status != agent.StatusWaiting {
		return fmt.Errorf("status must be %q or %q, got %q",
			agent.StatusRunning, agent.StatusWaiting, status)
	}
	switch source {
	case "",
		agent.WaitingReasonSourceAskUserQuestion,
		agent.WaitingReasonSourceExitPlanMode,
		agent.WaitingReasonSourceStopFailure:
	default:
		return fmt.Errorf("unknown --source %q (want one of: %s, %s, %s)", source,
			agent.WaitingReasonSourceAskUserQuestion,
			agent.WaitingReasonSourceExitPlanMode,
			agent.WaitingReasonSourceStopFailure)
	}
	fromHook := false
	var contextTokens int
	var payload *hookStdinPayload
	if session == "" {
		p, err := hookPayloadFromStdin()
		if err != nil {
			return fmt.Errorf("read hook payload: %w", err)
		}
		if p != nil {
			payload = p
			session = p.SessionID
			// Prefer an explicit context_tokens field if the caller set
			// one (manual or future-wrapper invocation); otherwise derive
			// it from the transcript Claude Code points us at.
			if p.ContextTokens > 0 {
				contextTokens = p.ContextTokens
			} else if p.TranscriptPath != "" {
				n, err := agent.ContextTokensFromTranscript(p.TranscriptPath)
				if err == nil {
					contextTokens = n
				}
				// On read error we just skip the token update — a stale
				// or missing transcript shouldn't fail the status flip.
			}
		}
		fromHook = true
	}
	if session == "" {
		return errors.New("no --session id supplied and stdin payload did not contain session_id")
	}
	err := agent.SetStatus(db, beadsDir, session, status, ticket, contextTokens)
	// Hook-driven invocations fire for every claude session, not just those
	// launched via `ticket agent` — so a missing record there is expected,
	// not an error. Explicit --session calls (manual testing backstop) still
	// surface the error so typos don't silently succeed.
	if err != nil {
		if fromHook && errors.Is(err, agent.ErrSessionNotFound) {
			return nil
		}
		return err
	}
	// Subagent invocations (Claude Code's task-spawned helpers) carry an
	// agent_id field. Conversation accumulation is main-session-only per
	// ai-vrc.47 scope, so skip the append in that case. The status flip
	// above is still legitimate — the wrapper's status is whatever the
	// last set-status said, regardless of which session typed the prompt.
	if payload == nil || payload.AgentID != "" {
		return nil
	}
	// Build per-source conversation entries and reconcile any new assistant
	// text segments from the transcript. AppendConversation is a silent
	// no-op when no record matches.
	entries := buildConversationEntries(status, source, payload, contextTokens)
	var segs []agent.TranscriptText
	finalAsReply := false
	if payload.TranscriptPath != "" {
		if s, err := agent.AssistantTextSegments(payload.TranscriptPath); err == nil {
			segs = s
		}
		// Stop hook (waiting + empty source) ends the turn: the final
		// transcript text segment is the assistant_reply.
		if status == agent.StatusWaiting && source == "" {
			finalAsReply = true
		}
	}
	aerr := agent.AppendConversation(db, beadsDir, session, ticket, segs, finalAsReply, entries, contextTokens)
	if aerr != nil && !errors.Is(aerr, agent.ErrSessionNotFound) {
		return aerr
	}
	return nil
}

// buildConversationEntries returns the per-source conversation entries to
// append for one hook firing. Transcript-derived assistant_comment entries
// are produced separately by AppendConversation; this function returns only
// the "header" entry that the hook event introduces (user, ask_user_question,
// exit_plan_mode, stop_failure). The Stop hook contributes no header entry —
// its content is exactly the run of assistant text segments AppendConversation
// reconciles from the transcript.
func buildConversationEntries(status, source string, p *hookStdinPayload, contextTokens int) []agent.ConvEntry {
	if status == agent.StatusRunning {
		if p.Prompt == "" {
			return nil
		}
		return []agent.ConvEntry{{
			Type:          agent.ConvTypeUser,
			Prompt:        p.Prompt,
			ContextTokens: contextTokens,
		}}
	}
	switch source {
	case agent.WaitingReasonSourceAskUserQuestion:
		input := json.RawMessage(p.ToolInput)
		if len(input) == 0 || string(input) == "null" {
			input = nil
		}
		return []agent.ConvEntry{{
			Type:          agent.ConvTypeAskUserQuestion,
			Input:         input,
			ContextTokens: contextTokens,
		}}
	case agent.WaitingReasonSourceExitPlanMode:
		var ti struct {
			Plan string `json:"plan"`
		}
		if len(p.ToolInput) > 0 {
			_ = json.Unmarshal(p.ToolInput, &ti)
		}
		return []agent.ConvEntry{{
			Type:          agent.ConvTypeExitPlanMode,
			Plan:          ti.Plan,
			ContextTokens: contextTokens,
		}}
	case agent.WaitingReasonSourceStopFailure:
		return []agent.ConvEntry{{
			Type:          agent.ConvTypeStopFailure,
			Matcher:       p.Matcher,
			Message:       p.Message,
			ContextTokens: contextTokens,
		}}
	default:
		// Stop hook: no header entry — AppendConversation reconciles the
		// transcript and produces the trailing assistant_comment* +
		// assistant_reply run.
		return nil
	}
}

// hookStdinPayload is the subset of the Claude Code hook payload we read
// from stdin. transcript_path is Claude's path to the session JSONL — we
// tail it to derive the agent's current context-window size and reconcile
// assistant text segments. tool_input is set on PreToolUse payloads and
// carries the in-flight tool call's JSON (we forward it as the entry payload
// for AskUserQuestion / ExitPlanMode). matcher + message are set on
// StopFailure payloads and identify the API error class plus its detail.
// context_tokens is not part of the native Claude Code payload; it's a
// passthrough hatch for tests and any future wrapper that wants to inject
// a pre-computed value.
type hookStdinPayload struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	ToolInput      json.RawMessage `json:"tool_input"`
	Matcher        string          `json:"matcher"`
	Message        string          `json:"message"`
	ContextTokens  int             `json:"context_tokens"`
	Prompt         string          `json:"prompt"`
	AgentID        string          `json:"agent_id"`
}

// hookPayloadFromStdin parses Claude Code's hook payload from os.Stdin.
// Returns (nil, nil) when no payload is attached (interactive stdin or
// empty body) so callers can fall back to other lookups.
func hookPayloadFromStdin() (*hookStdinPayload, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeCharDevice != 0 {
		return nil, nil
	}
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, nil
	}
	var payload hookStdinPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse hook payload: %w", err)
	}
	return &payload, nil
}

func runCreate(db, beadsDir, ticket string, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: ticket create <child|followup> [<id>] [--not-blocked] <title> --description <text>")
	}
	sub, rest := args[0], args[1:]

	// Extract --not-blocked / --description flags from anywhere in rest;
	// surrounding tokens are positionals (id, title) handled by resolveID
	// below.
	var (
		notBlocked     bool
		description    string
		descriptionSet bool
	)
	positional := make([]string, 0, len(rest))
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "--not-blocked":
			notBlocked = true
		case a == "--description":
			if i+1 >= len(rest) {
				return errors.New("--description requires a value")
			}
			description = rest[i+1]
			descriptionSet = true
			i++
		case strings.HasPrefix(a, "--description="):
			description = strings.TrimPrefix(a, "--description=")
			descriptionSet = true
		default:
			positional = append(positional, a)
		}
	}
	if !descriptionSet {
		return errors.New("--description <text> is required")
	}
	if description == "" {
		return errors.New("--description value must not be empty")
	}

	var rel create.Relation
	switch sub {
	case "child":
		if notBlocked {
			return errors.New("--not-blocked is only valid for `create followup`")
		}
		rel = create.Child
	case "followup":
		rel = create.Followup
	default:
		return fmt.Errorf("unknown create relation %q (want child|followup)", sub)
	}
	id, tail, err := resolveID(ticket, positional, 1, fmt.Sprintf("usage: ticket create %s [<id>] [--not-blocked] <title> --description <text>", sub))
	if err != nil {
		return err
	}
	return create.Run(db, beadsDir, id, tail[0], description, rel, !notBlocked, stdout)
}

func runReady(db, beadsDir, ticket string, args []string) error {
	id, _, err := resolveID(ticket, args, 0, "usage: ticket ready [<id>]")
	if err != nil {
		return err
	}
	return ready.Set(db, beadsDir, id)
}

func runNotReady(db, beadsDir, ticket string, args []string) error {
	id, _, err := resolveID(ticket, args, 0, "usage: ticket notready [<id>]")
	if err != nil {
		return err
	}
	return ready.Remove(db, beadsDir, id)
}

func runHandover(db, beadsDir, ticket string, args []string, stdout io.Writer) error {
	var output string
	positional := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--output":
			if i+1 >= len(args) {
				return errors.New("--output requires a value")
			}
			output = args[i+1]
			i++
		case strings.HasPrefix(a, "--output="):
			output = strings.TrimPrefix(a, "--output=")
		default:
			positional = append(positional, a)
		}
	}
	id, _, err := resolveID(ticket, positional, 0, "usage: ticket handover [<id>] [--output <path>]")
	if err != nil {
		return err
	}
	return handover.Run(db, beadsDir, id, output, stdout)
}

func runFinish(db, beadsDir, ticket string, args []string, stdout io.Writer) error {
	var continueMode bool
	positional := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--continue" {
			continueMode = true
			continue
		}
		positional = append(positional, a)
	}
	id, _, err := resolveID(ticket, positional, 0, "usage: ticket finish [<id>] [--continue]")
	if err != nil {
		return err
	}
	return finish.Run(db, beadsDir, id, continueMode, stdout)
}

func runTeardown(db, beadsDir, ticket string, args []string, stdout io.Writer) error {
	id, _, err := resolveID(ticket, args, 0, "usage: ticket teardown [<id>]")
	if err != nil {
		return err
	}
	return teardown.Run(db, beadsDir, id, stdout)
}

func runPipelineAsync(db, beadsDir string, args []string, stdout io.Writer) error {
	var payload string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--payload":
			if i+1 >= len(args) {
				return errors.New("pipeline-async: --payload needs a value")
			}
			payload = args[i+1]
			i++
		case strings.HasPrefix(a, "--payload="):
			payload = strings.TrimPrefix(a, "--payload=")
		default:
			return fmt.Errorf("pipeline-async: unknown argument %q", a)
		}
	}
	return finish.RunAsync(db, beadsDir, payload, stdout)
}

func runTerminateAsync(args []string, stdout io.Writer) error {
	var tty string
	var wrapperPID int
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--tty":
			if i+1 >= len(args) {
				return errors.New("terminate-async: --tty needs a value")
			}
			tty = args[i+1]
			i++
		case strings.HasPrefix(a, "--tty="):
			tty = strings.TrimPrefix(a, "--tty=")
		case a == "--wrapper-pid":
			if i+1 >= len(args) {
				return errors.New("terminate-async: --wrapper-pid needs a value")
			}
			pid, err := strconv.Atoi(args[i+1])
			if err != nil {
				return fmt.Errorf("terminate-async: --wrapper-pid not a number: %w", err)
			}
			wrapperPID = pid
			i++
		case strings.HasPrefix(a, "--wrapper-pid="):
			pid, err := strconv.Atoi(strings.TrimPrefix(a, "--wrapper-pid="))
			if err != nil {
				return fmt.Errorf("terminate-async: --wrapper-pid not a number: %w", err)
			}
			wrapperPID = pid
		default:
			return fmt.Errorf("terminate-async: unknown argument %q", a)
		}
	}
	return finish.RunTerminateAsync(wrapperPID, tty, stdout)
}

func runFinishPoll(db, beadsDir string, args []string, stdout io.Writer) error {
	var ticketID string
	intervalSeconds := 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--ticket":
			if i+1 >= len(args) {
				return errors.New("finish-poll: --ticket needs a value")
			}
			ticketID = args[i+1]
			i++
		case strings.HasPrefix(a, "--ticket="):
			ticketID = strings.TrimPrefix(a, "--ticket=")
		case a == "--interval":
			if i+1 >= len(args) {
				return errors.New("finish-poll: --interval needs a value")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return fmt.Errorf("finish-poll: --interval not a number: %w", err)
			}
			intervalSeconds = n
			i++
		case strings.HasPrefix(a, "--interval="):
			n, err := strconv.Atoi(strings.TrimPrefix(a, "--interval="))
			if err != nil {
				return fmt.Errorf("finish-poll: --interval not a number: %w", err)
			}
			intervalSeconds = n
		default:
			return fmt.Errorf("finish-poll: unknown argument %q", a)
		}
	}
	return finish.RunPoll(db, beadsDir, ticketID, intervalSeconds, stdout)
}

func runNext(db, beadsDir, ticket string, args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "agent" {
		return runNextAgent(db, beadsDir, ticket, args[1:], stdout)
	}
	id, _, err := resolveOptionalID(ticket, args, 0, "usage: ticket next [<id>]")
	if err != nil {
		return err
	}
	return next.Run(db, beadsDir, id, stdout, stderr)
}

// runNextAgent handles
// `ticket next agent [<id>] [--all] [--min-recursion-depth <M>] [--recursion-depth <N>] [--json]`.
// M defaults to 0 (include the root bead itself); N defaults to unlimited.
// --all flips next.RunAgent's includeRunning flag. --json switches the
// unified renderer to indented-JSON output.
func runNextAgent(db, beadsDir, ticket string, args []string, stdout io.Writer) error {
	usage := "usage: ticket next agent [<id>] [--all] [--min-recursion-depth <M>] [--recursion-depth <N>] [--json]"
	var maxDepth, minDepth int
	var maxSet, minSet bool
	var includeRunning bool
	var jsonOut bool
	positional := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--all":
			includeRunning = true
		case a == "--json":
			jsonOut = true
		case a == "--recursion-depth":
			if i+1 >= len(args) {
				return errors.New(usage)
			}
			n, err := parseDepth("--recursion-depth", args[i+1])
			if err != nil {
				return err
			}
			maxDepth, maxSet = n, true
			i++
		case strings.HasPrefix(a, "--recursion-depth="):
			n, err := parseDepth("--recursion-depth", strings.TrimPrefix(a, "--recursion-depth="))
			if err != nil {
				return err
			}
			maxDepth, maxSet = n, true
		case a == "--min-recursion-depth":
			if i+1 >= len(args) {
				return errors.New(usage)
			}
			n, err := parseDepth("--min-recursion-depth", args[i+1])
			if err != nil {
				return err
			}
			minDepth, minSet = n, true
			i++
		case strings.HasPrefix(a, "--min-recursion-depth="):
			n, err := parseDepth("--min-recursion-depth", strings.TrimPrefix(a, "--min-recursion-depth="))
			if err != nil {
				return err
			}
			minDepth, minSet = n, true
		default:
			positional = append(positional, a)
		}
	}
	id, _, err := resolveOptionalID(ticket, positional, 0, usage)
	if err != nil {
		return err
	}
	if !minSet {
		minDepth = 0
	}
	if !maxSet {
		maxDepth = -1
	} else if maxDepth < minDepth {
		return fmt.Errorf("--recursion-depth (%d) must be >= --min-recursion-depth (%d)", maxDepth, minDepth)
	}
	return next.RunAgent(db, beadsDir, id, minDepth, maxDepth, includeRunning, next.StateFilterWaiting, jsonOut, stdout)
}

func parseDepth(flag, s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer", flag, s)
	}
	if n < 0 {
		return 0, fmt.Errorf("%s: must be >= 0, got %d", flag, n)
	}
	return n, nil
}

// resolveID pulls the ticket id from either the global --ticket flag or the
// first positional. extra is the number of remaining positionals expected
// after the id is consumed. Returns the resolved id and the tail of
// positionals (length == extra). Errors if both forms are supplied or if no
// id is available.
func resolveID(ticket string, args []string, extra int, usage string) (string, []string, error) {
	if ticket != "" {
		if len(args) != extra {
			return "", nil, errors.New(usage)
		}
		return ticket, args, nil
	}
	if len(args) != extra+1 {
		return "", nil, errors.New(usage)
	}
	return args[0], args[1:], nil
}

// resolveIDVariadic is like resolveID but accepts a variable number of
// trailing positionals (zero or more). Used by `relaunch`.
func resolveIDVariadic(ticket string, args []string, usage string) (string, []string, error) {
	if ticket != "" {
		return ticket, args, nil
	}
	if len(args) == 0 {
		return "", nil, errors.New(usage)
	}
	return args[0], args[1:], nil
}

// resolveOptionalID is like resolveID but tolerates a missing id (returns
// the empty string in that case). Used by `next`.
func resolveOptionalID(ticket string, args []string, extra int, usage string) (string, []string, error) {
	if ticket != "" {
		if len(args) != extra {
			return "", nil, errors.New(usage)
		}
		return ticket, args, nil
	}
	switch len(args) {
	case extra:
		return "", args, nil
	case extra + 1:
		return args[0], args[1:], nil
	default:
		return "", nil, errors.New(usage)
	}
}

// runPR dispatches `ticket pr <sub> ...`.
func runPR(db, beadsDir, ticket string, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: ticket pr <list|create|show|link|create-bead|check|refresh> ...")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		id, _, err := resolveID(ticket, rest, 0, "usage: ticket pr list [<id>]")
		if err != nil {
			return err
		}
		return pr.List(db, beadsDir, id, stdout)
	case "create":
		var ready, all bool
		var positional []string
		for _, a := range rest {
			switch a {
			case "--ready":
				ready = true
			case "--all":
				all = true
			default:
				positional = append(positional, a)
			}
		}
		if all {
			usage := "usage: ticket pr create [<id>] --all [--ready]"
			id, _, err := resolveID(ticket, positional, 0, usage)
			if err != nil {
				return err
			}
			return pr.CreateAll(db, beadsDir, id, ready, stdout)
		}
		usage := "usage: ticket pr create [<id>] [<path>] [--ready]"
		id, selector, err := resolveIDOptionalSelector(ticket, positional, usage)
		if err != nil {
			return err
		}
		return pr.Create(db, beadsDir, id, selector, ready, stdout)
	case "show":
		var all bool
		var positional []string
		for _, a := range rest {
			if a == "--all" {
				all = true
				continue
			}
			positional = append(positional, a)
		}
		if all {
			usage := "usage: ticket pr show [<id>] --all"
			id, _, err := resolveID(ticket, positional, 0, usage)
			if err != nil {
				return err
			}
			return pr.ShowAll(db, beadsDir, id, stdout)
		}
		usage := "usage: ticket pr show [<id>] [<path>]"
		id, selector, err := resolveIDOptionalSelector(ticket, positional, usage)
		if err != nil {
			return err
		}
		return pr.Show(db, beadsDir, id, selector, stdout)
	case "link":
		// <pr-url> is optional. When present it is the last positional and an
		// optional <path> sits before it. When absent, the unique open PR for
		// the dir's branch is auto-detected (single dir or, with --all, every
		// eligible dir).
		var all bool
		var positional []string
		for _, a := range rest {
			if a == "--all" {
				all = true
				continue
			}
			positional = append(positional, a)
		}
		if all {
			usage := "usage: ticket pr link [<id>] --all"
			id, _, err := resolveID(ticket, positional, 0, usage)
			if err != nil {
				return err
			}
			return pr.LinkAll(db, beadsDir, id, stdout)
		}
		usage := "usage: ticket pr link [<id>] [<path>] [<pr-url>]"
		// A trailing positional that parses as a GitHub PR URL is the explicit
		// link target; anything else is a <path>/<id>.
		if n := len(positional); n > 0 && looksLikePRURL(positional[n-1]) {
			id, selector, err := resolveIDOptionalSelector(ticket, positional[:n-1], usage)
			if err != nil {
				return err
			}
			return pr.Link(db, beadsDir, id, selector, positional[n-1])
		}
		id, selector, err := resolveIDOptionalSelector(ticket, positional, usage)
		if err != nil {
			return err
		}
		return pr.LinkAuto(db, beadsDir, id, selector, stdout)
	case "create-bead":
		id, tail, err := resolveID(ticket, rest, 1, "usage: ticket pr create-bead [<id>] <pr-url>")
		if err != nil {
			return err
		}
		return pr.CreateBead(db, beadsDir, id, tail[0], stdout)
	case "check":
		var merged bool
		var positional []string
		for _, a := range rest {
			if a == "--merged" {
				merged = true
				continue
			}
			positional = append(positional, a)
		}
		id, _, err := resolveID(ticket, positional, 0, "usage: ticket pr check [<id>] [--merged]")
		if err != nil {
			return err
		}
		return pr.Check(db, beadsDir, id, merged, stdout)
	case "refresh":
		id, _, err := resolveID(ticket, rest, 0, "usage: ticket pr refresh [<id>]")
		if err != nil {
			return err
		}
		return pr.Refresh(db, beadsDir, id, stdout)
	default:
		return fmt.Errorf("unknown pr subcommand %q", sub)
	}
}

// resolveIDOptionalSelector handles `ticket <cmd> [<id>] [<selector>]` shapes
// where the selector is optional. When ticket (global flag) is set, args
// holds zero or one positional (the selector). Otherwise the first
// positional is the id and the second (if present) is the selector.
func resolveIDOptionalSelector(ticket string, args []string, usage string) (id, selector string, err error) {
	if ticket != "" {
		switch len(args) {
		case 0:
		case 1:
			selector = args[0]
		default:
			return "", "", errors.New(usage)
		}
		return ticket, selector, nil
	}
	switch len(args) {
	case 1:
		return args[0], "", nil
	case 2:
		return args[0], args[1], nil
	default:
		return "", "", errors.New(usage)
	}
}

// looksLikePRURL reports whether s parses as a GitHub PR URL. Used by `pr
// link` to tell an explicit <pr-url> from a <path>/<id> positional.
func looksLikePRURL(s string) bool {
	_, _, err := pr.ParseURL(s)
	return err == nil
}

func extractGlobal(args []string) (db, ticket string, rest []string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--db":
			if i+1 >= len(args) {
				return "", "", nil, errors.New("--db requires a value")
			}
			db = args[i+1]
			i++
		case strings.HasPrefix(a, "--db="):
			db = strings.TrimPrefix(a, "--db=")
		case a == "--ticket":
			if i+1 >= len(args) {
				return "", "", nil, errors.New("--ticket requires a value")
			}
			ticket = args[i+1]
			i++
		case strings.HasPrefix(a, "--ticket="):
			ticket = strings.TrimPrefix(a, "--ticket=")
		default:
			rest = append(rest, a)
		}
	}
	return db, ticket, rest, nil
}
