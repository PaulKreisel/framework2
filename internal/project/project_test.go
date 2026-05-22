package project_test

import (
	"path/filepath"
	"testing"

	"framework2/internal/project"
)

func TestPathJoinsBeadsDirWithRelativePath(t *testing.T) {
	got := project.Path("/tmp/.beads", "project.yaml")
	want := filepath.Join("/tmp/.beads", "project.yaml")
	if got != want {
		t.Errorf("Path(/tmp/.beads, project.yaml) = %q, want %q", got, want)
	}
}

func TestPathConfigFilenameConstant(t *testing.T) {
	if project.ConfigFilename != "project.yaml" {
		t.Errorf("ConfigFilename = %q, want project.yaml", project.ConfigFilename)
	}
}
