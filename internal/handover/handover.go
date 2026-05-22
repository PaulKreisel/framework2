// Package handover implements the `ticket handover` subcommand: it produces
// a portable JSONL snapshot of a ticket subtree so a colleague can review
// the work using only `bd` plus the project's git remote.
//
// The package is non-destructive: it never closes beads, removes worktrees,
// or touches the env folder. It is not invoked from `this finish`. The
// snapshot reflects state at the moment Run is called.
//
// Output shape: one JSONL row per bead in the subtree (host fields stripped
// from metadata.dirs[]; conversations already live under
// metadata.agents[].conversation[]). Code is not shipped: the reviewer
// fetches PRs from each bead's `metadata.prs` against the project's
// shared remote, and reads the `Subtask N…` trailer off the commit messages
// there to attribute work back to subtree beads.
package handover

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"framework2/internal/project"
)

// Run produces a handover JSONL file for ticketID and its descendants.
//
// outputOverride, when non-empty, sets the final file path. Otherwise the
// default location is <beadsDir>/handovers/<id>-<UTC YYYYMMDD-HHMMSS>.jsonl.
//
// Status messages are written to stdout; the final artifact path is the
// last line emitted so callers can scrape it.
func Run(db, beadsDir, ticketID, outputOverride string, stdout io.Writer) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	if beadsDir == "" {
		return errors.New("BEADS_DIR not set: cannot derive default handover path")
	}

	beads, order, err := loadSubtree(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "handover %s: subtree has %d bead(s)\n", ticketID, len(order))

	rows := subtreeBeadsRaw(order, beads)

	outPath, err := resolveOutputPath(beadsDir, ticketID, outputOverride)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create handover parent: %w", err)
	}
	if err := writeJSONL(outPath, rows); err != nil {
		return fmt.Errorf("write handover jsonl: %w", err)
	}

	fmt.Fprintln(stdout, outPath)
	return nil
}

func resolveOutputPath(beadsDir, ticketID, outputOverride string) (string, error) {
	if outputOverride != "" {
		return outputOverride, nil
	}
	ts := time.Now().UTC().Format("20060102-150405")
	return project.Path(beadsDir, filepath.Join("handovers", fmt.Sprintf("%s-%s.jsonl", ticketID, ts))), nil
}

// writeJSONL serialises rows to path atomically: write to a sibling .tmp,
// then rename. One JSON object per line, no wrapping container.
func writeJSONL(path string, rows []map[string]any) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			f.Close()
			os.Remove(tmp)
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
