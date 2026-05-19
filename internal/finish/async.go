package finish

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"framework2/internal/dir"
	"framework2/internal/pipeline"
)

// asyncTask is one pipeline command bound to the parent worktree it should
// run in. Serialised to JSON and handed to the detached async runner.
type asyncTask struct {
	Entry  pipeline.Entry `json:"entry"`
	Parent dir.Entry      `json:"parent"`
}

// asyncPayload is the JSON file written by [spawnPipelinesAsync] and read by
// [RunAsync]. BeadsDir is intentionally absent: the child receives it via
// BEADS_DIR in its environment so it follows the same resolution rules as
// every other ticketcli invocation.
type asyncPayload struct {
	DB       string      `json:"db,omitempty"`
	TicketID string      `json:"ticket_id"`
	Tasks    []asyncTask `json:"tasks"`
}

// computePipelineTasks dedupes parentTargets by bare repo, loads each
// pipeline.md, and matches its entries by the parent's branch. Returns the
// list of tasks the runner (inline or async) will execute. pipeline.md parse
// errors propagate so config typos still abort `this finish` rather than
// being silently swallowed in a detached process the user never sees.
func computePipelineTasks(parentTargets []dir.Entry) ([]asyncTask, error) {
	seen := map[string]bool{}
	var tasks []asyncTask
	for _, pe := range parentTargets {
		if seen[pe.BareRepo] {
			continue
		}
		seen[pe.BareRepo] = true
		entries, err := pipeline.Load(pe.BareRepo)
		if err != nil {
			return nil, err
		}
		entry, ok := pipeline.Match(entries, pe.Branch)
		if !ok {
			continue
		}
		tasks = append(tasks, asyncTask{Entry: entry, Parent: pe})
	}
	return tasks, nil
}

// executeTasks runs each pipeline task in sequence. A non-zero exit (or a
// runner error) is recorded as a `bug` bead linked back to ticketID; the
// loop then moves on to the next task. Returned error means we could not
// even create the bug bead — i.e. infrastructural failure.
func executeTasks(db, beadsDir, ticketID string, tasks []asyncTask, stdout io.Writer) error {
	for _, t := range tasks {
		fmt.Fprintf(stdout, "pipeline %s @ %s: running\n", t.Parent.Branch, t.Parent.Path)
		res := pipeline.Run(t.Entry, t.Parent.Path)
		if !res.Failed() {
			fmt.Fprintf(stdout, "pipeline %s @ %s: ok\n", t.Parent.Branch, t.Parent.Path)
			continue
		}
		fmt.Fprintf(stdout, "pipeline %s @ %s: FAILED (exit=%d)\n", t.Parent.Branch, t.Parent.Path, res.ExitCode)
		bugID, err := createBugBead(db, beadsDir, ticketID, t.Parent, res)
		if err != nil {
			return fmt.Errorf("record pipeline failure for %s: %w", ticketID, err)
		}
		fmt.Fprintf(stdout, "pipeline failure recorded as %s\n", bugID)
	}
	return nil
}

// spawnPipelinesAsync writes the task list to a temp JSON file and launches
// a detached `ticket pipeline-async --payload <file>` subprocess that
// executes them after the parent `this finish` has returned. The child is
// placed in a new session (Setsid) so a closing controlling terminal won't
// SIGHUP it, and its stdio is detached from the parent.
func spawnPipelinesAsync(db, beadsDir, ticketID string, tasks []asyncTask, stdout io.Writer) error {
	payload := asyncPayload{DB: db, TicketID: ticketID, Tasks: tasks}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal pipeline payload: %w", err)
	}
	tmp, err := os.CreateTemp("", "pipeline-async-*.json")
	if err != nil {
		return fmt.Errorf("create pipeline payload tempfile: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write pipeline payload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("close pipeline payload: %w", err)
	}
	ticketBin, err := findTicketBin()
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("locate ticket binary for async pipeline: %w", err)
	}
	args := []string{}
	if db != "" {
		args = append(args, "--db", db)
	}
	args = append(args, "pipeline-async", "--payload", tmp.Name())
	cmd := exec.Command(ticketBin, args...)
	cmd.Env = append(os.Environ(), "BEADS_DIR="+beadsDir)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Anchor cwd in beadsDir, which outlives every worktree this finish is
	// about to remove.
	cmd.Dir = beadsDir
	if err := cmd.Start(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("spawn async pipeline runner: %w", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release async pipeline runner: %w", err)
	}
	fmt.Fprintf(stdout, "pipeline: dispatched %d task(s) asynchronously (pid=%d)\n", len(tasks), pid)
	return nil
}

// findTicketBin prefers a sibling `ticket` next to the current executable
// (covers the `this`+`ticket` pair installed by `make install`), then falls
// back to a PATH lookup. We can't rely on os.Executable() alone because the
// parent may have been invoked as `this`, not `ticket`, and the async
// subcommand needs the ticketcli dispatcher (no .ticket-id discovery).
func findTicketBin() (string, error) {
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "ticket")
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	return exec.LookPath("ticket")
}

// runPipelinesDispatch picks the inline path under `go test` (so existing
// pipeline-outcome tests stay synchronous and deterministic) and the
// detached-subprocess path in production.
func runPipelinesDispatch(db, beadsDir, ticketID string, parentTargets []dir.Entry, stdout io.Writer) error {
	tasks, err := computePipelineTasks(parentTargets)
	if err != nil {
		return err
	}
	if len(tasks) == 0 {
		return nil
	}
	if testing.Testing() {
		return executeTasks(db, beadsDir, ticketID, tasks, stdout)
	}
	return spawnPipelinesAsync(db, beadsDir, ticketID, tasks, stdout)
}

// RunAsync is the entrypoint invoked by the hidden `ticket pipeline-async`
// subcommand. It reads the payload file written by [spawnPipelinesAsync],
// removes it, and executes the queued pipeline tasks (creating bug beads
// for any failures). By the time it runs, the parent `this finish` has
// already returned — its stdio is detached, so output here is effectively
// discarded, but bug beads still get filed.
func RunAsync(db, beadsDir, payloadPath string, stdout io.Writer) error {
	if payloadPath == "" {
		return errors.New("pipeline-async: --payload <file> required")
	}
	data, err := os.ReadFile(payloadPath)
	if err != nil {
		return fmt.Errorf("read pipeline payload %s: %w", payloadPath, err)
	}
	_ = os.Remove(payloadPath)
	var p asyncPayload
	if err := json.Unmarshal(data, &p); err != nil {
		return fmt.Errorf("parse pipeline payload %s: %w", payloadPath, err)
	}
	if db == "" {
		db = p.DB
	}
	return executeTasks(db, beadsDir, p.TicketID, p.Tasks, stdout)
}
