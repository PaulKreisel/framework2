// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package wfmcp

import (
	"log"
	"os"
	"path/filepath"
	"testing"
)

func TestInstancesFromPdeFiles(t *testing.T) {
	env := t.TempDir()
	os.WriteFile(filepath.Join(env, "pde.hubdev.yaml"), []byte(`launches:
  - name: devserver
    vmArgs: ["-ea", "-Dwf.mcp.port=30021"]
`), 0o644)
	os.WriteFile(filepath.Join(env, "pde.yaml"), []byte(`launches:
  - name: AP
    vmArgs: ["-Dwf.mcp.port=1"]
  - name: devserver
    vmArgs: ["-Dwf.mcp.port=30011"]
  - name: devserver-second
    vmArgs: ["-Dwf.mcp.port=30015"]
  - name: devserver-noport
    vmArgs: ["-ea"]
`), 0o644)
	p := &proxy{logger: log.New(os.Stderr, "", 0), envDir: env, ticket: "kn-1"}
	got, err := p.instances()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("instances: %+v", got)
	}
	if filepath.Base(got[0].Config) != "pde.yaml" || got[0].Preset != "devserver" || got[0].Port != 30011 {
		t.Fatalf("default first: %+v", got[0])
	}
	if got[1].Port != 30015 || got[2].Port != 30021 {
		t.Fatalf("order: %+v", got)
	}
}

func TestNoTicketEnvYieldsNoInstances(t *testing.T) {
	p := &proxy{logger: log.New(os.Stderr, "", 0)}
	got, err := p.instances()
	if err != nil || got != nil {
		t.Fatalf("got %v %v", got, err)
	}
}
