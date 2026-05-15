// Package agent implements the `ticket agent` and `ticket spec` subcommands:
// launching an AI agent (currently always `claude`) from a ticket's on-disk
// env folder, with the env's auto-managed context files regenerated on every
// launch and a small mode-specific system prompt appended.
package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"framework2/internal/bdcli"
	"framework2/internal/dir"
	"framework2/internal/env"
)

// AgentCommand is the binary execed by Run. Exported as a var so future work
// can swap it (e.g. a per-ticket choice) without further plumbing.
var AgentCommand = "claude"

// AgentDefaultArgs are passed to AgentCommand on every iteration of the
// wrapper loop, before any relaunch-supplied args. Tests override this to
// substitute a shell script for the real claude.
var AgentDefaultArgs = []string{"--dangerously-skip-permissions"}

// Mode selects which system prompt the wrapper appends and is included in
// description.md so the agent can tell which surface launched it.
type Mode int

const (
	// ModeAgent is the default coding surface: writes source, runs tests.
	ModeAgent Mode = iota
	// ModeSpec is the spec'ing surface: edits description.md and spawns
	// related beads via `ticket create ...`; does not write source.
	ModeSpec
)

// DescriptionFile is the basename of the synced description file inside the
// env folder. It mirrors the bead description on launch and is pushed back
// by `this env syncback` / `ticket env syncback`.
const DescriptionFile = "description.md"

// Run is the fresh-launch variant of [RunWithResume]: it generates a new
// session id, claims the bead, and lets claude allocate the session via
// `--session-id`. See [RunWithResume] for the resume-on-existing-transcript
// path used by [Attach].
func Run(db, beadsDir, ticketID string, mode Mode) error {
	return RunWithResume(db, beadsDir, ticketID, "", mode)
}

// RunWithResume resolves the env folder for ticketID — creating it via
// [env.Create] if it doesn't exist yet — regenerates the auto-managed context
// files (description.md, .claude/settings.json), and then execs AgentCommand
// from inside the env folder in a loop: after each exit, if a [SignalFile] is
// present, the loop regenerates context and re-execs AgentCommand with
// `--continue` plus the signal's args appended.
//
// When resumeSessionID is empty (the common path used by `ticket agent`), a
// fresh session id is generated, the bead is claimed (unless mode is
// [ModeSpec] — `ticket spec` doesn't own the status transition), and the
// first claude invocation passes `--session-id <newID>` so the id is stable
// from the start.
//
// When resumeSessionID is non-empty (the path used by [Attach] for an exited
// agent), the bead is NOT re-claimed (it's already in_progress from the prior
// wrapper), the existing agent record for that session id is updated in
// place, and the first claude invocation passes `--resume <sessionID>` so it
// reattaches to the same transcript instead of starting a new one.
//
// While AgentCommand is running, its pid is recorded at <env>/[PIDFile] so
// [Relaunch] can target it.
func RunWithResume(db, beadsDir, ticketID, resumeSessionID string, mode Mode) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	if beadsDir == "" {
		return errors.New("BEADS_DIR not set: cannot derive env root")
	}
	envPath, err := env.Create(db, beadsDir, ticketID)
	if err != nil {
		return fmt.Errorf("create env dir: %w", err)
	}

	if resumeSessionID == "" && mode != ModeSpec {
		if err := Claim(db, beadsDir, ticketID); err != nil {
			return err
		}
	}

	sigPath := filepath.Join(envPath, SignalFile)
	pidPath := filepath.Join(envPath, PIDFile)
	// Drop any stale signal/pid file before starting; otherwise the very
	// first iteration would relaunch on a leftover from a prior session.
	_ = os.Remove(sigPath)
	_ = os.Remove(pidPath)

	sessionID := resumeSessionID
	if sessionID == "" {
		sessionID, err = NewSessionID()
		if err != nil {
			return fmt.Errorf("generate session id: %w", err)
		}
	}
	startedAt := time.Now().UTC().Format(time.RFC3339)
	if err := UpsertAgent(db, beadsDir, ticketID, AgentEntry{
		SessionID:       sessionID,
		PID:             os.Getpid(),
		Mode:            mode.String(),
		StartedAt:       startedAt,
		Env:             envPath,
		Status:          StatusRunning,
		StatusUpdatedAt: startedAt,
	}); err != nil {
		return fmt.Errorf("register agent: %w", err)
	}
	if err := InstallStatusHooks(); err != nil {
		return fmt.Errorf("install status hooks: %w", err)
	}

	var extraArgs []string
	for {
		prompt, err := Regenerate(db, beadsDir, envPath, ticketID, mode)
		if err != nil {
			return fmt.Errorf("regenerate env context: %w", err)
		}
		argv := append([]string{}, AgentDefaultArgs...)
		argv = append(argv, "--append-system-prompt", prompt)
		// First iteration: pin claude to our chosen session id, either by
		// having it allocate a fresh session under that id (`--session-id`)
		// or by reattaching to an existing transcript (`--resume`).
		// Subsequent iterations pass --continue (via extraArgs) and inherit
		// the same session.
		if len(extraArgs) == 0 {
			if resumeSessionID != "" {
				argv = append(argv, "--resume", sessionID)
			} else {
				argv = append(argv, "--session-id", sessionID)
			}
		}
		argv = append(argv, extraArgs...)
		cmd := exec.Command(AgentCommand, argv...)
		cmd.Dir = envPath
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			return err
		}
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
			_ = cmd.Process.Kill()
			return fmt.Errorf("write pid file: %w", err)
		}
		waitErr := cmd.Wait()
		_ = os.Remove(pidPath)

		sigArgs, hasSignal, err := readSignal(sigPath)
		if err != nil {
			return err
		}
		if !hasSignal {
			return waitErr
		}
		extraArgs = append([]string{"--continue"}, sigArgs...)
	}
}

// Regenerate rewrites the env's auto-managed context files from the current
// state of the ticket and its linked dirs, and returns the system prompt the
// wrapper should pass via --append-system-prompt. Idempotent. Exported so
// tests can drive the regen step without forking AgentCommand.
func Regenerate(db, beadsDir, envPath, ticketID string, mode Mode) (string, error) {
	issue, err := loadIssue(db, beadsDir, ticketID)
	if err != nil {
		return "", err
	}
	dirs, err := dir.Load(db, beadsDir, ticketID)
	if err != nil {
		return "", err
	}
	ancestorDirs, err := dir.LoadAncestorEntries(db, beadsDir, ticketID)
	if err != nil {
		return "", err
	}
	if err := writeDescription(envPath, issue); err != nil {
		return "", err
	}
	if err := writeSettings(envPath, dirs); err != nil {
		return "", err
	}
	return BuildSystemPrompt(issue.ID, issue.Title, mode, dirs, ancestorDirs), nil
}

type bdIssue struct {
	ID           string         `json:"id"`
	Title        string         `json:"title"`
	Description  string         `json:"description"`
	Status       string         `json:"status"`
	Dependencies []bdDependency `json:"dependencies"`
}

type bdDependency struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	DependencyType string `json:"dependency_type"`
}

// Claim marks ticketID as actively being worked on by the current user via
// `bd update --claim` (sets assignee to the current user, status to
// in_progress). Refuses if the ticket is already in_progress, or if any of
// its `blocks`-type dependencies are still open.
func Claim(db, beadsDir, ticketID string) error {
	issue, err := loadIssue(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	if issue.Status == "in_progress" {
		return fmt.Errorf("ticket %s is already in_progress; refusing to claim", ticketID)
	}
	for _, dep := range issue.Dependencies {
		if dep.DependencyType == "blocks" && dep.Status != "closed" {
			return fmt.Errorf("ticket %s is blocked by %s (status %s); refusing to claim", ticketID, dep.ID, dep.Status)
		}
	}
	out, err := bdcli.Cmd(beadsDir, db, "update", ticketID, "--claim").CombinedOutput()
	if err != nil {
		return fmt.Errorf("bd update %s --claim: %v: %s", ticketID, err, out)
	}
	return nil
}

func loadIssue(db, beadsDir, ticketID string) (*bdIssue, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "show", ticketID).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("bd show %s: %v: %s", ticketID, err, ee.Stderr)
		}
		return nil, fmt.Errorf("bd show %s: %w", ticketID, err)
	}
	var rows []bdIssue
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse bd show output for %s: %w", ticketID, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("ticket not found: %s", ticketID)
	}
	return &rows[0], nil
}

func writeDescription(envPath string, issue *bdIssue) error {
	body := strings.TrimRight(issue.Description, "\n") + "\n"
	return os.WriteFile(filepath.Join(envPath, DescriptionFile), []byte(body), 0o644)
}

// BuildSystemPrompt returns the mode-specific text appended to claude's
// system prompt for this launch. Lists the in-session commands and what
// they're for; description.md holds the actual task content. dirs are the
// current bead's linked dirs and ancestorDirs are the union of ancestors'
// linked dirs — they're used to render a "Repos in this session" block so
// the agent knows what's wired up and what an ancestor already pulled in.
func BuildSystemPrompt(ticketID, title string, mode Mode, dirs, ancestorDirs []dir.Entry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ticket %s — %s\n\n", ticketID, title)
	switch mode {
	case ModeSpec:
		b.WriteString("You are spec'ing this ticket, not coding. Do not edit source files.\n")
		b.WriteString("Read description.md first.\n\n")
		b.WriteString("## Update the description of this ticket\n")
		b.WriteString("* Modify the description.md file\n")
		b.WriteString("* Run `this env syncback`\n\n")
		b.WriteString("## Delegating work\n")
		b.WriteString("### Within this ticket: create a child\n")
		b.WriteString("`this create child <title>`\n\n")
		b.WriteString("### For later\n")
		b.WriteString("`this create followup <title>` — sibling of this ticket, blocked by it\n")
		b.WriteString("`this create followup --not-blocked <title>` — sibling of this ticket, not blocked\n")
	default:
		b.WriteString("Execute the task described in description.md. Read it first.\n\n")
		b.WriteString("## Delegating work for later\n")
		b.WriteString("`this create followup <title>` — sibling of this ticket, blocked by it\n")
		b.WriteString("`this create followup --not-blocked <title>` — sibling of this ticket, not blocked\n\n")
		b.WriteString("## Adding a directory to this session\n")
		b.WriteString("`this dir worktree <bare-repo>` (or `this dir link <path>`). The wrapper auto-relaunches claude so the new dir is in scope.\n\n")
		b.WriteString("(Side note: if you intentionally edit description.md, run `this env syncback` to persist it back to the bead.)\n")
	}
	writeReposSection(&b, dirs, ancestorDirs)
	return b.String()
}

// writeReposSection appends a "Repos in this session" block listing the
// bare-repo-backed dirs currently linked to the bead and, when an ancestor
// touches a repo the current bead does not yet have, an actionable bullet of
// repo name and bare path so the agent can run `this dir worktree <path>`.
// Entries without a BareRepo are skipped — only true bare-repo-backed entries
// count as "repos" here.
func writeReposSection(b *strings.Builder, dirs, ancestorDirs []dir.Entry) {
	currentRepos := bareReposFrom(dirs)
	currentBares := make(map[string]struct{}, len(currentRepos))
	for _, r := range currentRepos {
		currentBares[r.bare] = struct{}{}
	}
	parentRepos := bareReposFromExcluding(ancestorDirs, currentBares)
	if len(currentRepos) == 0 && len(parentRepos) == 0 {
		return
	}
	b.WriteString("\n## Repos in this session\n\n")
	if len(currentRepos) == 0 {
		b.WriteString("No repos linked yet.\n")
	} else {
		names := make([]string, len(currentRepos))
		for i, r := range currentRepos {
			names[i] = r.name
		}
		fmt.Fprintf(b, "Repos linked into this session: %s.\n", strings.Join(names, ", "))
	}
	if len(parentRepos) > 0 {
		b.WriteString("\nParent tickets touch the following repos (add with `this dir worktree <path>`):\n\n")
		for _, r := range parentRepos {
			fmt.Fprintf(b, "- %s — `%s`\n", r.name, r.bare)
		}
	}
}

// bareRepoRef is one repo surfaced in the system prompt: the bare path is its
// identity (also the argument an agent would pass to `this dir worktree`) and
// name is the human-readable basename.
type bareRepoRef struct {
	bare string
	name string
}

// bareReposFrom returns the bare repos backing entries, in entry order, with
// duplicates collapsed by bare path. Entries without BareRepo are omitted —
// per ai-02h we only surface bare-repo-backed entries here.
func bareReposFrom(entries []dir.Entry) []bareRepoRef {
	seen := make(map[string]struct{})
	var out []bareRepoRef
	for _, e := range entries {
		if e.BareRepo == "" {
			continue
		}
		if _, dup := seen[e.BareRepo]; dup {
			continue
		}
		seen[e.BareRepo] = struct{}{}
		out = append(out, bareRepoRef{bare: e.BareRepo, name: repoNameFromBarePath(e.BareRepo)})
	}
	return out
}

func bareReposFromExcluding(entries []dir.Entry, exclude map[string]struct{}) []bareRepoRef {
	seen := make(map[string]struct{})
	var out []bareRepoRef
	for _, e := range entries {
		if e.BareRepo == "" {
			continue
		}
		if _, x := exclude[e.BareRepo]; x {
			continue
		}
		if _, dup := seen[e.BareRepo]; dup {
			continue
		}
		seen[e.BareRepo] = struct{}{}
		out = append(out, bareRepoRef{bare: e.BareRepo, name: repoNameFromBarePath(e.BareRepo)})
	}
	return out
}

// repoNameFromBarePath turns "/abs/path/to/framework2.git" into "framework2".
func repoNameFromBarePath(p string) string {
	return strings.TrimSuffix(filepath.Base(p), ".git")
}

// SettingsPayload mirrors the .claude/settings.json structure we write.
// Exported so tests can decode it without redeclaring the schema.
type SettingsPayload struct {
	Permissions struct {
		AdditionalDirectories []string `json:"additionalDirectories"`
	} `json:"permissions"`
}

func writeSettings(envPath string, dirs []dir.Entry) error {
	settingsDir := filepath.Join(envPath, ".claude")
	if err := os.MkdirAll(settingsDir, 0o755); err != nil {
		return fmt.Errorf("create .claude dir: %w", err)
	}
	paths := make([]string, 0, len(dirs))
	for _, e := range dirs {
		paths = append(paths, e.Path)
	}
	var payload SettingsPayload
	payload.Permissions.AdditionalDirectories = paths
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}
	body = append(body, '\n')
	return os.WriteFile(filepath.Join(settingsDir, "settings.json"), body, 0o644)
}

// Syncback reads <env>/description.md and updates the bead's description via
// `bd update <id> --body-file <path>`. One-way (file → bead).
func Syncback(db, beadsDir, ticketID string) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	if beadsDir == "" {
		return errors.New("BEADS_DIR not set: cannot derive env path")
	}
	envPath := filepath.Join(beadsDir, "envs", ticketID)
	descPath := filepath.Join(envPath, DescriptionFile)
	if _, err := os.Stat(descPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no %s in env (%s); launch `ticket agent` or `ticket spec` first", DescriptionFile, envPath)
		}
		return fmt.Errorf("stat %s: %w", descPath, err)
	}
	out, err := bdcli.Cmd(beadsDir, db, "update", ticketID, "--body-file", descPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("bd update %s --body-file: %v: %s", ticketID, err, out)
	}
	return nil
}
