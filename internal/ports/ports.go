// Package ports is the per-project registry of local port blocks.
//
// Every ticket that runs the browser-editor dev setup needs a handful of
// local ports (gateway backend, workflow MCP, frontend dev server, room for
// more sidecars). Picking them by probing at launch time would leave the agent
// to guess, so `ticket pde` reserves one block of [BlockSize] consecutive ports
// per ticket in `<beadsDir>/ports.yaml` and writes them into the generated
// pde.yaml. `ticket teardown` frees the block. Tickets therefore never collide
// with each other, and every session of one ticket sees the same ports.
//
// The registry is the source of truth; a bind probe on each port of a
// candidate block is only a safety check against ports held by something
// outside the registry.
package ports

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"framework2/internal/project"
	"gopkg.in/yaml.v3"
)

// RegistryFile is the registry's basename inside beadsDir.
const RegistryFile = "ports.yaml"

// BlockSize is the number of consecutive ports in one block.
const BlockSize = 10

// Block is one ticket's reserved port block. Fixed roles use the first three
// ports; the rest is free for hand-added sidecars.
type Block struct {
	Start int
}

// Gateway is the backend's websocket/http port (`-port=`).
func (b Block) Gateway() int { return b.Start }

// WfMCP is the workflow-building MCP port inside the backend (`-Dwf.mcp.port=`).
func (b Block) WfMCP() int { return b.Start + 1 }

// Editor is the frontend dev server port (`KNIME_EDITOR_PORT`).
func (b Block) Editor() int { return b.Start + 2 }

// SpareFirst is the first port with no fixed role.
func (b Block) SpareFirst() int { return b.Start + 3 }

// SpareLast is the last port of the block.
func (b Block) SpareLast() int { return b.Start + BlockSize - 1 }

// Lookup returns the block registered for ticketID, if any.
func Lookup(beadsDir, ticketID string) (Block, bool, error) {
	reg, err := read(beadsDir)
	if err != nil {
		return Block{}, false, err
	}
	start, ok := reg[ticketID]
	return Block{Start: start}, ok, nil
}

// Allocate returns the ticket's block, reserving a new one when the ticket has
// none. The new block is the lowest start at or above base, in steps of
// BlockSize, that no other ticket holds and whose ports all accept a bind on
// 127.0.0.1. A block skipped because a port is held is reported on warn.
func Allocate(beadsDir, ticketID string, base int, warn io.Writer) (Block, error) {
	if ticketID == "" {
		return Block{}, errors.New("ports: ticket id required")
	}
	if base <= 0 || base+BlockSize > 65535 {
		return Block{}, fmt.Errorf("ports: invalid port base %d", base)
	}
	var out Block
	err := withLock(beadsDir, func() error {
		reg, err := read(beadsDir)
		if err != nil {
			return err
		}
		if start, ok := reg[ticketID]; ok {
			out = Block{Start: start}
			return nil
		}
		taken := map[int]bool{}
		for _, start := range reg {
			taken[start] = true
		}
		for start := base; start+BlockSize <= 65535; start += BlockSize {
			if taken[start] {
				continue
			}
			if held := heldPort(Block{Start: start}); held != 0 {
				fmt.Fprintf(warn, "WARNING: port %d is in use by something outside the registry; skipping block %d\n", held, start)
				continue
			}
			reg[ticketID] = start
			out = Block{Start: start}
			return write(beadsDir, reg)
		}
		return fmt.Errorf("ports: no free block above %d", base)
	})
	return out, err
}

// Free removes the ticket's entry. A ticket without an entry is not an error.
func Free(beadsDir, ticketID string) error {
	return withLock(beadsDir, func() error {
		reg, err := read(beadsDir)
		if err != nil {
			return err
		}
		if _, ok := reg[ticketID]; !ok {
			return nil
		}
		delete(reg, ticketID)
		return write(beadsDir, reg)
	})
}

// heldPort returns the first port of the block that refuses a loopback bind,
// or 0 when every port is free.
func heldPort(b Block) int {
	for port := b.Start; port <= b.SpareLast(); port++ {
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			return port
		}
		l.Close()
	}
	return 0
}

func registryPath(beadsDir string) string {
	return project.Path(beadsDir, RegistryFile)
}

// read parses the registry. A missing file is an empty registry.
func read(beadsDir string) (map[string]int, error) {
	data, err := os.ReadFile(registryPath(beadsDir))
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]int{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", registryPath(beadsDir), err)
	}
	reg := map[string]int{}
	if err := yaml.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", registryPath(beadsDir), err)
	}
	return reg, nil
}

// write stores the registry sorted by block start, one `ticket: start` line
// each, under a short header explaining the layout.
func write(beadsDir string, reg map[string]int) error {
	ids := make([]string, 0, len(reg))
	for id := range reg {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if reg[ids[i]] != reg[ids[j]] {
			return reg[ids[i]] < reg[ids[j]]
		}
		return ids[i] < ids[j]
	})
	var b strings.Builder
	b.WriteString("# Port blocks per ticket, reserved by `ticket pde` and freed by `ticket teardown`.\n")
	fmt.Fprintf(&b, "# Each block is %d ports: +0 gateway, +1 wf-mcp, +2 editor frontend, +3..+%d spare.\n", BlockSize, BlockSize-1)
	for _, id := range ids {
		fmt.Fprintf(&b, "%s: %d\n", id, reg[id])
	}
	path := registryPath(beadsDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// withLock serializes registry updates across processes with an advisory
// lock on <beadsDir>/.locks/ports.lock (released on process exit, so a crash
// leaves nothing stale).
func withLock(beadsDir string, fn func() error) error {
	if beadsDir == "" {
		return errors.New("ports: BEADS_DIR not set")
	}
	lockDir := project.Path(beadsDir, ".locks")
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(lockDir, "ports.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open lock file: %w", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("acquire ports lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
