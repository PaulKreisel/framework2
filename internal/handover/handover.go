// Package handover implements the `ticket handover` subcommand: it produces
// a portable, self-contained package describing a ticket subtree so a
// colleague can review the work using only `bd` + a markdown viewer.
//
// The package is non-destructive: it never closes beads, removes worktrees,
// or touches the env folder. It is not invoked from `this finish`. The
// snapshot reflects state at the moment Run is called.
//
// On-disk shape (per sibling ai-vrc.57.2, narrowed for sibling C):
//
//	<bundle-root>/
//	  README.md
//	  beads.jsonl                       (subtree-filtered bd export)
//	  commits.md                        (single chronological table across the subtree)
//	  tickets/<id>/conversation.md      (one per bead in subtree)
//
// Git bundles and the `repos.json` mapping are out of scope here: code is
// shipped via a separate push channel and the bundle deliberately stays
// markdown + JSONL only.
package handover

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"framework2/internal/dir"
)

// Run produces a handover package for ticketID and its descendants.
//
// outputOverride, when non-empty, sets the final artifact path (the tarball,
// or — under noArchive — the directory). Otherwise the default location is
// <beadsDir>/handovers/<id>-<UTC YYYYMMDD-HHMMSS>(.tar.gz)?.
//
// When noArchive is true the function leaves the directory in place; the
// default is to tar+gzip it and remove the directory.
//
// Status messages are written to stdout; the final artifact path is the
// last line emitted so callers can scrape it.
func Run(db, beadsDir, ticketID, outputOverride string, noArchive bool, stdout io.Writer) error {
	if ticketID == "" {
		return errors.New("ticket id required")
	}
	if beadsDir == "" {
		return errors.New("BEADS_DIR not set: cannot derive default handover root")
	}

	// 1. Subtree discovery (root + descendants via parent-child deps).
	beads, order, err := loadSubtree(db, beadsDir, ticketID)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "handover %s: subtree has %d bead(s)\n", ticketID, len(order))

	// 2. Resolve output paths. We always materialise the directory first
	// (under a stable name) and optionally fold it into a tarball at the
	// end.
	dirPath, archivePath, err := resolveOutputPaths(beadsDir, ticketID, outputOverride, noArchive)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dirPath), 0o755); err != nil {
		return fmt.Errorf("create handover parent: %w", err)
	}
	if _, err := os.Stat(dirPath); err == nil {
		return fmt.Errorf("handover directory already exists: %s", dirPath)
	}
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return fmt.Errorf("create handover dir: %w", err)
	}

	// 3. beads.jsonl (subtree-filtered, host-fields stripped).
	if err := writeBeadsJSONL(dirPath, order, beads); err != nil {
		return fmt.Errorf("write beads.jsonl: %w", err)
	}
	fmt.Fprintln(stdout, "  wrote beads.jsonl")

	// 4. Per-ticket conversation.md plus a single top-level commits.md.
	// The commits table is collapsed across the whole subtree: a finished
	// child's commits live on the parent's branch after FF-rebase, so
	// listing them per-bead would either double-count them or leave the
	// child file empty. We collect every (worktree, base-branch) pair in
	// the subtree and let writeCommitsMD dedupe by SHA.
	var sources []commitSource
	for _, id := range order {
		if err := writeConversationMD(dirPath, beads[id], beads); err != nil {
			return fmt.Errorf("render conversation for %s: %w", id, err)
		}
		dirs, err := loadDirEntries(db, beadsDir, id)
		if err != nil {
			return fmt.Errorf("load dirs for %s: %w", id, err)
		}
		if !hasWorktree(dirs) {
			continue
		}
		parentBranches, err := parentBranchesFor(db, beadsDir, id, dirs)
		if err != nil {
			return fmt.Errorf("resolve parent branches for %s: %w", id, err)
		}
		for _, e := range dirs {
			if e.BareRepo == "" || e.Branch == "" {
				continue
			}
			sources = append(sources, commitSource{Entry: e, Base: parentBranches[e.BareRepo]})
		}
	}
	fmt.Fprintf(stdout, "  wrote tickets/ (%d bead(s))\n", len(order))
	if err := writeCommitsMD(dirPath, sources, beads); err != nil {
		return fmt.Errorf("render commits.md: %w", err)
	}
	fmt.Fprintln(stdout, "  wrote commits.md")

	// 5. README.
	if err := writeReadme(dirPath, ticketID, beads[ticketID], order, beads); err != nil {
		return fmt.Errorf("write README: %w", err)
	}
	fmt.Fprintln(stdout, "  wrote README.md")

	// 6. Optional tarball.
	finalPath := dirPath
	if !noArchive {
		if err := writeTarball(archivePath, dirPath); err != nil {
			return fmt.Errorf("archive: %w", err)
		}
		if err := os.RemoveAll(dirPath); err != nil {
			return fmt.Errorf("remove staging dir %s: %w", dirPath, err)
		}
		finalPath = archivePath
		fmt.Fprintln(stdout, "  wrote tarball")
	}

	fmt.Fprintln(stdout, finalPath)
	return nil
}

// hasWorktree reports whether any entry was created via `dir worktree`
// (BareRepo set). Plain `dir link` entries don't participate in commits.md
// since they have no branch model.
func hasWorktree(entries []dir.Entry) bool {
	for _, e := range entries {
		if e.BareRepo != "" {
			return true
		}
	}
	return false
}

// resolveOutputPaths returns (stagingDir, archivePath). When outputOverride
// is non-empty we honour it as the final artifact path; we still need a
// staging directory to populate, even in archive mode.
func resolveOutputPaths(beadsDir, ticketID, outputOverride string, noArchive bool) (string, string, error) {
	ts := time.Now().UTC().Format("20060102-150405")
	if outputOverride != "" {
		if noArchive {
			return outputOverride, "", nil
		}
		// Tarball mode: stage next to the target archive so we don't
		// clobber whatever's at outputOverride until the very last step.
		staging := outputOverride + ".staging-" + ts
		return staging, outputOverride, nil
	}
	root := filepath.Join(beadsDir, "handovers")
	dirName := fmt.Sprintf("%s-%s", ticketID, ts)
	staging := filepath.Join(root, dirName)
	if noArchive {
		return staging, "", nil
	}
	return staging, staging + ".tar.gz", nil
}
