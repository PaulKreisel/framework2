package finish

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"framework2/internal/bdcli"
	"framework2/internal/dir"
	"framework2/internal/project"

	"gopkg.in/yaml.v3"
)

// StepsFilename is the basename of the finish-step declaration. It is read
// from each linked bare repo's root (same convention as pipeline.md) and, as a
// project-wide fallback, from the BEADS_DIR root. See [findStep].
const StepsFilename = "finish-steps.yaml"

// StepLabel is the literal label stamped onto every step-bead created by the
// step-driven finish path, so the beads are queryable as a group.
const StepLabel = "finish-step"

// Step is one entry in finish-steps.yaml.
//
// finish-steps.yaml format (whole file is YAML):
//
//	- selector: test -f go.mod          # gate; first exit-0 wins
//	  title: build & test               # optional step-bead title
//	  complete: go build ./...          # optional; run before check
//	  check: go test ./...              # required; exit 0 closes the step
//	- selector: true
//	  check: ./ci.sh
//
// Selectors are evaluated top to bottom; the first whose `selector` command
// exits 0 is the step that runs. `selector` and `check` are required;
// `complete` and `title` are optional.
type Step struct {
	Selector string `yaml:"selector"`
	Title    string `yaml:"title"`
	Complete string `yaml:"complete"`
	Check    string `yaml:"check"`
}

// LoadSteps reads <bareRepo>/finish-steps.yaml and returns its steps. A
// missing file yields (nil, nil) — finish-steps.yaml is optional (its absence
// is what selects the Go fallback). Parse/validation errors propagate so a
// config typo aborts `this finish` loudly rather than silently dropping to the
// fallback.
func LoadSteps(bareRepo string) ([]Step, error) {
	path := filepath.Join(bareRepo, StepsFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var steps []Step
	if err := yaml.Unmarshal(data, &steps); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i, s := range steps {
		if s.Selector == "" {
			return nil, fmt.Errorf("%s: entry %d is missing `selector`", path, i)
		}
		if s.Check == "" {
			return nil, fmt.Errorf("%s: entry %d is missing `check`", path, i)
		}
	}
	return steps, nil
}

// selectStep returns the first step whose selector command exits 0 when run in
// workdir, or (Step{}, false) when none match.
func selectStep(steps []Step, workdir string) (Step, bool) {
	for _, s := range steps {
		if runShell(s.Selector, workdir).exit == 0 {
			return s, true
		}
	}
	return Step{}, false
}

// runStepBead is the step-driven finish path. It looks for a finish-steps.yaml
// in a linked worktree's bare repo, evaluates its selectors, and — when one
// matches — creates a `finish-step` step-bead under ticketID, runs the step's
// `complete` (if present) then `check` in the worktree, closes the step on a
// clean check, and runs the shared cleanup tail.
//
// It reports handled=true when the step path took over (so the caller skips
// the Go fallback). handled=false means no finish-steps.yaml was found or no
// selector matched: the caller should run the monolithic Go finish unchanged.
func runStepBead(db, beadsDir, ticketID string, worktrees []dir.Entry, stdout io.Writer) (handled bool, err error) {
	workdir, step, ok, err := findStep(beadsDir, ticketID, worktrees)
	if err != nil || !ok {
		return false, err
	}

	title := step.Title
	if title == "" {
		title = fmt.Sprintf("finish-step: %s", ticketID)
	}
	stepID, err := createStepBead(db, beadsDir, ticketID, title)
	if err != nil {
		return false, err
	}
	fmt.Fprintf(stdout, "finish-step %s created for %s (selector matched in %s)\n", stepID, ticketID, workdir)

	if step.Complete != "" {
		fmt.Fprintf(stdout, "finish-step %s: complete\n", stepID)
		if res := runShell(step.Complete, workdir); res.exit != 0 {
			return false, fmt.Errorf("finish-step %s: complete command failed (exit=%d):\n%s",
				stepID, res.exit, strings.TrimSpace(res.output))
		}
	}

	fmt.Fprintf(stdout, "finish-step %s: check\n", stepID)
	if res := runShell(step.Check, workdir); res.exit != 0 {
		return false, fmt.Errorf("finish-step %s: check command failed (exit=%d); step left open for follow-up:\n%s",
			stepID, res.exit, strings.TrimSpace(res.output))
	}

	if err := bdClose(db, beadsDir, stepID); err != nil {
		return false, err
	}
	fmt.Fprintf(stdout, "finish-step %s closed\n", stepID)

	if err := cleanup(db, beadsDir, ticketID, worktrees, stdout); err != nil {
		return false, err
	}
	return true, nil
}

// findStep locates the finish step to run and the working directory its
// commands run in. It checks, in order:
//
//  1. each linked worktree's bare repo root (<bareRepo>/finish-steps.yaml),
//     evaluating selectors in that worktree — the spec'd per-repo location;
//  2. a global <beadsDir>/finish-steps.yaml fallback, evaluating selectors in
//     [globalWorkdir] (the first worktree, else the env folder). This is the
//     one config that applies across every repo, so a single file can drive
//     finish for all of a project's tickets without copying it into ~85 bare
//     repos.
//
// The returned workdir is where the step's complete/check commands run.
// ok=false means nothing matched anywhere — the Go fallback applies. A
// parse error in any encountered file propagates.
func findStep(beadsDir, ticketID string, worktrees []dir.Entry) (workdir string, step Step, ok bool, err error) {
	for _, wt := range worktrees {
		steps, err := LoadSteps(wt.BareRepo)
		if err != nil {
			return "", Step{}, false, err
		}
		if steps == nil {
			continue
		}
		if s, ok := selectStep(steps, wt.Path); ok {
			return wt.Path, s, true, nil
		}
	}

	globalSteps, err := LoadSteps(beadsDir)
	if err != nil {
		return "", Step{}, false, err
	}
	if globalSteps != nil {
		wd := globalWorkdir(beadsDir, ticketID, worktrees)
		if s, ok := selectStep(globalSteps, wd); ok {
			return wd, s, true, nil
		}
	}
	return "", Step{}, false, nil
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

// createStepBead creates a child bead of ticketID and stamps the finish-step
// label onto it. Returns the new step-bead id.
func createStepBead(db, beadsDir, ticketID, title string) (string, error) {
	out, err := bdcli.Cmd(beadsDir, db, "create", title, "--silent", "--parent", ticketID).Output()
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
	if lout, err := bdcli.Cmd(beadsDir, db, "label", "add", stepID, StepLabel).CombinedOutput(); err != nil {
		return stepID, fmt.Errorf("bd label add %s %s: %w: %s", stepID, StepLabel, err, strings.TrimSpace(string(lout)))
	}
	return stepID, nil
}

// shellResult is the captured outcome of one finish-step shell command.
type shellResult struct {
	exit   int
	output string
}

// runShell runs cmd via `bash -c` with cwd set to workdir, capturing stdout
// and stderr together. A command that cannot be started reports exit=-1.
func runShell(cmd, workdir string) shellResult {
	c := exec.Command("bash", "-c", cmd)
	c.Dir = workdir
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
