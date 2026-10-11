// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package ports

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAllocateIsStablePerTicketAndFreesOnRequest(t *testing.T) {
	beads := t.TempDir()
	var warn bytes.Buffer
	a, err := Allocate(beads, "kn-1", 40000, &warn)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Allocate(beads, "kn-2", 40000, &warn)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Allocate(beads, "kn-1", 40000, &warn)
	if err != nil {
		t.Fatal(err)
	}
	if a.Start != 40000 || b.Start != 40010 || again.Start != a.Start {
		t.Fatalf("blocks: a=%d b=%d again=%d", a.Start, b.Start, again.Start)
	}
	if a.Gateway() != 40000 || a.WfMCP() != 40001 || a.Editor() != 40002 || a.SpareFirst() != 40003 || a.SpareLast() != 40009 {
		t.Fatalf("roles: %+v", a)
	}
	data, _ := os.ReadFile(filepath.Join(beads, RegistryFile))
	if !strings.Contains(string(data), "kn-1: 40000\nkn-2: 40010\n") {
		t.Fatalf("registry:\n%s", data)
	}
	if err := Free(beads, "kn-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := Lookup(beads, "kn-1"); ok {
		t.Fatal("kn-1 still registered after Free")
	}
	c, err := Allocate(beads, "kn-3", 40000, &warn)
	if err != nil {
		t.Fatal(err)
	}
	if c.Start != 40000 {
		t.Fatalf("freed block not reused: %d", c.Start)
	}
	if err := Free(beads, "unknown"); err != nil {
		t.Fatalf("Free of unknown ticket: %v", err)
	}
}

func TestAllocateSkipsBlockWithHeldPort(t *testing.T) {
	beads := t.TempDir()
	l, err := net.Listen("tcp", "127.0.0.1:40105")
	if err != nil {
		t.Skip("port 40105 not bindable")
	}
	defer l.Close()
	var warn bytes.Buffer
	b, err := Allocate(beads, "kn-1", 40100, &warn)
	if err != nil {
		t.Fatal(err)
	}
	if b.Start != 40110 {
		t.Fatalf("expected block 40110, got %d", b.Start)
	}
	if !strings.Contains(warn.String(), "port 40105") {
		t.Fatalf("warning missing: %q", warn.String())
	}
}
