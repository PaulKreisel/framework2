// Package ready implements the `ready` label flow described in ai-vrc.56:
//
//   - `Set` / `Remove` add or remove the literal label `ready` on a bead.
//   - [HandleBlocked] is called from `ticket agent` when the target ticket
//     is currently blocked: it stamps `ready` onto the target and every
//     transitive open blocker, then launches `ticket agent <id>` in a new
//     Terminal.app window for every blocker that itself has no remaining
//     open blockers (the roots of the blocker graph).
//   - [LaunchReadyFollowups] is called from `ticket finish` after the bead
//     is closed: it inspects tickets that were blocked by the just-finished
//     one and, for those that now have no remaining blockers AND still
//     carry `ready`, launches `ticket agent` in a new window.
//
// All terminal spawning is macOS-only (osascript + Terminal.app). On other
// platforms the spawn step no-ops with a printed warning so the rest of
// the flow (label stamps, bd state) still completes deterministically.
package ready

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"framework2/internal/bdcli"
)

// Label is the literal label this package manages. Exported so tests and
// other packages can reference it without re-stringifying.
const Label = "ready"

// ExecOsascript runs `osascript -e <script>` and returns its stdout. Mirrors
// the pattern in internal/agent/attach.go: exposed as a package-level var so
// tests can substitute a stub (Terminal.app is unavailable on CI).
var ExecOsascript = func(script string) (string, error) {
	out, err := exec.Command("osascript", "-e", script).Output()
	return string(out), err
}

// SpawnAgentWindow is the package-level hook used to open a new agent
// terminal. Tests override it to capture launches without driving
// Terminal.app. Production callers go through [DefaultSpawnAgentWindow].
// beadsDir is the BEADS_DIR the spawned `ticket agent` must resolve
// against — without it, the new shell would fall back to whatever the
// user's profile sets (typically ~/ai), which breaks follow-ups owned
// by any other bead directory.
var SpawnAgentWindow = DefaultSpawnAgentWindow

// Set adds the `ready` label to ticketID. Idempotent — `bd label add` on a
// label that is already present is a no-op there.
func Set(db, beadsDir, ticketID string) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	out, err := bdcli.Cmd(beadsDir, db, "label", "add", ticketID, Label).CombinedOutput()
	if err != nil {
		return fmt.Errorf("bd label add %s %s: %w: %s", ticketID, Label, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Remove removes the `ready` label from ticketID. Idempotent in the same
// sense as [Set].
func Remove(db, beadsDir, ticketID string) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	out, err := bdcli.Cmd(beadsDir, db, "label", "remove", ticketID, Label).CombinedOutput()
	if err != nil {
		return fmt.Errorf("bd label remove %s %s: %w: %s", ticketID, Label, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// issueRow mirrors the subset of `bd --json show` we care about.
type issueRow struct {
	ID           string       `json:"id"`
	Status       string       `json:"status"`
	Labels       []string     `json:"labels"`
	Dependencies []dependency `json:"dependencies"`
}

type dependency struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	DependencyType string `json:"dependency_type"`
}

func showIssue(db, beadsDir, ticketID string) (*issueRow, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "show", ticketID).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("bd show %s: %v: %s", ticketID, err, ee.Stderr)
		}
		return nil, fmt.Errorf("bd show %s: %w", ticketID, err)
	}
	var rows []issueRow
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse bd show %s: %w", ticketID, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("ticket not found: %s", ticketID)
	}
	return &rows[0], nil
}

// openBlockers returns the IDs of issue's `blocks`-type dependencies whose
// status is not `closed`. Mirrors the rule [agent.Claim] uses to refuse a
// blocked launch.
func openBlockers(issue *issueRow) []string {
	var ids []string
	for _, d := range issue.Dependencies {
		if d.DependencyType == "blocks" && d.Status != "closed" {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

func hasLabel(issue *issueRow, label string) bool {
	return slices.Contains(issue.Labels, label)
}

// HandleBlocked is invoked by `ticket agent <id>` after positional resolution
// but before [agent.Run] is called. When ticketID has at least one open
// blocker, it stamps `ready` on the target and every transitive open
// blocker, spawns `ticket agent <id>` in a new Terminal.app window for each
// open blocker that itself has no remaining open blockers, and returns
// (true, nil) so the caller exits without trying to claim/launch the
// blocked target. When ticketID is not blocked, it returns (false, nil)
// and the caller proceeds with the regular launch.
func HandleBlocked(db, beadsDir, ticketID string, stdout io.Writer) (handled bool, err error) {
	root, err := showIssue(db, beadsDir, ticketID)
	if err != nil {
		return false, err
	}
	rootBlockers := openBlockers(root)
	if len(rootBlockers) == 0 {
		return false, nil
	}

	// Walk the blocker graph: BFS over open `blocks` deps, collecting the
	// reachable set and recording each visited bead's open-blockers count
	// so we can identify roots (count == 0) afterwards.
	visited := map[string]*issueRow{ticketID: root}
	frontier := append([]string(nil), rootBlockers...)
	for len(frontier) > 0 {
		next := frontier[0]
		frontier = frontier[1:]
		if _, seen := visited[next]; seen {
			continue
		}
		info, err := showIssue(db, beadsDir, next)
		if err != nil {
			return false, err
		}
		visited[next] = info
		frontier = append(frontier, openBlockers(info)...)
	}

	// Stamp `ready` on the target and every transitive open blocker. We
	// label closed beads too if they sneak in — but openBlockers already
	// filters those out, so in practice every visited node is non-closed.
	// Order is stable (sort by id) so logs and tests don't flake.
	ids := make([]string, 0, len(visited))
	for id := range visited {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := Set(db, beadsDir, id); err != nil {
			return false, err
		}
	}

	// Roots = visited blockers whose own open-blockers list is empty.
	// The original target is excluded — by construction it has at least one
	// blocker (rootBlockers is non-empty) so it can never be a root.
	var roots []string
	for _, id := range ids {
		if id == ticketID {
			continue
		}
		if len(openBlockers(visited[id])) == 0 {
			roots = append(roots, id)
		}
	}

	if len(roots) == 0 {
		// Edge case: every blocker is itself blocked by something we
		// could not load (e.g. closed-but-pending status). Nothing to
		// launch — labels stay so the next finish can pick it up.
		fmt.Fprintf(stdout, "ticket %s is blocked; labeled %d ticket(s) `ready` but no unblocked root found\n",
			ticketID, len(ids))
		return true, nil
	}

	fmt.Fprintf(stdout, "ticket %s is blocked; labeled %d ticket(s) `ready` and launching %d root(s): %s\n",
		ticketID, len(ids), len(roots), strings.Join(roots, ", "))
	for _, id := range roots {
		if err := SpawnAgentWindow(id, beadsDir); err != nil {
			fmt.Fprintf(stdout, "warn: could not launch %s in new terminal: %v\n", id, err)
		}
	}
	return true, nil
}

// LaunchReadyFollowups is called by `ticket finish` after ticketID has been
// closed. It walks the dependents of ticketID (issues that listed ticketID
// as a `blocks`-type dependency) and, for each that (a) now has no
// remaining open blockers and (b) still carries the `ready` label, spawns
// `ticket agent <id>` in a new Terminal.app window.
//
// Returns nil even when individual spawns fail — finish should not roll back
// just because one terminal could not be opened. Spawn errors are logged
// to stdout.
func LaunchReadyFollowups(db, beadsDir, ticketID string, stdout io.Writer) error {
	dependents, err := listDependents(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	var launched []string
	for _, dep := range dependents {
		info, err := showIssue(db, beadsDir, dep)
		if err != nil {
			fmt.Fprintf(stdout, "warn: could not inspect dependent %s: %v\n", dep, err)
			continue
		}
		if info.Status == "closed" {
			continue
		}
		if len(openBlockers(info)) > 0 {
			continue
		}
		if !hasLabel(info, Label) {
			continue
		}
		launched = append(launched, dep)
	}
	if len(launched) == 0 {
		return nil
	}
	fmt.Fprintf(stdout, "launching %d ready follow-up(s): %s\n", len(launched), strings.Join(launched, ", "))
	for _, id := range launched {
		if err := SpawnAgentWindow(id, beadsDir); err != nil {
			fmt.Fprintf(stdout, "warn: could not launch %s in new terminal: %v\n", id, err)
		}
	}
	return nil
}

// listDependents returns the IDs of issues that depend on ticketID via a
// `blocks`-type edge.
func listDependents(db, beadsDir, ticketID string) ([]string, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "dep", "list", ticketID, "--direction", "up").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("bd dep list %s --direction up: %v: %s", ticketID, err, ee.Stderr)
		}
		return nil, fmt.Errorf("bd dep list %s --direction up: %w", ticketID, err)
	}
	out = []byte(strings.TrimSpace(string(out)))
	if len(out) == 0 {
		return nil, nil
	}
	var rows []dependency
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse bd dep list output for %s: %w", ticketID, err)
	}
	var ids []string
	for _, r := range rows {
		if r.DependencyType != "blocks" {
			continue
		}
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// DefaultSpawnAgentWindow opens a new macOS Terminal.app window and runs
// `ticket agent <ticketID>` inside it. beadsDir is exported as BEADS_DIR
// for the spawned process so the follow-up resolves against the same
// bead directory the parent finish ran in — otherwise the new shell
// would inherit whatever the user's profile sets (typically ~/ai), and
// `ticket agent` would fail to find ticketID in a different BEADS_DIR.
// Errors on non-darwin platforms.
func DefaultSpawnAgentWindow(ticketID, beadsDir string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("launching a new agent window requires macOS (GOOS=%s)", runtime.GOOS)
	}
	bin, err := findTicketBin()
	if err != nil {
		return fmt.Errorf("locate ticket binary: %w", err)
	}
	cmd := fmt.Sprintf("exec env BEADS_DIR=%s %s agent %s",
		shellQuote(beadsDir), shellQuote(bin), shellQuote(ticketID))
	script := fmt.Sprintf(`tell application "Terminal"
    do script %s
    activate
end tell`, applescriptString(cmd))
	if _, err := ExecOsascript(script); err != nil {
		return fmt.Errorf("osascript spawn: %w", err)
	}
	return nil
}

// findTicketBin mirrors internal/finish/async.go: prefer a sibling `ticket`
// next to the current executable (covers the `this`+`ticket` pair installed
// by `make install`), then fall back to a PATH lookup.
func findTicketBin() (string, error) {
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "ticket")
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	return exec.LookPath("ticket")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func applescriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

