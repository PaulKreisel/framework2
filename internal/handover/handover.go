// Package handover implements the `ticket handover` subcommand: it produces
// a portable, single-file JSON package describing a ticket subtree so a
// colleague can review the work using only `bd` plus the project's git
// remote.
//
// The package is non-destructive: it never closes beads, removes worktrees,
// or touches the env folder. It is not invoked from `this finish`. The
// snapshot reflects state at the moment Run is called.
//
// Output shape (single JSON file):
//
//	{
//	  "beads": [ /* bd export rows for the subtree, host fields stripped;
//	               conversations already live under metadata.agents[].conversation[] */ ],
//	  "code": [
//	    {
//	      "repo": "<bare-repo-name>",
//	      "commits": [ { "SHA": "...", "message": "...", "bead_id": "ai-vrc.57.x" } ]
//	    }
//	  ]
//	}
//
// Diffs are not shipped: the reviewer fetches code from a shared remote
// using the SHAs. The author is expected to have pushed every ticket
// branch to a reachable remote before running handover.
package handover

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Package is the top-level JSON document emitted by Run.
type Package struct {
	Beads []map[string]any `json:"beads"`
	Code  []RepoSection    `json:"code"`
}

// RepoSection is one bare repo's slice of the handover: the repo's
// portable name plus every commit in the subtree authored against it.
type RepoSection struct {
	Repo    string   `json:"repo"`
	Commits []Commit `json:"commits"`
}

// Commit is one row in a RepoSection.commits[]. The field set is fixed by
// the handover spec: SHA, message, bead_id (in that order).
type Commit struct {
	SHA     string `json:"SHA"`
	Message string `json:"message"`
	BeadID  string `json:"bead_id"`
}

// Run produces a handover JSON file for ticketID and its descendants.
//
// outputOverride, when non-empty, sets the final file path. Otherwise the
// default location is <beadsDir>/handovers/<id>-<UTC YYYYMMDD-HHMMSS>.json.
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

	code, err := collectCode(db, beadsDir, order)
	if err != nil {
		return err
	}
	totalCommits := 0
	for _, r := range code {
		totalCommits += len(r.Commits)
	}
	fmt.Fprintf(stdout, "  collected %d commit(s) across %d repo(s)\n", totalCommits, len(code))

	pkg := Package{
		Beads: subtreeBeadsRaw(order, beads),
		Code:  code,
	}

	outPath, err := resolveOutputPath(beadsDir, ticketID, outputOverride)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return fmt.Errorf("create handover parent: %w", err)
	}
	if err := writeJSON(outPath, pkg); err != nil {
		return fmt.Errorf("write handover json: %w", err)
	}

	fmt.Fprintln(stdout, outPath)
	return nil
}

func resolveOutputPath(beadsDir, ticketID, outputOverride string) (string, error) {
	if outputOverride != "" {
		return outputOverride, nil
	}
	ts := time.Now().UTC().Format("20060102-150405")
	return filepath.Join(beadsDir, "handovers", fmt.Sprintf("%s-%s.json", ticketID, ts)), nil
}

// writeJSON serialises pkg to path atomically: write to a sibling .tmp,
// then rename. Pretty-printed so reviewers can read it without tooling.
func writeJSON(path string, pkg Package) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(pkg); err != nil {
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
