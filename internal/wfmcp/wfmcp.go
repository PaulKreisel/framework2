// Package wfmcp implements `this wf-mcp-proxy`: a stdio MCP server, registered
// once machine-wide in Claude Code, that forwards every tool call to the
// workflow-building MCP server inside the ticket's running browser-editor
// backend.
//
// The ticket comes from the working directory (Claude Code starts stdio
// servers in the project folder, which is the ticket env). The backend's port
// comes from the env's pde*.yaml: every `devserver*` launch preset's
// `-Dwf.mcp.port=`. No port, name or instance is ever chosen by the agent.
//
// Tools are listed from the running backend and cached on disk, so a session
// started before the backend is up still sees the tool list; calls then fail
// with a message naming the launch command.
package wfmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"framework2/internal/devserver"
	"framework2/internal/env"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

// ServerName is the MCP server name Claude Code registers the proxy under.
const ServerName = "wf-mcp"

// refusedTools open the embedded browser inside the backend, which crashes the
// headless JVM. They are answered with an error instead of forwarded.
var refusedTools = map[string]string{
	"get_node_view":            "node views are checked in the browser editor (open the printed editor URL), not through the headless backend",
	"get_workflow_image":       "workflow images are not available from the headless backend; look at the browser editor instead",
	"get_component_view_image": "component views are checked in the browser editor, not through the headless backend",
}

// Instance is one `devserver*` preset of one pde config file.
type Instance struct {
	Config string
	Preset string
	Port   int
}

// Endpoint is the backend's streamable HTTP MCP endpoint.
func (i Instance) Endpoint() string { return fmt.Sprintf("http://127.0.0.1:%d/mcp", i.Port) }

// Main runs the proxy until stdin closes. Logs go to stderr; stdout is the
// MCP transport.
func Main(cwd string, stdout, stderr io.Writer) error {
	logger := log.New(stderr, "wf-mcp-proxy: ", log.Ltime)
	p := &proxy{logger: logger}
	id, envDir, err := env.Discover(cwd)
	if err != nil {
		logger.Printf("no ticket env from %s: %v (no tools)", cwd, err)
	} else {
		p.ticket = id
		p.envDir = envDir
	}
	server := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: "1"}, &mcp.ServerOptions{
		Instructions: p.instructions(),
	})
	p.server = server
	server.AddTool(&mcp.Tool{
		Name:        "editor_status",
		Description: "Reports the browser-editor instances of the current ticket (from the env's pde*.yaml) and whether their backend is running. Use it when a tool call reports that no editor instance is running.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}, p.status)
	if p.ticket != "" {
		p.installTools(context.Background(), false)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = server.Run(ctx, &mcp.StdioTransport{})
	p.closeBackend()
	return err
}

type proxy struct {
	logger *log.Logger
	ticket string
	envDir string
	server *mcp.Server

	mu        sync.Mutex
	session   *mcp.ClientSession
	connected Instance
	installed map[string]bool
}

func (p *proxy) instructions() string {
	if p.ticket == "" {
		return "No ticket env found from the working directory, so no workflow tools are available. Start Claude Code inside a ticket env folder."
	}
	return fmt.Sprintf("Workflow-building tools of ticket %s, forwarded to the browser-editor backend started with `pde launch devserver` in %s. "+
		"When a call reports that no editor instance is running, start it (`pde compile && pde launch devserver` in the env folder) and retry; do not pick ports or names.", p.ticket, p.envDir)
}

// instances parses every pde*.yaml of the env.
func (p *proxy) instances() ([]Instance, error) {
	if p.envDir == "" {
		return nil, nil
	}
	files, err := filepath.Glob(filepath.Join(p.envDir, "pde*.yaml"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []Instance
	for _, f := range files {
		found, err := parseInstances(f)
		if err != nil {
			p.logger.Printf("skip %s: %v", f, err)
			continue
		}
		out = append(out, found...)
	}
	// pde.yaml's default preset first, so the plain launch is the one tried first.
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := filepath.Base(out[i].Config) == "pde.yaml", filepath.Base(out[j].Config) == "pde.yaml"
		return di && !dj
	})
	return out, nil
}

var portArg = regexp.MustCompile(`^-Dwf\.mcp\.port=(\d+)$`)

func parseInstances(file string) ([]Instance, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Launches []struct {
			Name   string   `yaml:"name"`
			VMArgs []string `yaml:"vmArgs"`
		} `yaml:"launches"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	var out []Instance
	for _, l := range cfg.Launches {
		if !strings.HasPrefix(l.Name, "devserver") {
			continue
		}
		for _, a := range l.VMArgs {
			if m := portArg.FindStringSubmatch(a); m != nil {
				port, _ := strconv.Atoi(m[1])
				out = append(out, Instance{Config: file, Preset: l.Name, Port: port})
				break
			}
		}
	}
	return out, nil
}

// listening reports whether something accepts connections on the port.
func listening(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// backend returns a session to the running instance, connecting when needed.
// With wait, a backend whose JVM is up but whose port is not open yet is
// waited for (bounded by startingWait); without, it counts as not running.
// Startup must not wait: Claude Code gives the MCP server a short time to
// answer initialize. Caller holds p.mu.
func (p *proxy) backend(ctx context.Context, wait bool) (*mcp.ClientSession, Instance, error) {
	if p.session != nil {
		return p.session, p.connected, nil
	}
	instances, err := p.instances()
	if err != nil {
		return nil, Instance{}, err
	}
	if len(instances) == 0 {
		return nil, Instance{}, fmt.Errorf("no `devserver` launch preset with -Dwf.mcp.port in %s/pde*.yaml; run `this pde` first", p.envDir)
	}
	var tried []string
	for _, inst := range instances {
		if !listening(inst.Port) {
			tried = append(tried, fmt.Sprintf("%s/%s (port %d, not listening)", filepath.Base(inst.Config), inst.Preset, inst.Port))
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		session, err := mcp.NewClient(&mcp.Implementation{Name: ServerName + "-proxy", Version: "1"}, nil).
			Connect(cctx, &mcp.StreamableClientTransport{Endpoint: inst.Endpoint(), DisableStandaloneSSE: true}, nil)
		cancel()
		if err != nil {
			tried = append(tried, fmt.Sprintf("%s/%s (port %d, MCP initialize failed: %v)", filepath.Base(inst.Config), inst.Preset, inst.Port, err))
			continue
		}
		p.session, p.connected = session, inst
		p.logger.Printf("connected to %s/%s on port %d", filepath.Base(inst.Config), inst.Preset, inst.Port)
		return session, inst, nil
	}
	// Nothing listens. A backend JVM that was started with this port is still
	// starting (20 to 60 s from launch to an open port): wait for it instead
	// of failing, bounded well below the MCP client's tool timeout.
	for _, inst := range instances {
		if !wait {
			break
		}
		pid := devserver.BackendPID(inst.Port)
		if pid == 0 {
			continue
		}
		p.logger.Printf("backend pid %d for port %d is starting; waiting up to %s", pid, inst.Port, startingWait)
		deadline := time.Now().Add(startingWait)
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return nil, Instance{}, ctx.Err()
			case <-time.After(time.Second):
			}
			if !listening(inst.Port) {
				if devserver.BackendPID(inst.Port) == 0 {
					return nil, Instance{}, fmt.Errorf("the backend of %s (pid %d) exited while starting; check the launch log in %s, then `pde launch devserver` again",
						p.ticket, pid, p.envDir)
				}
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			session, err := mcp.NewClient(&mcp.Implementation{Name: ServerName + "-proxy", Version: "1"}, nil).
				Connect(cctx, &mcp.StreamableClientTransport{Endpoint: inst.Endpoint(), DisableStandaloneSSE: true}, nil)
			cancel()
			if err != nil {
				continue // the port is open but the MCP server is not serving yet
			}
			p.session, p.connected = session, inst
			p.logger.Printf("connected to %s/%s on port %d after waiting for the start", filepath.Base(inst.Config), inst.Preset, inst.Port)
			return session, inst, nil
		}
		return nil, Instance{}, fmt.Errorf("the backend of %s is still starting (pid %d, port %d not serving after %s); call again in a moment, or wait for the `[backend] ready:` line of `pde launch devserver`",
			p.ticket, pid, inst.Port, startingWait)
	}
	return nil, Instance{}, fmt.Errorf("no editor instance running for %s; run `pde launch devserver` in %s and retry. Checked: %s",
		p.ticket, p.envDir, strings.Join(tried, "; "))
}

// startingWait bounds one tool call's wait for a backend that is starting.
// Claude Code's MCP tool timeout can be as low as 60 s; a call that returns
// "still starting" is retried by the agent, a timed-out one is not.
const startingWait = 45 * time.Second

// Caller holds p.mu.
func (p *proxy) dropBackend() {
	if p.session != nil {
		p.session.Close()
		p.session = nil
	}
}

func (p *proxy) closeBackend() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dropBackend()
}

// installTools registers the backend's tools, from the backend when one runs,
// else from the cache. Tools already registered are kept.
func (p *proxy) installTools(ctx context.Context, wait bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.installed == nil {
		p.installed = map[string]bool{}
	}
	var tools []*mcp.Tool
	session, _, err := p.backend(ctx, wait)
	if err == nil {
		res, lerr := session.ListTools(ctx, nil)
		if lerr != nil {
			p.logger.Printf("list tools: %v", lerr)
			p.dropBackend()
		} else {
			tools = res.Tools
			if cerr := writeCache(tools); cerr != nil {
				p.logger.Printf("write tool cache: %v", cerr)
			}
		}
	} else {
		p.logger.Printf("%v", err)
	}
	if tools == nil {
		cached, cerr := readCache()
		if cerr != nil {
			p.logger.Printf("no cached tool list (%v); tools appear after the first call against a running backend", cerr)
			return
		}
		tools = cached
		p.logger.Printf("serving %d tool(s) from the cache", len(tools))
	}
	for _, t := range tools {
		if p.installed[t.Name] {
			continue
		}
		p.installed[t.Name] = true
		name := t.Name
		p.server.AddTool(&mcp.Tool{
			Name:         t.Name,
			Title:        t.Title,
			Description:  t.Description,
			InputSchema:  t.InputSchema,
			OutputSchema: t.OutputSchema,
			Annotations:  t.Annotations,
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return p.forward(ctx, name, req.Params.Arguments)
		})
	}
}

func errorResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

// forward sends the call to the running backend. A failed call on a stale
// session (backend restarted) reconnects and retries once.
func (p *proxy) forward(ctx context.Context, name string, args json.RawMessage) (*mcp.CallToolResult, error) {
	if reason, refused := refusedTools[name]; refused {
		return errorResult("refused: " + reason), nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		session, _, err := p.backend(ctx, true)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err == nil {
			return res, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		p.logger.Printf("call %s failed (%v); reconnecting", name, err)
		p.dropBackend()
		if attempt == 1 || !retryable(err) {
			return errorResult(fmt.Sprintf("backend call failed: %v. If the backend was restarted, retry; if it is down, run `pde launch devserver` in %s", err, p.envDir)), nil
		}
	}
	return errorResult("unreachable"), nil
}

func retryable(err error) bool {
	if errors.Is(err, mcp.ErrConnectionClosed) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "session") || strings.Contains(msg, "connection") || strings.Contains(msg, "eof") || strings.Contains(msg, "404")
}

// status answers the editor_status tool.
func (p *proxy) status(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if p.ticket == "" {
		return errorResult("no ticket env found from the working directory"), nil
	}
	instances, err := p.instances()
	if err != nil {
		return errorResult(err.Error()), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ticket %s, env %s\n", p.ticket, p.envDir)
	if len(instances) == 0 {
		b.WriteString("no devserver preset in pde*.yaml; run `this pde`\n")
	}
	for _, inst := range instances {
		state := "down"
		if listening(inst.Port) {
			state = "running"
		} else if pid := devserver.BackendPID(inst.Port); pid != 0 {
			state = fmt.Sprintf("starting (backend pid %d, port not open yet; tool calls wait up to %s for it)", pid, startingWait)
		}
		fmt.Fprintf(&b, "%s preset %s: wf-mcp port %d, %s\n", filepath.Base(inst.Config), inst.Preset, inst.Port, state)
	}
	b.WriteString("start: cd <env> && pde compile && pde launch devserver")
	// A status call is a good moment to pick up tools that were not
	// available at session start.
	go p.installTools(context.Background(), true)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: b.String()}}}, nil
}

// Tool cache: the last tool list seen from any backend, shared across tickets
// (the same bundle serves every ticket).

func cachePath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "framework2", "wf-mcp-tools.json"), nil
}

func writeCache(tools []*mcp.Tool) error {
	path, err := cachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(tools, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func readCache() ([]*mcp.Tool, error) {
	path, err := cachePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var tools []*mcp.Tool
	if err := json.Unmarshal(data, &tools); err != nil {
		return nil, err
	}
	return tools, nil
}
