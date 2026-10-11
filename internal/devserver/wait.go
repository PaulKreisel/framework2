package devserver

import (
	"context"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Listening reports whether something accepts TCP connections on the port.
func Listening(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// BackendPID returns the pid of a running backend JVM that was started with
// -Dwf.mcp.port=<port>, or 0. The backend takes 20 to 60 s from process start
// to an open port; this tells "starting" from "not launched".
func BackendPID(port int) int {
	out, err := exec.Command("ps", "-axo", "pid=,command=").Output()
	if err != nil {
		return 0
	}
	needle := fmt.Sprintf("-Dwf.mcp.port=%d", port)
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if pid, err := strconv.Atoi(f[0]); err == nil {
			return pid
		}
	}
	return 0
}

// WaitPorts blocks until every port accepts connections, then prints one
// `ready: ...` line to stdout. It is the `backend` sidecar of the devserver
// preset: pde relays the line as `[backend] ready: ...`, which is the signal
// that the backend (gateway + wf-mcp) is up. The frontend's own `[editor]
// ready:` line comes earlier and says nothing about the backend.
//
// ports: gateway first, then wf-mcp. Progress lines go to stdout every 15 s.
func WaitPorts(ctx context.Context, ports []int, stdout io.Writer) error {
	if len(ports) == 0 {
		return fmt.Errorf("devserver-wait: no ports given")
	}
	start := time.Now()
	nextNote := 15 * time.Second
	for {
		open := 0
		for _, p := range ports {
			if Listening(p) {
				open++
			}
		}
		if open == len(ports) {
			parts := []string{fmt.Sprintf("gateway ws://localhost:%d", ports[0])}
			if len(ports) > 1 {
				parts = append(parts, fmt.Sprintf("wf-mcp http://127.0.0.1:%d/mcp", ports[1]))
			}
			for _, p := range ports[2:] {
				parts = append(parts, fmt.Sprintf("port %d", p))
			}
			fmt.Fprintf(stdout, "ready: %s (after %s)\n", strings.Join(parts, ", "), time.Since(start).Round(time.Second))
			return nil
		}
		if time.Since(start) >= nextNote {
			fmt.Fprintf(stdout, "waiting for the backend: %d of %d ports open after %s\n", open, len(ports), time.Since(start).Round(time.Second))
			nextNote += 15 * time.Second
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// ParsePorts turns command-line port arguments into ints.
func ParsePorts(args []string) ([]int, error) {
	var ports []int
	for _, a := range args {
		p, err := strconv.Atoi(a)
		if err != nil || p <= 0 || p > 65535 {
			return nil, fmt.Errorf("devserver-wait: %q is not a port", a)
		}
		ports = append(ports, p)
	}
	return ports, nil
}
