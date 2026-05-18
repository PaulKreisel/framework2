// Package pipeline loads and runs per-repo post-completion hooks declared in
// a bare repo's pipeline.md. Each entry maps a branch to a bash command that
// should run against the worktree whose branch matches when a ticket is being
// finished into that branch.
//
// pipeline.md format (whole file is YAML):
//
//	- branch: main
//	  command: make install && go test ./...
//	- branch: develop
//	  command: ./ci.sh
//
// A missing pipeline.md is not an error: it simply contributes no entries.
package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Filename is the basename of the per-bare-repo pipeline declaration.
const Filename = "pipeline.md"

// Entry is one branch-to-command mapping.
type Entry struct {
	Branch  string `yaml:"branch"`
	Command string `yaml:"command"`
}

// Result captures the outcome of running one entry. Err is set only when the
// command could not be started or its exit status is unavailable; a non-zero
// command exit is reported via ExitCode (and the caller treats that as a
// pipeline failure too).
type Result struct {
	Branch   string
	Command  string
	Worktree string
	ExitCode int
	Output   string
	Err      error
}

// Failed reports whether the pipeline run is considered unsuccessful: either
// the runner errored before/after exec, or the command exited non-zero.
func (r Result) Failed() bool {
	return r.Err != nil || r.ExitCode != 0
}

// Load reads <bareRepo>/pipeline.md and returns its entries. A missing file
// yields (nil, nil) — pipeline.md is optional.
func Load(bareRepo string) ([]Entry, error) {
	path := filepath.Join(bareRepo, Filename)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var entries []Entry
	if err := yaml.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i, e := range entries {
		if e.Branch == "" {
			return nil, fmt.Errorf("%s: entry %d is missing `branch`", path, i)
		}
		if e.Command == "" {
			return nil, fmt.Errorf("%s: entry %d (branch %q) is missing `command`", path, i, e.Branch)
		}
	}
	return entries, nil
}

// Match returns the first entry whose branch equals branch, or (Entry{}, false)
// when none does.
func Match(entries []Entry, branch string) (Entry, bool) {
	for _, e := range entries {
		if e.Branch == branch {
			return e, true
		}
	}
	return Entry{}, false
}

// Run executes entry.Command via `bash -c` with cwd set to worktree. Stdout
// and stderr are captured together into Result.Output.
func Run(entry Entry, worktree string) Result {
	res := Result{
		Branch:   entry.Branch,
		Command:  entry.Command,
		Worktree: worktree,
	}
	cmd := exec.Command("bash", "-c", entry.Command)
	cmd.Dir = worktree
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	res.Output = buf.String()
	if err == nil {
		res.ExitCode = 0
		return res
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res
	}
	res.Err = err
	res.ExitCode = -1
	return res
}
