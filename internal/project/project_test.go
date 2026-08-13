// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

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
