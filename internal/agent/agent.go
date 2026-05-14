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

	"framework2/internal/dir"
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

// Run resolves the env folder for ticketID, regenerates the auto-managed
// context files (description.md, .claude/settings.json), and then execs
// AgentCommand from inside the env folder in a loop: after each exit, if a
// [SignalFile] is present, the loop regenerates context and re-execs
// AgentCommand with `--continue` plus the signal's args appended. The env
// folder must already exist — use `ticket env <id>` first.
//
// While AgentCommand is running, its pid is recorded at <env>/[PIDFile] so
// [Relaunch] can target it.
func Run(db, beadsDir, ticketID string, mode Mode) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	if beadsDir == "" {
		return errors.New("BEADS_DIR not set: cannot derive env root")
	}
	envPath := filepath.Join(beadsDir, "envs", ticketID)
	info, err := os.Stat(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("env folder does not exist for %s: %s (run `ticket env %s` first)", ticketID, envPath, ticketID)
		}
		return fmt.Errorf("stat env dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("env path is not a directory: %s", envPath)
	}

	sigPath := filepath.Join(envPath, SignalFile)
	pidPath := filepath.Join(envPath, PIDFile)
	// Drop any stale signal/pid file before starting; otherwise the very
	// first iteration would relaunch on a leftover from a prior session.
	_ = os.Remove(sigPath)
	_ = os.Remove(pidPath)

	var extraArgs []string
	for {
		prompt, err := Regenerate(db, envPath, ticketID, mode)
		if err != nil {
			return fmt.Errorf("regenerate env context: %w", err)
		}
		argv := append([]string{}, AgentDefaultArgs...)
		argv = append(argv, "--append-system-prompt", prompt)
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
func Regenerate(db, envPath, ticketID string, mode Mode) (string, error) {
	issue, err := loadIssue(db, ticketID)
	if err != nil {
		return "", err
	}
	dirs, err := dir.Load(db, ticketID)
	if err != nil {
		return "", err
	}
	if err := writeDescription(envPath, issue); err != nil {
		return "", err
	}
	if err := writeSettings(envPath, dirs); err != nil {
		return "", err
	}
	return BuildSystemPrompt(issue.ID, issue.Title, mode), nil
}

type bdIssue struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

func loadIssue(db, ticketID string) (*bdIssue, error) {
	args := []string{}
	if db != "" {
		args = append(args, "--db", db)
	}
	args = append(args, "--json", "show", ticketID)
	out, err := exec.Command("bd", args...).Output()
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

// BuildSystemPrompt returns the small mode-specific text appended to
// claude's system prompt for this launch. Kept intentionally short: the
// authoritative ticket content lives in description.md, not in the prompt.
func BuildSystemPrompt(ticketID, title string, mode Mode) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ticket %s — %s\n", ticketID, title)
	b.WriteString("IMPORTANT: Read description.md before everything else.\n")
	switch mode {
	case ModeSpec:
		b.WriteString("You are spec'ing, not coding. Do not edit source files. ")
		b.WriteString("Iterate on description.md (synced back to the bead description via `this env syncback`) ")
		b.WriteString("and use `ticket create child|followup|sibling <title>` to spawn related beads.")
	default:
		b.WriteString("To add a directory to this session: `this dir worktree <bare-repo>` (or `this dir link <path>`), then `this agent relaunch`.")
	}
	return b.String()
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
	args := []string{}
	if db != "" {
		args = append(args, "--db", db)
	}
	args = append(args, "update", ticketID, "--body-file", descPath)
	out, err := exec.Command("bd", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("bd update %s --body-file: %v: %s", ticketID, err, out)
	}
	return nil
}
