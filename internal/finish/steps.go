package finish

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"framework2/internal/bdcli"
	"framework2/internal/dir"
	"framework2/internal/project"

	"gopkg.in/yaml.v3"
)

// StepsFilename is the basename of the finish-step declaration. It is read
// from each linked bare repo's root (same convention as pipeline.md) and, as a
// project-wide fallback, from the BEADS_DIR root. See [findStepSet].
const StepsFilename = "finish-steps.yaml"

// StepLabel is the literal label stamped onto every step-bead created by the
// step-driven finish path, so the beads are queryable as a group.
const StepLabel = "finish-step"

// Metadata keys persisted on each step-bead at creation. They make a step-bead
// self-contained: every subsequent `this finish` re-reads the commands from
// the bead (not the YAML), so the config can change without stranding
// in-flight steps and resume needs no YAML at all.
const (
	metaCheck    = "finish_check"
	metaComplete = "finish_complete"
	metaWorkdir  = "finish_workdir"
	metaOrder    = "finish_order"
)

// StepSet is one selectable group of steps in finish-steps.yaml. The whole
// file is YAML: a list of step-sets, each gating a list of steps. For example:
//
//	# finish-steps.yaml
//	- condition: acli jira is-linked $BEAD_ID    # gate; first exit-0 wins
//	  steps:
//	    - title: Create PR                       # required; blocked_by key
//	      complete: gh pr create ...             # optional; run on launch
//	      check: gh pr view ... | grep -q OPEN   # required; exit 0 closes
//	    - title: Merge PR
//	      complete: gh pr merge ...
//	      check: gh pr view ... | grep -q MERGED
//	      blocked_by: [Create PR]                # optional; titles in this set
//	- condition: "true"                          # fallback set
//	  steps:
//	    - title: ...
//	      check: ...
//
// Conditions are evaluated top to bottom; the first whose `condition` command
// exits 0 selects that set. Every step in the selected set becomes a
// `finish-step` child bead of the ticket; `blocked_by` edges become bd
// `blocks` dependencies between those beads.
type StepSet struct {
	Condition string `yaml:"condition"`
	Steps     []Step `yaml:"steps"`
}

// Step is one entry in a [StepSet]. `title` and `check` are required;
// `complete` and `blocked_by` are optional. `title` doubles as the key other
// steps reference in their `blocked_by`.
type Step struct {
	Title     string   `yaml:"title"`
	Complete  string   `yaml:"complete"`
	Check     string   `yaml:"check"`
	BlockedBy []string `yaml:"blocked_by"`
}

// LoadStepSets reads <dir>/finish-steps.yaml and returns its step-sets. A
// missing file yields (nil, nil) — finish-steps.yaml is optional (its absence
// is what selects the Go fallback). Parse/validation errors propagate so a
// config typo aborts `this finish` loudly rather than silently dropping to the
// fallback.
func LoadStepSets(configDir string) ([]StepSet, error) {
	path := filepath.Join(configDir, StepsFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var sets []StepSet
	if err := yaml.Unmarshal(data, &sets); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i, set := range sets {
		if err := validateStepSet(set); err != nil {
			return nil, fmt.Errorf("%s: step-set %d: %w", path, i, err)
		}
	}
	return sets, nil
}

// validateStepSet enforces the per-set invariants the engine relies on: a
// non-empty condition, at least one step, required title/check on every step,
// unique titles, blocked_by edges that reference titles in the same set, and
// no cycle in the blocking graph (a cycle would deadlock the advance — no step
// would ever become unblocked).
func validateStepSet(set StepSet) error {
	if set.Condition == "" {
		return errors.New("missing `condition`")
	}
	if len(set.Steps) == 0 {
		return errors.New("has no `steps`")
	}
	titles := make(map[string]bool, len(set.Steps))
	for i, s := range set.Steps {
		if s.Title == "" {
			return fmt.Errorf("step %d is missing `title`", i)
		}
		if s.Check == "" {
			return fmt.Errorf("step %q is missing `check`", s.Title)
		}
		if titles[s.Title] {
			return fmt.Errorf("duplicate step title %q", s.Title)
		}
		titles[s.Title] = true
	}
	for _, s := range set.Steps {
		for _, b := range s.BlockedBy {
			if !titles[b] {
				return fmt.Errorf("step %q is blocked_by %q, which is not a step in this set", s.Title, b)
			}
		}
	}
	return detectCycle(set.Steps)
}

// detectCycle reports an error if the blocked_by graph contains a cycle.
// Edges point from a step to each of its blockers; a back-edge during DFS is a
// cycle.
func detectCycle(steps []Step) error {
	blockers := make(map[string][]string, len(steps))
	for _, s := range steps {
		blockers[s.Title] = s.BlockedBy
	}
	const (
		visiting = 1
		done     = 2
	)
	state := make(map[string]int, len(steps))
	var visit func(title string) error
	visit = func(title string) error {
		switch state[title] {
		case done:
			return nil
		case visiting:
			return fmt.Errorf("blocked_by cycle involving step %q", title)
		}
		state[title] = visiting
		for _, b := range blockers[title] {
			if err := visit(b); err != nil {
				return err
			}
		}
		state[title] = done
		return nil
	}
	for _, s := range steps {
		if err := visit(s.Title); err != nil {
			return err
		}
	}
	return nil
}

// runStepBead is the step-driven finish path. It is called on every `this
// finish`; behaviour follows the epic's state machine:
//
//  1. If the ticket already has `finish-step` children, resume advancing them
//     (the YAML is not re-read — the beads carry their own commands).
//  2. Otherwise evaluate finish-steps.yaml conditions; the first matching set
//     is materialised into one `finish-step` child bead per step, with
//     blocked_by edges recorded as bd `blocks` dependencies. No match anywhere
//     means handled=false and the caller runs the Go fallback.
//  3. Advance the graph: launch every not-yet-started step whose blockers are
//     all closed (set in-progress, run `complete`, then `check`); re-run
//     `check` for every in-progress step. A passing check closes the step and
//     cascades to any step it just unblocked.
//  4. When every step is closed, run the shared cleanup tail.
//
// While steps remain open, runStepBead returns handled=true with a non-nil
// error summarising what is still pending, so `this finish` exits non-zero —
// the signal for the operator (or the scheduled poller, ai-vrc.86.5) to call
// again later.
func runStepBead(db, beadsDir, ticketID string, worktrees []dir.Entry, stdout io.Writer) (handled bool, err error) {
	beads, err := loadStepBeads(db, beadsDir, ticketID)
	if err != nil {
		return false, err
	}

	if len(beads) == 0 {
		workdir, set, ok, err := findStepSet(db, beadsDir, ticketID, worktrees)
		if err != nil || !ok {
			return false, err
		}
		beads, err = createStepBeads(db, beadsDir, ticketID, workdir, set, stdout)
		if err != nil {
			return false, err
		}
	}

	if err := advanceSteps(db, beadsDir, ticketID, beads, stdout); err != nil {
		return true, err
	}

	if !allClosed(beads) {
		return true, pendingError(beads)
	}

	fmt.Fprintf(stdout, "all finish-steps for %s are closed\n", ticketID)
	if err := cleanup(db, beadsDir, ticketID, worktrees, stdout); err != nil {
		return true, err
	}
	return true, nil
}

// stepBead is the in-memory view of one finish-step child bead: its id/title,
// current status (open | in_progress | closed), the commands it was created
// with, the directory they run in, and its blockers (the bd `blocks`
// dependencies pointing at the steps that must close first). note records the
// most recent failure during this finish call, for the pending-summary error.
type stepBead struct {
	ID       string
	Title    string
	Status   string
	Check    string
	Complete string
	Workdir  string
	Order    int
	Blockers []string // ids of sibling step-beads that must close first
	note     string
}

// advanceSteps drives the blocking graph forward by one `this finish` call.
// Each non-closed step is processed at most once: an unblocked `open` step is
// launched (in-progress → complete → check), an `in_progress` step has its
// `check` re-run. Whenever a check passes the step closes and the scan repeats
// so a step it just unblocked gets its turn in the same call. Steps whose
// blockers are still open are skipped and left untouched. Only bd/infra
// failures abort (returned error); a failing `complete`/`check` just leaves
// the step in-progress with a recorded note.
func advanceSteps(db, beadsDir, ticketID string, beads []*stepBead, stdout io.Writer) error {
	byID := make(map[string]*stepBead, len(beads))
	for _, b := range beads {
		byID[b.ID] = b
	}

	processed := make(map[string]bool, len(beads))
	for progress := true; progress; {
		progress = false
		for _, b := range beads {
			if b.Status == "closed" || processed[b.ID] {
				continue
			}
			if b.Status == "open" && !blockersClosed(b, byID) {
				continue // still blocked — reconsider if a blocker closes below
			}
			processed[b.ID] = true

			if b.Status == "open" {
				if err := bdUpdateStatus(db, beadsDir, b.ID, "in_progress"); err != nil {
					return err
				}
				b.Status = "in_progress"
				fmt.Fprintf(stdout, "finish-step %s (%s): launched\n", b.ID, b.Title)
				if b.Complete != "" {
					if res := runShell(b.Complete, b.Workdir, stepEnv(ticketID, b.ID)...); res.exit != 0 {
						b.note = fmt.Sprintf("complete failed (exit=%d): %s", res.exit, oneLine(res.output))
						fmt.Fprintf(stdout, "finish-step %s (%s): complete failed (exit=%d); check not run\n", b.ID, b.Title, res.exit)
						continue // launched but not fulfilled — re-checked next call
					}
				}
			}

			if res := runShell(b.Check, b.Workdir, stepEnv(ticketID, b.ID)...); res.exit == 0 {
				if err := bdClose(db, beadsDir, b.ID); err != nil {
					return err
				}
				b.Status = "closed"
				b.note = ""
				fmt.Fprintf(stdout, "finish-step %s (%s): check passed — closed\n", b.ID, b.Title)
				progress = true // may have unblocked another step
			} else {
				b.note = fmt.Sprintf("check failed (exit=%d): %s", res.exit, oneLine(res.output))
				fmt.Fprintf(stdout, "finish-step %s (%s): check failed (exit=%d) — staying in-progress\n", b.ID, b.Title, res.exit)
			}
		}
	}
	return nil
}

// blockersClosed reports whether every blocker of b is closed, reading the
// blockers' CURRENT in-memory status (so a step closed earlier in this same
// advance pass counts as cleared). A blocker id missing from byID is treated
// as not-closed — conservative, but it never happens for graphs we build.
func blockersClosed(b *stepBead, byID map[string]*stepBead) bool {
	for _, id := range b.Blockers {
		blocker, ok := byID[id]
		if !ok || blocker.Status != "closed" {
			return false
		}
	}
	return true
}

func allClosed(beads []*stepBead) bool {
	for _, b := range beads {
		if b.Status != "closed" {
			return false
		}
	}
	return true
}

// pendingError summarises the steps that are not yet closed, so the non-zero
// `this finish` exit explains what is still outstanding and why.
func pendingError(beads []*stepBead) error {
	var b strings.Builder
	b.WriteString("finish-steps not all closed; re-run `this finish` to continue:")
	for _, s := range beads {
		if s.Status == "closed" {
			continue
		}
		state := s.Status
		if s.Status == "open" {
			state = "blocked"
		}
		fmt.Fprintf(&b, "\n  - %s (%s): %s", s.ID, s.Title, state)
		if s.note != "" {
			fmt.Fprintf(&b, " — %s", s.note)
		}
	}
	return errors.New(b.String())
}

// stepEnv is the environment exposed to a step's complete/check commands:
// BEAD_ID (the ticket being finished) and STEP_BEAD_ID (the step's own bead).
func stepEnv(ticketID, stepID string) []string {
	return []string{"BEAD_ID=" + ticketID, "STEP_BEAD_ID=" + stepID}
}

// oneLine collapses captured command output to a single trimmed line so it
// fits inside the pending-summary error without swallowing the whole log.
func oneLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	return s
}

// loadStepBeads returns the ticket's `finish-step` children (closed ones
// included) as [stepBead]s, sorted by their stored order. An empty result
// means no steps have been created yet — the signal to evaluate the YAML.
func loadStepBeads(db, beadsDir, ticketID string) ([]*stepBead, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "list", "--parent", ticketID, "--label", StepLabel, "--all").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("bd list steps of %s: %v: %s", ticketID, err, ee.Stderr)
		}
		return nil, fmt.Errorf("bd list steps of %s: %w", ticketID, err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return nil, fmt.Errorf("parse bd list steps of %s: %w", ticketID, err)
	}
	beads := make([]*stepBead, 0, len(rows))
	for _, r := range rows {
		b, err := showStepBead(db, beadsDir, r.ID)
		if err != nil {
			return nil, err
		}
		beads = append(beads, b)
	}
	sort.SliceStable(beads, func(i, j int) bool {
		if beads[i].Order != beads[j].Order {
			return beads[i].Order < beads[j].Order
		}
		return beads[i].ID < beads[j].ID
	})
	return beads, nil
}

// showStepBead reads one step-bead's status, persisted commands (metadata),
// and blocker ids (its `blocks` dependencies) via `bd --json show`.
func showStepBead(db, beadsDir, id string) (*stepBead, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "show", id).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("bd show step %s: %v: %s", id, err, ee.Stderr)
		}
		return nil, fmt.Errorf("bd show step %s: %w", id, err)
	}
	var rows []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		Status   string `json:"status"`
		Metadata struct {
			Check    string `json:"finish_check"`
			Complete string `json:"finish_complete"`
			Workdir  string `json:"finish_workdir"`
			Order    int    `json:"finish_order"`
		} `json:"metadata"`
		Dependencies []struct {
			ID             string `json:"id"`
			DependencyType string `json:"dependency_type"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parse bd show step %s: %w", id, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("step bead not found: %s", id)
	}
	r := rows[0]
	b := &stepBead{
		ID:       r.ID,
		Title:    r.Title,
		Status:   r.Status,
		Check:    r.Metadata.Check,
		Complete: r.Metadata.Complete,
		Workdir:  r.Metadata.Workdir,
		Order:    r.Metadata.Order,
	}
	for _, d := range r.Dependencies {
		if d.DependencyType == "blocks" {
			b.Blockers = append(b.Blockers, d.ID)
		}
	}
	return b, nil
}

// findStepSet locates the step-set to materialise and the working directory
// its commands run in. It checks, in order:
//
//  1. each linked worktree's bare repo root (<bareRepo>/finish-steps.yaml),
//     evaluating conditions in that worktree — the spec'd per-repo location;
//  2. a global <beadsDir>/finish-steps.yaml fallback, evaluating conditions in
//     [globalWorkdir] (the first worktree, else the env folder). One config
//     can then drive finish across every repo in a project.
//
// Conditions run with BEAD_ID set to ticketID. ok=false means nothing matched
// anywhere — the Go fallback applies. A parse error in any file propagates.
func findStepSet(db, beadsDir, ticketID string, worktrees []dir.Entry) (workdir string, set StepSet, ok bool, err error) {
	for _, wt := range worktrees {
		sets, err := LoadStepSets(wt.BareRepo)
		if err != nil {
			return "", StepSet{}, false, err
		}
		if sets == nil {
			continue
		}
		if s, ok := selectStepSet(sets, wt.Path, ticketID); ok {
			return wt.Path, s, true, nil
		}
	}

	globalSets, err := LoadStepSets(beadsDir)
	if err != nil {
		return "", StepSet{}, false, err
	}
	if globalSets != nil {
		wd := globalWorkdir(beadsDir, ticketID, worktrees)
		if s, ok := selectStepSet(globalSets, wd, ticketID); ok {
			return wd, s, true, nil
		}
	}
	return "", StepSet{}, false, nil
}

// selectStepSet returns the first step-set whose condition command exits 0
// when run in workdir (with BEAD_ID set), or (StepSet{}, false) when none
// match.
func selectStepSet(sets []StepSet, workdir, ticketID string) (StepSet, bool) {
	for _, s := range sets {
		if runShell(s.Condition, workdir, "BEAD_ID="+ticketID).exit == 0 {
			return s, true
		}
	}
	return StepSet{}, false
}

// globalWorkdir picks the cwd for commands driven by the global
// <beadsDir>/finish-steps.yaml. With at least one worktree it uses the first
// (repo context); otherwise it uses the ticket's env folder when that exists,
// falling back to beadsDir — both always present while the step runs.
func globalWorkdir(beadsDir, ticketID string, worktrees []dir.Entry) string {
	if len(worktrees) > 0 {
		return worktrees[0].Path
	}
	envPath := project.Path(beadsDir, filepath.Join("envs", ticketID))
	if st, err := os.Stat(envPath); err == nil && st.IsDir() {
		return envPath
	}
	return beadsDir
}

// createStepBeads materialises every step in set as a `finish-step` child bead
// of ticketID, persisting each step's commands/workdir/order in metadata, then
// wires blocked_by edges as bd `blocks` dependencies between the new beads.
// Returns the beads in YAML order, all `open`, ready for [advanceSteps].
func createStepBeads(db, beadsDir, ticketID, workdir string, set StepSet, stdout io.Writer) ([]*stepBead, error) {
	titleToID := make(map[string]string, len(set.Steps))
	beads := make([]*stepBead, 0, len(set.Steps))
	for i, s := range set.Steps {
		id, err := createOneStepBead(db, beadsDir, ticketID, s, workdir, i)
		if err != nil {
			return nil, err
		}
		titleToID[s.Title] = id
		beads = append(beads, &stepBead{
			ID:       id,
			Title:    s.Title,
			Status:   "open",
			Check:    s.Check,
			Complete: s.Complete,
			Workdir:  workdir,
			Order:    i,
		})
	}
	for i, s := range set.Steps {
		for _, blockerTitle := range s.BlockedBy {
			blockerID := titleToID[blockerTitle] // validated to exist by LoadStepSets
			if err := bdDepAdd(db, beadsDir, beads[i].ID, blockerID); err != nil {
				return nil, err
			}
			beads[i].Blockers = append(beads[i].Blockers, blockerID)
		}
	}
	fmt.Fprintf(stdout, "created %d finish-step(s) for %s (condition matched in %s)\n", len(beads), ticketID, workdir)
	return beads, nil
}

// createOneStepBead creates a single child bead, stamps the finish-step label,
// and persists the step's commands, workdir, and order as bead metadata.
func createOneStepBead(db, beadsDir, ticketID string, s Step, workdir string, order int) (string, error) {
	out, err := bdcli.Cmd(beadsDir, db, "create", s.Title, "--silent", "--parent", ticketID).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("bd create step bead: %v: %s", err, ee.Stderr)
		}
		return "", fmt.Errorf("bd create step bead: %w", err)
	}
	stepID := strings.TrimSpace(string(out))
	if stepID == "" {
		return "", errors.New("bd create returned empty step bead id")
	}
	// Set metadata as an explicit JSON object (not `--set-metadata key=value`):
	// bd type-coerces string values, so a `check: "true"` or a numeric command
	// would be stored as a bool/int and fail to read back as a string. A JSON
	// object pins every command to a string.
	meta := map[string]any{
		metaCheck:   s.Check,
		metaWorkdir: workdir,
		metaOrder:   order,
	}
	if s.Complete != "" {
		meta[metaComplete] = s.Complete
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return stepID, fmt.Errorf("marshal step %s metadata: %w", stepID, err)
	}
	if uout, err := bdcli.Cmd(beadsDir, db, "update", stepID, "--add-label", StepLabel, "--metadata", string(metaJSON)).CombinedOutput(); err != nil {
		return stepID, fmt.Errorf("bd update %s (label+metadata): %w: %s", stepID, err, strings.TrimSpace(string(uout)))
	}
	return stepID, nil
}

// bdDepAdd records that stepID is blocked-by blockerID (a `blocks` dependency,
// the same edge type the ready-label flow reads).
func bdDepAdd(db, beadsDir, stepID, blockerID string) error {
	if out, err := bdcli.Cmd(beadsDir, db, "dep", "add", stepID, blockerID).CombinedOutput(); err != nil {
		return fmt.Errorf("bd dep add %s %s: %w: %s", stepID, blockerID, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// bdUpdateStatus sets a bead's status (used to move a launched step to
// in_progress).
func bdUpdateStatus(db, beadsDir, id, status string) error {
	if out, err := bdcli.Cmd(beadsDir, db, "update", id, "--status", status).CombinedOutput(); err != nil {
		return fmt.Errorf("bd update %s --status %s: %w: %s", id, status, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// shellResult is the captured outcome of one finish-step shell command.
type shellResult struct {
	exit   int
	output string
}

// runShell runs cmd via `bash -c` with cwd set to workdir, capturing stdout
// and stderr together. Each entry in env ("KEY=VALUE") is added on top of the
// inherited environment. A command that cannot be started reports exit=-1.
func runShell(cmd, workdir string, env ...string) shellResult {
	c := exec.Command("bash", "-c", cmd)
	c.Dir = workdir
	if len(env) > 0 {
		c.Env = append(os.Environ(), env...)
	}
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	err := c.Run()
	res := shellResult{output: buf.String()}
	if err == nil {
		return res
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.exit = ee.ExitCode()
		return res
	}
	res.exit = -1
	return res
}
