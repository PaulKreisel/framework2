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
	"sync"

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
// file is YAML: a list of step-sets, each gating a list of steps. Steps may
// nest: a step that carries a `steps:` list is a parent (a group) that closes
// once all its children close, rather than running commands of its own. For
// example:
//
//	# finish-steps.yaml
//	- condition: acli jira is-linked $BEAD_ID    # gate; first exit-0 wins
//	  steps:
//	    - title: PR 1                            # parent step (a group)
//	      steps:
//	        - title: lint                        # leaf; runs check
//	          check: gh pr checks ... | grep lint
//	        - title: tests
//	          check: gh pr checks ... | grep tests
//	    - title: PR 2                            # leaf step
//	      complete: gh pr create ...             # optional; run on launch
//	      check: gh pr view ... | grep -q MERGED # required on a leaf
//	      blocked_by: [PR 1]                     # optional; sibling titles
//	- condition: "true"                          # fallback set
//	  steps:
//	    - title: ...
//	      check: ...
//
// Conditions are evaluated top to bottom; the first whose `condition` command
// exits 0 selects that set. Every step (parent or leaf) in the selected set
// becomes a `finish-step` bead: top-level steps are children of the ticket,
// nested steps are children of their parent step-bead, so the bead tree mirrors
// the YAML. `blocked_by` edges become bd `blocks` dependencies between sibling
// beads. Nesting is unbounded.
type StepSet struct {
	Condition string `yaml:"condition"`
	Steps     []Step `yaml:"steps"`
}

// Step is one entry in a [StepSet]. Every step needs a `title`, which doubles
// as the key its siblings reference in their `blocked_by`. A step is one of two
// kinds:
//
//   - leaf: carries `check` (required) and optional `complete`. These commands
//     run during the finish; a passing `check` closes the step.
//   - parent: carries a nested `steps` list and no `check`/`complete` of its
//     own. It closes automatically once all of its children close.
//
// `blocked_by` (optional, both kinds) lists sibling titles — steps in the same
// group that must close before this one starts.
type Step struct {
	Title     string   `yaml:"title"`
	Complete  string   `yaml:"complete"`
	Check     string   `yaml:"check"`
	BlockedBy []string `yaml:"blocked_by"`
	Steps     []Step   `yaml:"steps"`
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
// non-empty condition, at least one step, and a valid step tree (see
// [validateSteps]).
func validateStepSet(set StepSet) error {
	if set.Condition == "" {
		return errors.New("missing `condition`")
	}
	if len(set.Steps) == 0 {
		return errors.New("has no `steps`")
	}
	return validateSteps(set.Steps)
}

// validateSteps validates one sibling group and recurses into every nested
// group, enforcing: a `title` on each step; the leaf-vs-parent rule (a step
// with nested `steps` is a parent and must carry no `check`/`complete` of its
// own, while a leaf must carry a `check`); titles unique within the group;
// blocked_by edges that reference titles in the SAME group (sibling-scoped); and
// no cycle in that group's blocking graph (a cycle would deadlock the advance —
// no step would ever become unblocked). Nesting is unbounded.
func validateSteps(steps []Step) error {
	titles := make(map[string]bool, len(steps))
	for i, s := range steps {
		if s.Title == "" {
			return fmt.Errorf("step %d is missing `title`", i)
		}
		if titles[s.Title] {
			return fmt.Errorf("duplicate step title %q", s.Title)
		}
		titles[s.Title] = true
		if len(s.Steps) > 0 {
			// Parent step: it closes when its children close, so it must not
			// carry commands of its own.
			if s.Check != "" || s.Complete != "" {
				return fmt.Errorf("step %q has nested `steps` and so must not have its own `check`/`complete`", s.Title)
			}
			if err := validateSteps(s.Steps); err != nil {
				return err
			}
		} else if s.Check == "" {
			return fmt.Errorf("step %q is missing `check`", s.Title)
		}
	}
	for _, s := range steps {
		for _, b := range s.BlockedBy {
			if !titles[b] {
				return fmt.Errorf("step %q is blocked_by %q, which is not a step in this group", s.Title, b)
			}
		}
	}
	return detectCycle(steps)
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
//  3. Advance the graph: launch every not-yet-started leaf step whose blockers
//     are all closed and whose parent group has started (set in-progress, run
//     `complete`, then `check`); re-run `check` for every in-progress leaf. A
//     passing check closes the step and cascades to any step it just unblocked.
//     A parent (group) step launches like a leaf but runs no commands; it
//     closes automatically once all of its children close, which can cascade
//     further up the tree within the same call.
//  4. When every top-level step is closed, run the shared cleanup tail.
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

// stepBead is the in-memory view of one finish-step bead: its id/title,
// current status (open | in_progress | closed), the commands it was created
// with, the directory they run in, its blockers (the bd `blocks` dependencies
// pointing at the sibling steps that must close first), its nested child steps,
// and a back-pointer to its parent step (nil at the top level). A bead with
// children is a parent (group) and carries no commands of its own. note records
// the most recent failure during this finish call, for the pending summary.
type stepBead struct {
	ID       string
	Title    string
	Status   string
	Check    string
	Complete string
	Workdir  string
	Order    int
	Blockers []string    // ids of sibling step-beads that must close first
	Children []*stepBead // nested step-beads (non-empty ⇒ this is a parent)
	parent   *stepBead   // enclosing group, nil at the top level
	note     string
}

// maxStepConcurrency bounds how many step shell commands (complete/check) run
// at once inside a single advanceSteps wave. Checks are meant to be light
// network polls (gh pr view, jenkins status); the cap keeps a wide graph from
// spawning an unbounded number of subprocesses simultaneously.
const maxStepConcurrency = 8

// isParent reports whether b is a group step (has nested children) rather than
// a leaf that runs commands.
func (b *stepBead) isParent() bool { return len(b.Children) > 0 }

// advanceSteps drives the blocking graph forward by one `this finish` call,
// walking the whole step tree. Leaf steps run shell commands; parent (group)
// steps run none and merely close once all their children close.
//
// Each iteration first settles parent-group state (see [advanceGroups]):
// unblocked open groups flip to in_progress so their children may start, and
// groups whose children have all closed are closed — cascading up the tree. It
// then gathers every LEAF runnable right now — every in_progress leaf plus
// every open leaf whose blockers have all closed and whose parent group has
// started — flips the open ones to in_progress up front, then runs all of their
// `complete`/`check` shell commands concurrently (bounded by
// [maxStepConcurrency]) before awaiting any of them. That way, when one step
// closes and unblocks several siblings, the siblings all start together rather
// than one-at-a-time in list order, so a slow check no longer delays an
// independent branch.
//
// bd writes stay serialised: group transitions, leaf status flips (before the
// wave's commands run) and leaf closes (after they all return) happen in stable
// bead order — only the shell commands run in parallel, so there are no races
// on the database. A close may unblock further steps or complete a parent,
// which the next iteration picks up. Each leaf is processed at most once, so the
// loop terminates. Only bd/infra failures abort (returned error); a failing
// `complete`/`check` just leaves the leaf in-progress with a recorded note.
func advanceSteps(db, beadsDir, ticketID string, beads []*stepBead, stdout io.Writer) error {
	all := flattenSteps(beads)
	byID := make(map[string]*stepBead, len(all))
	for _, b := range all {
		byID[b.ID] = b
	}

	processed := make(map[string]bool, len(all))
	for {
		// Settle parent-group launches/closes before gathering leaves, so a
		// group that just opened lets its children run and a group whose
		// children all closed is itself closed this iteration.
		if err := advanceGroups(db, beadsDir, all, byID, stdout); err != nil {
			return err
		}

		// Gather every leaf that can run right now, in stable bead order so all
		// logging and bd writes below stay deterministic.
		var wave []*stepBead
		for _, b := range all {
			if b.isParent() || b.Status == "closed" || processed[b.ID] {
				continue
			}
			if b.Status == "open" && !canStart(b, byID) {
				continue // still blocked — reconsider once a blocker closes
			}
			wave = append(wave, b)
		}
		if len(wave) == 0 {
			break
		}

		// Launch phase (serialised bd writes): flip every open step in the wave to
		// in_progress before any check is awaited, so siblings unblocked at the
		// same time all start together. Only steps launched this wave run
		// `complete`; previously in_progress steps just re-check.
		justLaunched := make([]bool, len(wave))
		for i, b := range wave {
			if b.Status == "open" {
				if err := bdUpdateStatus(db, beadsDir, b.ID, "in_progress"); err != nil {
					return err
				}
				b.Status = "in_progress"
				justLaunched[i] = true
				fmt.Fprintf(stdout, "finish-step %s (%s): launched\n", b.ID, b.Title)
			}
		}

		// Run phase (concurrent, bounded): each step's shell commands run in their
		// own goroutine writing only its own slot — no bd writes, no shared state.
		outcomes := make([]stepOutcome, len(wave))
		var wg sync.WaitGroup
		sem := make(chan struct{}, maxStepConcurrency)
		for i, b := range wave {
			wg.Add(1)
			go func(i int, b *stepBead) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				outcomes[i] = runStepShell(b, justLaunched[i], ticketID)
			}(i, b)
		}
		wg.Wait()

		// Apply phase (serialised bd writes): close steps whose check passed and
		// record notes for the rest, in stable bead order.
		for i, b := range wave {
			processed[b.ID] = true
			o := outcomes[i]
			if o.completeFailed {
				b.note = fmt.Sprintf("complete failed (exit=%d): %s", o.completeRes.exit, oneLine(o.completeRes.output))
				fmt.Fprintf(stdout, "finish-step %s (%s): complete failed (exit=%d); check not run\n", b.ID, b.Title, o.completeRes.exit)
				continue // launched but not fulfilled — re-checked next call
			}
			if o.checkRes.exit == 0 {
				if err := bdClose(db, beadsDir, b.ID); err != nil {
					return err
				}
				b.Status = "closed"
				b.note = ""
				fmt.Fprintf(stdout, "finish-step %s (%s): check passed — closed\n", b.ID, b.Title)
			} else {
				b.note = fmt.Sprintf("check failed (exit=%d): %s", o.checkRes.exit, oneLine(o.checkRes.output))
				fmt.Fprintf(stdout, "finish-step %s (%s): check failed (exit=%d) — staying in-progress\n", b.ID, b.Title, o.checkRes.exit)
			}
		}
	}
	return nil
}

// advanceGroups settles parent (group) step state to a fixpoint: an unblocked
// open group is flipped to in_progress (so [canStart] lets its children run),
// and an in_progress group whose children have all closed is itself closed.
// Both are bd-only writes (groups run no shell commands) and run in stable bead
// order; the loop repeats because launching or closing one group can cascade to
// another — a nested group opening, or a parent completing once its last child
// group closes. Leaf steps are left to advanceSteps' concurrent wave.
func advanceGroups(db, beadsDir string, all []*stepBead, byID map[string]*stepBead, stdout io.Writer) error {
	for changed := true; changed; {
		changed = false
		for _, b := range all {
			if !b.isParent() || b.Status == "closed" {
				continue
			}
			if b.Status == "open" {
				if !canStart(b, byID) {
					continue // blocked or enclosing group not yet started
				}
				if err := bdUpdateStatus(db, beadsDir, b.ID, "in_progress"); err != nil {
					return err
				}
				b.Status = "in_progress"
				fmt.Fprintf(stdout, "finish-step %s (%s): group launched\n", b.ID, b.Title)
				changed = true
			}
			if b.Status == "in_progress" && allChildrenClosed(b) {
				if err := bdClose(db, beadsDir, b.ID); err != nil {
					return err
				}
				b.Status = "closed"
				b.note = ""
				fmt.Fprintf(stdout, "finish-step %s (%s): all child steps closed — closed\n", b.ID, b.Title)
				changed = true
			}
		}
	}
	return nil
}

// stepOutcome captures the result of one step's shell commands for a single
// wave, gathered off the bd-write path so advanceSteps' serialised apply phase
// can act on it.
type stepOutcome struct {
	completeFailed bool        // `complete` ran and exited non-zero (check skipped)
	completeRes    shellResult // valid when completeFailed
	checkRes       shellResult // valid unless completeFailed
}

// runStepShell runs one step's shell work for a wave: a just-launched step with
// a `complete` command runs it first and, on failure, skips the check (so a
// launched-but-unfulfilled step is re-checked next call); otherwise it runs the
// check. It performs no bd writes, so it is safe to call concurrently.
func runStepShell(b *stepBead, justLaunched bool, ticketID string) stepOutcome {
	var o stepOutcome
	if justLaunched && b.Complete != "" {
		o.completeRes = runShell(b.Complete, b.Workdir, stepEnv(ticketID, b.ID)...)
		if o.completeRes.exit != 0 {
			o.completeFailed = true
			return o
		}
	}
	o.checkRes = runShell(b.Check, b.Workdir, stepEnv(ticketID, b.ID)...)
	return o
}

// flattenSteps returns every bead in the tree (parents before their children),
// so advanceSteps can scan the whole graph in one loop while the fixpoint
// re-runs until no step changes.
func flattenSteps(beads []*stepBead) []*stepBead {
	var out []*stepBead
	var walk func([]*stepBead)
	walk = func(bs []*stepBead) {
		for _, b := range bs {
			out = append(out, b)
			walk(b.Children)
		}
	}
	walk(beads)
	return out
}

// canStart reports whether b may begin: all its (sibling) blockers are closed
// AND its enclosing group has started (a child never runs before its parent
// group does, so a group blocked by a sibling holds its children back too).
func canStart(b *stepBead, byID map[string]*stepBead) bool {
	if b.parent != nil && b.parent.Status == "open" {
		return false
	}
	return blockersClosed(b, byID)
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

// allChildrenClosed reports whether every child of a parent step is closed —
// the trigger to close the parent. Only called for parents, which always have
// at least one child.
func allChildrenClosed(b *stepBead) bool {
	for _, c := range b.Children {
		if c.Status != "closed" {
			return false
		}
	}
	return true
}

// allClosed reports whether every top-level step is closed. A closed parent
// implies its whole subtree is closed (a parent only closes once its children
// do), so the top level is sufficient to decide the ticket is done.
func allClosed(beads []*stepBead) bool {
	for _, b := range beads {
		if b.Status != "closed" {
			return false
		}
	}
	return true
}

// pendingError summarises the steps that are not yet closed, so the non-zero
// `this finish` exit explains what is still outstanding and why. The tree is
// walked depth-first and indented by nesting level.
func pendingError(beads []*stepBead) error {
	var b strings.Builder
	b.WriteString("finish-steps not all closed; re-run `this finish` to continue:")
	writePending(&b, beads, 1)
	return errors.New(b.String())
}

// writePending appends the not-yet-closed steps in beads (and their pending
// descendants) to b, indented by depth. An `open` step is reported as "blocked"
// (after an advance pass, an open step is one whose blockers — or parent group —
// have not cleared); a parent group is prefixed with "group ".
func writePending(b *strings.Builder, beads []*stepBead, depth int) {
	indent := strings.Repeat("  ", depth)
	for _, s := range beads {
		if s.Status == "closed" {
			continue
		}
		state := s.Status
		if s.Status == "open" {
			state = "blocked"
		}
		if s.isParent() {
			state = "group " + state
		}
		fmt.Fprintf(b, "\n%s- %s (%s): %s", indent, s.ID, s.Title, state)
		if s.note != "" {
			fmt.Fprintf(b, " — %s", s.note)
		}
		writePending(b, s.Children, depth+1)
	}
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

// loadStepBeads returns the ticket's top-level `finish-step` children (closed
// ones included) as a sorted [stepBead] tree, recursively loading each step's
// own children. An empty result means no steps have been created yet — the
// signal to evaluate the YAML.
func loadStepBeads(db, beadsDir, ticketID string) ([]*stepBead, error) {
	return loadStepChildren(db, beadsDir, ticketID, nil)
}

// loadStepChildren returns the `finish-step` children of parentID (closed ones
// included), each with its own subtree loaded recursively, sorted by stored
// order. parent is the in-memory bead these children hang off (nil at the top
// level), wired so [canStart] can consult the enclosing group's status.
func loadStepChildren(db, beadsDir, parentID string, parent *stepBead) ([]*stepBead, error) {
	out, err := bdcli.Cmd(beadsDir, db, "--json", "list", "--parent", parentID, "--label", StepLabel, "--all").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("bd list steps of %s: %v: %s", parentID, err, ee.Stderr)
		}
		return nil, fmt.Errorf("bd list steps of %s: %w", parentID, err)
	}
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return nil, fmt.Errorf("parse bd list steps of %s: %w", parentID, err)
	}
	beads := make([]*stepBead, 0, len(rows))
	for _, r := range rows {
		b, err := showStepBead(db, beadsDir, r.ID)
		if err != nil {
			return nil, err
		}
		b.parent = parent
		children, err := loadStepChildren(db, beadsDir, b.ID, b)
		if err != nil {
			return nil, err
		}
		b.Children = children
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

// createStepBeads materialises set's step tree as `finish-step` beads beneath
// ticketID (top-level steps are children of the ticket, nested steps children
// of their parent step-bead), persisting each step's commands/workdir/order in
// metadata and wiring blocked_by edges as bd `blocks` dependencies between
// siblings. Returns the top-level beads in YAML order, all `open`, ready for
// [advanceSteps].
func createStepBeads(db, beadsDir, ticketID, workdir string, set StepSet, stdout io.Writer) ([]*stepBead, error) {
	beads, total, err := createStepLevel(db, beadsDir, ticketID, set.Steps, workdir, nil)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(stdout, "created %d finish-step(s) for %s (condition matched in %s)\n", total, ticketID, workdir)
	return beads, nil
}

// createStepLevel materialises one sibling group as `finish-step` children of
// parentBeadID, recursing into each parent step's nested group, then wires the
// group's blocked_by edges as bd `blocks` dependencies between siblings. parent
// is the in-memory bead the new beads hang off (nil at the top level). Returns
// the group's beads (in YAML order, all `open`) and the total number of beads
// created across the whole subtree.
func createStepLevel(db, beadsDir, parentBeadID string, steps []Step, workdir string, parent *stepBead) ([]*stepBead, int, error) {
	titleToID := make(map[string]string, len(steps))
	beads := make([]*stepBead, 0, len(steps))
	total := 0
	for i, s := range steps {
		id, err := createOneStepBead(db, beadsDir, parentBeadID, s, workdir, i)
		if err != nil {
			return nil, 0, err
		}
		total++
		b := &stepBead{
			ID:       id,
			Title:    s.Title,
			Status:   "open",
			Check:    s.Check,
			Complete: s.Complete,
			Workdir:  workdir,
			Order:    i,
			parent:   parent,
		}
		titleToID[s.Title] = id
		if len(s.Steps) > 0 {
			children, sub, err := createStepLevel(db, beadsDir, id, s.Steps, workdir, b)
			if err != nil {
				return nil, 0, err
			}
			b.Children = children
			total += sub
		}
		beads = append(beads, b)
	}
	for i, s := range steps {
		for _, blockerTitle := range s.BlockedBy {
			blockerID := titleToID[blockerTitle] // validated to exist by LoadStepSets
			if err := bdDepAdd(db, beadsDir, beads[i].ID, blockerID); err != nil {
				return nil, 0, err
			}
			beads[i].Blockers = append(beads[i].Blockers, blockerID)
		}
	}
	return beads, total, nil
}

// createOneStepBead creates a single step bead as a child of parentBeadID
// (the ticket for a top-level step, or the enclosing step-bead for a nested
// one), stamps the finish-step label, and persists the step's commands,
// workdir, and order as bead metadata. A parent (group) step has empty
// check/complete, so only its workdir and order are stored.
func createOneStepBead(db, beadsDir, parentBeadID string, s Step, workdir string, order int) (string, error) {
	out, err := bdcli.Cmd(beadsDir, db, "create", s.Title, "--silent", "--parent", parentBeadID).Output()
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
