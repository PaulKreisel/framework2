// Package project centralises access to files that live under a bead's
// `.beads/` directory. Every production caller that needs to read or
// stat a file inside `.beads/` routes through [Path] so we never have
// two callers disagreeing on whether a given relative path already
// includes the `.beads/` prefix — the bug that motivated ai-vrc.71.
package project

import (
	"os"
	"path/filepath"
)

// File names of well-known project files. Defined here (rather than in
// each consumer) so the spelling lives next to the path helper.
const (
	// ConfigFilename is the per-project YAML config consumed by
	// internal/projectconfig. Lives at <beadsDir>/project.yaml.
	ConfigFilename = "project.yaml"
)

// Path returns the filesystem path to a project file given a path
// relative to the `.beads/` directory. Callers that need to read,
// stat, or write the file should pass the result of [Path] to the
// usual os/io helpers — keeping the join in one place.
func Path(beadsDir, relPath string) string {
	return filepath.Join(beadsDir, relPath)
}

// Dir returns the resolved `.beads/` directory for the current process,
// read from the BEADS_DIR environment variable. This is the single
// chokepoint for that env lookup: every production caller that needs to
// know the bead's directory should route through here (or accept a
// pre-resolved value from a caller that did).
func Dir() string {
	return os.Getenv("BEADS_DIR")
}
