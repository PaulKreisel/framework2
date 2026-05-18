// Package ticketcli implements the `ticket` command-line dispatcher.
//
// It is extracted from cmd/ticket so tests can invoke subcommands in-process
// via [Run] without forking a subprocess. The `this` binary shares this
// dispatcher: it resolves a ticket id from a .ticket-id marker and forwards
// the call via the global `--ticket` flag, so there is no `this`-side
// subcommand knowledge to drift against `ticket`.
package ticketcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"

	"framework2/internal/agent"
	"framework2/internal/bdcli"
	"framework2/internal/create"
	"framework2/internal/dir"
	"framework2/internal/env"
	"framework2/internal/finish"
	"framework2/internal/next"
	"framework2/internal/slug"
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
  dir worktree [<id>] <bare-repo>    create a worktree off <bare-repo> for the
                                     ticket and link it (cascades through
                                     parents so ancestors gain the worktree too)
  dir status [<id>] [<selector>] [--json]
                                     show working-tree status of one linked dir
                                     and (when the parent links the same bare
                                     repo) how its branch has diverged from the
                                     parent's. selector matches path/localRepo
                                     /bareRepo; may be omitted when the ticket
                                     has exactly one linked dir.
  dir slug [<id>] <slug>             set the bead's slug (refused once the slug
                                     has been frozen by a branch/worktree)
  dir slug --ai [<id>]               refine the slug via Claude Haiku in place;
                                     silently no-ops if the slug is frozen
  env [<id>]                              create on-disk env folder for a ticket
  env syncback [<id>]                     push <env>/description.md back to the
                                          bead description (one-way file → bead)
  agent [<id>] [--resume <session-id> | --dry-run]
                                          launch claude from a ticket's env folder
                                          in coding mode. With --resume, claude
                                          reattaches to the named transcript
                                          instead of starting a fresh session;
                                          the existing agent record is updated
                                          in place. With --dry-run, print the
                                          rendered system prompt that would be
                                          appended on a fresh launch and do
                                          nothing else (no env folder, no
                                          claim, no claude). Mutually exclusive
                                          with --resume.
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
  agent relaunch [<id>] [extra-args...]   from inside a session: ask the running
                                          wrapper to re-exec claude with --continue
                                          (use after a dir link/worktree)
  agent --print-default-prompt            print the built-in default coding-mode
                                          prompt template (copy as a starting
                                          point for <BEADS_DIR>/agent-prompt.md)
  agent --list-prompt-fragments           list named fragments operator templates
                                          can interpolate via {{name}}
  spec [<id>] [--dry-run]                 launch claude from a ticket's env folder
                                          in spec mode (edits description.md and
                                          spawns related beads; does not code).
                                          With --dry-run, print the rendered
                                          system prompt that would be appended
                                          on a fresh launch and do nothing else.
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
  finish [<id>]                           wrap up: rebase the ticket's worktrees
                                          onto their parent branch (FF when no
                                          divergence, real rebase otherwise),
                                          close the bead, remove the worktrees,
                                          and delete the env folder. One-way;
                                          never invoked automatically.
  finish [<id>] --continue                resume a finish whose rebase paused on
                                          conflicts: runs ` + "`git rebase --continue`" + ` in
                                          each paused worktree, then completes
                                          the rest of the flow.

WAITING REASON column / waitingReason field:
  Surfaced by ` + "`agent list`" + `, ` + "`agent status`" + ` and ` + "`next agent`" + ` for agents
  in the waiting state. Populated by Claude Code hooks (Stop /
  PreToolUse / StopFailure) via ` + "`agent set-status`" + `; ` + "`UserPromptSubmit`" + `
  clears it on the next user turn. The kind of wait is recorded in
  ` + "`waitingReasonSource`" + ` (omitted in --json when empty, i.e. the legacy
  Stop form):

    ""                  Stop hook — the assistant's final text message
                        for the turn. Table cell shows the message,
                        whitespace-collapsed and truncated.
    ask-user-question   PreToolUse:AskUserQuestion — assistant invoked
                        the multiple-choice prompt tool. waitingReason
                        holds the raw tool_input JSON (questions +
                        options); table cell renders "asking: <first
                        question text>".
    exit-plan-mode      PreToolUse:ExitPlanMode — assistant proposed a
                        plan in plan mode. waitingReason holds the plan
                        markdown body; table cell renders "plan: <first
                        non-empty line>".
    stop-failure        StopFailure — turn ended due to an API error
                        (rate_limit, billing_error, server_error, …).
                        waitingReason holds "<matcher>: <message>" (or
                        just one of them when the other is absent);
                        table cell shows the bare matcher first.

  ` + "`agent list`" + ` and ` + "`next agent`" + ` truncate the cell to one line; use
  ` + "`agent status`" + ` (or --json) for the full payload.`

// Options configures a Run invocation. The zero value uses
// os.Getenv("BEADS_DIR") for BeadsDir, matching the production behaviour of
// the `ticket` binary.
type Options struct {
	// BeadsDir, when non-empty, overrides BEADS_DIR for the duration of this
	// invocation: env folder lookups and child bd subprocess invocations use
	// this value instead of os.Getenv("BEADS_DIR"). Tests use it to drive a
	// per-test .beads/ directory in parallel.
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
		beadsDir = os.Getenv("BEADS_DIR")
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
	case "agent":
		return runAgent(db, beadsDir, ticket, rest[1:], agent.ModeAgent, stdout)
	case "spec":
		return runAgent(db, beadsDir, ticket, rest[1:], agent.ModeSpec, stdout)
	case "create":
		return runCreate(db, beadsDir, ticket, rest[1:], stdout)
	case "next":
		return runNext(db, beadsDir, ticket, rest[1:], stdout, stderr)
	case "finish":
		return runFinish(db, beadsDir, ticket, rest[1:], stdout)
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
		id, tail, err := resolveID(ticket, rest, 1, "usage: ticket dir worktree [<id>] <bare-repo>")
		if err != nil {
			return err
		}
		if err := dir.Worktree(db, beadsDir, id, tail[0]); err != nil {
			return err
		}
		// See the comment under `link` above: cascade-created ancestor
		// worktrees belong to tickets whose agents aren't running, so we
		// only relaunch the explicitly addressed ticket.
		return agent.RelaunchIfActive(beadsDir, id)
	case "slug":
		return runSlug(db, beadsDir, ticket, rest)
	case "status":
		return runDirStatus(db, beadsDir, ticket, rest, stdout)
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
	newSlug := slug.Derive(tail[0], slug.MaxLen)
	if newSlug == "" {
		return fmt.Errorf("slug %q reduces to empty after normalisation", tail[0])
	}
	return slug.Set(db, beadsDir, id, newSlug)
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
	ctx, cancel := context.WithTimeout(context.Background(), slug.RefineTimeout)
	defer cancel()
	improved := slug.Refine(ctx, title, info.Slug)
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
	return enterEnvShell(stdout, envPath)
}

// enterEnvShell drops the caller into an interactive $SHELL rooted at envPath
// so they end up inside the env folder after `ticket env`. A binary cannot cd
// its parent shell, so we exec a child shell instead — `exit` returns them.
//
// Gated on stdout being an *os.File terminal so tests and pipelines (which
// pass a bytes.Buffer or redirect) keep the plain print-and-return behavior.
func enterEnvShell(stdout io.Writer, envPath string) error {
	if !isTerminalWriter(stdout) {
		return nil
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		return nil
	}
	if err := os.Chdir(envPath); err != nil {
		return fmt.Errorf("chdir to env: %w", err)
	}
	return syscall.Exec(shell, []string{shell}, os.Environ())
}

func isTerminalWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
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
	var resume string
	var dryRun bool
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
		default:
			positional = append(positional, a)
		}
	}
	usage := "usage: ticket agent [<id>] [--resume <session-id> | --dry-run]"
	if mode == agent.ModeSpec {
		usage = "usage: ticket spec [<id>] [--resume <session-id> | --dry-run]"
	}
	if dryRun && resume != "" {
		return errors.New("--dry-run and --resume are mutually exclusive")
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
	return agent.RunWithResume(db, beadsDir, id, resume, mode)
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
// --source selects how the waiting reason is built from the stdin payload:
//
//   - "" (default): the Stop hook's `last_assistant_message` — the legacy
//     ai-vrc.23 form.
//   - "ask-user-question": the PreToolUse payload's raw `tool_input` JSON
//     (questions+options) is stored verbatim.
//   - "exit-plan-mode": the PreToolUse payload's `tool_input.plan` markdown
//     body is stored.
//   - "stop-failure": the StopFailure payload's `matcher` (error type),
//     suffixed with `: <message>` when the payload carries one.
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
	var reason string
	if session == "" {
		p, err := hookPayloadFromStdin()
		if err != nil {
			return fmt.Errorf("read hook payload: %w", err)
		}
		if p != nil {
			session = p.SessionID
			reason = waitingReasonFromPayload(source, p)
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
	err := agent.SetStatus(db, beadsDir, session, status, reason, source, ticket, contextTokens)
	// Hook-driven invocations fire for every claude session, not just those
	// launched via `ticket agent` — so a missing record there is expected,
	// not an error. Explicit --session calls (manual testing backstop) still
	// surface the error so typos don't silently succeed.
	if fromHook && errors.Is(err, agent.ErrSessionNotFound) {
		return nil
	}
	return err
}

// waitingReasonFromPayload extracts the per-source waiting-reason payload
// from the parsed hook stdin. The returned string is stored verbatim in
// [agent.AgentEntry.WaitingReason]; the display layer derives the short
// table summary from (source, value) via [agent.FormatWaitingReason].
//
// Returns "" when the relevant field is missing — set-status will then
// clear the stored reason on the flip to running, or store an empty
// reason on the flip to waiting (the source tag still routes the column
// rendering).
func waitingReasonFromPayload(source string, p *hookStdinPayload) string {
	switch source {
	case agent.WaitingReasonSourceAskUserQuestion:
		// Forward the tool_input JSON unchanged — it carries the full
		// questions+options structure. The display layer parses out the
		// first question for the truncated table column.
		if len(p.ToolInput) > 0 && string(p.ToolInput) != "null" {
			return string(p.ToolInput)
		}
		return ""
	case agent.WaitingReasonSourceExitPlanMode:
		var ti struct {
			Plan string `json:"plan"`
		}
		if len(p.ToolInput) > 0 {
			_ = json.Unmarshal(p.ToolInput, &ti)
		}
		return ti.Plan
	case agent.WaitingReasonSourceStopFailure:
		// Matcher is the error type ("rate_limit", "billing_error", …);
		// message is the human-readable detail. Compose
		// "<matcher>: <message>" when both are present, otherwise just
		// whichever one we have.
		if p.Matcher != "" && p.Message != "" {
			return p.Matcher + ": " + p.Message
		}
		if p.Matcher != "" {
			return p.Matcher
		}
		return p.Message
	default:
		// Legacy Stop-hook form: assistant's final reply for the turn.
		return p.LastAssistantMessage
	}
}

// hookStdinPayload is the subset of the Claude Code hook payload we read
// from stdin. transcript_path is Claude's path to the session JSONL — we
// tail it to derive the agent's current context-window size.
// last_assistant_message is set on Stop payloads and holds the assistant's
// final reply for the turn. tool_input is set on PreToolUse payloads and
// carries the in-flight tool call's JSON (we forward it as the waiting
// reason for AskUserQuestion / ExitPlanMode). matcher + message are set on
// StopFailure payloads and identify the API error class plus its detail.
// context_tokens is not part of the native Claude Code payload; it's a
// passthrough hatch for tests and any future wrapper that wants to inject
// a pre-computed value.
type hookStdinPayload struct {
	SessionID            string          `json:"session_id"`
	TranscriptPath       string          `json:"transcript_path"`
	LastAssistantMessage string          `json:"last_assistant_message"`
	ToolInput            json.RawMessage `json:"tool_input"`
	Matcher              string          `json:"matcher"`
	Message              string          `json:"message"`
	ContextTokens        int             `json:"context_tokens"`
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
