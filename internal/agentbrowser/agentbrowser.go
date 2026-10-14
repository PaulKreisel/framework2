// Package agentbrowser talks to the KNIME Agent Browser: one Chromium
// process (the app bundle in ~/Applications, built from Playwright's
// Chromium with its own name and icon) that every Claude Code session's
// Playwright MCP attaches to over the DevTools protocol. One Dock icon for
// all sessions, and the user can watch the agents work. A ticket's tab is
// found by its URL: the editor frontend port is unique per ticket.
package agentbrowser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"framework2/internal/devserver"
	"framework2/internal/env"
	"framework2/internal/wfmcp"
)

const (
	// Port is the DevTools port of the agent browser; the Playwright MCP is
	// registered with `--cdp-endpoint http://127.0.0.1:<Port>`.
	Port = 9333
	// AppName is the bundle name in ~/Applications and the Dock.
	AppName = "KNIME Agent Browser"
	// DevServerJobID is the single job of the gateway dev server; the editor
	// URL of a ticket is http://localhost:<editor port>/#/workflow/<id>/root.
	DevServerJobID = "11111111-1111-1111-1111-111111111111"
)

func endpoint(path string) string { return fmt.Sprintf("http://127.0.0.1:%d%s", Port, path) }

// AppPath is the app bundle.
func AppPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Applications", AppName+".app")
}

// DataDir is the browser profile of the agent browser (never the user's own).
func DataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "knime", "agent-browser")
}

// launcher is the bundle's main executable, a script written by the install
// script of the browser-editor-devserver skill. It adds the Chromium flags
// (the DevTools Port, DataDir as the profile, the mock keychain) and execs
// the real binary, so a start from the Dock or Spotlight carries the same
// flags as one by Start. The flags are listed only there.
//
// Two flags matter beyond the port and the profile. --use-mock-keychain:
// without it Chromium asks for "Chromium Safe Storage" in the keychain at
// every start, since the ad-hoc signature changes with each install. And not
// --no-startup-window: together with --remote-debugging-port that flag makes
// Chromium hold a REMOTE_DEBUGGING keep-alive which only a DevTools
// Browser.close releases (chrome_devtools_manager_delegate.cc, "controlled
// entirely by the automation process"). Cmd+Q and the quit of a macOS
// shutdown then set "trying to quit", close the windows and wait forever.
func launcher() string {
	return filepath.Join(AppPath(), "Contents", "MacOS", AppName)
}

// Running reports whether the DevTools endpoint answers.
func Running() bool {
	c := http.Client{Timeout: 700 * time.Millisecond}
	resp, err := c.Get(endpoint("/json/version"))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// Start launches the app bundle through LaunchServices without bringing it
// to the front (`open -g`), with startURL in its startup window (about:blank
// when empty), and waits for the DevTools port. The flags come from the
// bundle's launcher. Nothing keeps the browser alive afterwards: Cmd+Q quits
// it.
func Start(ctx context.Context, startURL string) error {
	if _, err := os.Stat(launcher()); err != nil {
		return fmt.Errorf("%s is not installed with its launcher (%s); run the install script of the browser-editor-devserver skill", AppName, launcher())
	}
	if err := os.MkdirAll(DataDir(), 0o700); err != nil {
		return err
	}
	if err := markCleanExit(); err != nil {
		return err
	}
	if startURL == "" {
		startURL = "about:blank"
	}
	args := []string{"-g", "-a", AppPath(), "--args", startURL}
	if out, err := exec.Command("open", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("start %s: %v: %s", AppName, err, strings.TrimSpace(string(out)))
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if Running() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("%s did not open port %d within 20 s (an instance of an older install without the launcher? quit it, re-run the install script and retry)", AppName, Port)
}

// markCleanExit sets profile.exit_type=Normal in the profile's Preferences
// so a start after a kill shows no "Restore pages?" bubble. (The "Hold Cmd+Q
// to Quit" warning is a Local State pref that defaults to off; it was never
// what blocked quitting, see Args.)
func markCleanExit() error {
	p := filepath.Join(DataDir(), "Default", "Preferences")
	prefs := map[string]any{}
	if b, err := os.ReadFile(p); err == nil {
		if err := json.Unmarshal(b, &prefs); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
	}
	profile, _ := prefs["profile"].(map[string]any)
	if profile == nil {
		profile = map[string]any{}
	}
	if t, _ := profile["exit_type"].(string); t == "Normal" {
		return nil
	}
	profile["exit_type"] = "Normal"
	profile["exited_cleanly"] = true
	prefs["profile"] = profile
	b, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

type target struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	URL  string `json:"url"`
}

func targets() ([]target, error) {
	resp, err := http.Get(endpoint("/json"))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out []target
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// FindTab returns the id of the first page whose URL starts with prefix.
func FindTab(prefix string) (string, error) {
	ts, err := targets()
	if err != nil {
		return "", err
	}
	for _, t := range ts {
		if t.Type == "page" && strings.HasPrefix(t.URL, prefix) {
			return t.ID, nil
		}
	}
	return "", nil
}

// OpenTab creates a new tab at the URL and returns its id.
func OpenTab(rawURL string) (string, error) {
	req, err := http.NewRequest(http.MethodPut, endpoint("/json/new?"+rawURL), nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var t target
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return "", err
	}
	return t.ID, nil
}

// Activate brings the tab to the front of its window.
func Activate(id string) error {
	resp, err := http.Get(endpoint("/json/activate/" + id))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Focus brings the agent browser's window to the front of the screen.
func Focus() {
	_ = exec.Command("open", "-a", AppName).Run()
}

// EditorURL is the browser-editor URL of a ticket's devserver instance.
func EditorURL(inst wfmcp.Instance) string {
	return fmt.Sprintf("http://localhost:%d/#/workflow/%s/root", inst.Port+1, DevServerJobID)
}

// View implements `this view`: show the ticket's editor tab in the agent
// browser, creating it when there is none. The tab is the one the ticket's
// Playwright sessions use too (they pick it by URL), so user and agent look
// at the same page.
func View(cwd string, stdout io.Writer) error {
	_, envDir, err := env.Discover(cwd)
	if err != nil {
		return err
	}
	instances, err := wfmcp.Instances(envDir)
	if err != nil {
		return err
	}
	if len(instances) == 0 {
		return fmt.Errorf("no devserver preset in %s/pde*.yaml; run `this pde` first", envDir)
	}
	inst := instances[0]
	gateway, wfPort, editor := inst.Port-1, inst.Port, inst.Port+1
	switch {
	case devserver.Listening(wfPort):
		fmt.Fprintf(stdout, "backend: running (gateway %d, wf-mcp %d)\n", gateway, wfPort)
	case devserver.BackendPID(wfPort) != 0:
		fmt.Fprintf(stdout, "backend: starting (pid %d); the tab shows the editor once `[backend] ready:` was printed, reload it then\n", devserver.BackendPID(wfPort))
	default:
		fmt.Fprintf(stdout, "backend: down; `pde launch devserver` in %s starts it, the tab shows a skeleton until then\n", envDir)
	}
	if !devserver.Listening(editor) {
		fmt.Fprintf(stdout, "editor frontend: not serving on %d yet\n", editor)
	}
	target := EditorURL(inst)
	prefix := fmt.Sprintf("http://localhost:%d/", editor)
	started := false
	if !Running() {
		fmt.Fprintf(stdout, "%s is not running; starting it with the editor tab\n", AppName)
		if err := Start(context.Background(), target); err != nil {
			return err
		}
		started = true
	}
	id, err := FindTab(prefix)
	if err != nil {
		return fmt.Errorf("agent browser: %w", err)
	}
	// The startup window of a fresh start may not be listed yet.
	for i := 0; started && id == "" && i < 10; i++ {
		time.Sleep(300 * time.Millisecond)
		if id, err = FindTab(prefix); err != nil {
			return fmt.Errorf("agent browser: %w", err)
		}
	}
	if id == "" {
		if id, err = OpenTab(target); err != nil {
			return fmt.Errorf("open tab: %w", err)
		}
		fmt.Fprintf(stdout, "opened %s in %s\n", target, AppName)
	} else {
		fmt.Fprintf(stdout, "showing the existing tab %s in %s\n", prefix, AppName)
	}
	if err := Activate(id); err != nil {
		return err
	}
	Focus()
	return nil
}

// URLPrefix of a ticket's editor, for callers that only have the port.
func URLPrefix(editorPort int) string {
	u := url.URL{Scheme: "http", Host: fmt.Sprintf("localhost:%d", editorPort), Path: "/"}
	return u.String()
}
