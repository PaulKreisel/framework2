// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package pde

import (
	"strings"
	"testing"

	"framework2/internal/dir"
	"framework2/internal/ports"
	"gopkg.in/yaml.v3"
)

func TestRenderDevserverPreset(t *testing.T) {
	body := string(Render(Options{
		TicketID:   "kn-1",
		Target:     "/t/x.target",
		BundlePool: "/pool",
		Bundles:    []Bundle{{Path: "/wt/org.knime.a", Note: "changed"}, {Path: "/ui/org.knime.ui.js", Note: "frontend"}},
		VMArgs:     []string{"-Dextra=1"},
		Devserver: &DevserverOptions{
			Block:       ports.Block{Start: 30010},
			ContextPath: "/home/.config/knime/devserver/kn-1.workflowContextConfig.yaml",
			DataDir:     "/env/.devserver-data",
			Frontend:    &Frontend{Dir: "/ui/org.knime.ui.js", Primary: dir.Entry{Path: "/ui", Branch: "b"}},
			HubAlias:    "dev_green",
			ItemPath:    "/Users/T/S/G/KEY-1",
			Link:        "https://hub/app/t/spaces/S/G/KEY-1~id",
		},
	}))
	var parsed struct {
		Launches []struct {
			Name        string   `yaml:"name"`
			Application string   `yaml:"application"`
			ProgramArgs []string `yaml:"programArgs"`
			VMArgs      []string `yaml:"vmArgs"`
			Sidecars    []struct {
				Name         string            `yaml:"name"`
				Command      []string          `yaml:"command"`
				Cwd          string            `yaml:"cwd"`
				Env          map[string]string `yaml:"env"`
				ReadyPattern string            `yaml:"readyPattern"`
			} `yaml:"sidecars"`
		} `yaml:"launches"`
	}
	if err := yaml.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("not yaml: %v\n%s", err, body)
	}
	if len(parsed.Launches) != 1 {
		t.Fatalf("launches: %d\n%s", len(parsed.Launches), body)
	}
	l := parsed.Launches[0]
	if l.Name != "devserver" || l.Application != devserverApplication {
		t.Fatalf("preset: %+v", l)
	}
	if !contains(l.ProgramArgs, "-port=30010") || !contains(l.ProgramArgs, "-workflowContextConfig=/home/.config/knime/devserver/kn-1.workflowContextConfig.yaml") {
		t.Fatalf("programArgs: %v", l.ProgramArgs)
	}
	for _, want := range []string{"-Djava.awt.headless=true", "-Dwf.mcp.autostart=true", "-Dwf.mcp.port=30011", "-Dextra=1", "-Dorg.knime.ui.dev.mode=true"} {
		if !contains(l.VMArgs, want) {
			t.Errorf("vmArgs missing %s", want)
		}
	}
	for _, unwanted := range []string{"-XstartOnFirstThread", "-Dsun.java2d.metal=false", "-Dp2.trustedAuthorities=https://update.knime.com"} {
		if contains(l.VMArgs, unwanted) {
			t.Errorf("vmArgs has desktop-only %s", unwanted)
		}
	}
	if len(l.Sidecars) != 1 {
		t.Fatalf("sidecars: %+v", l.Sidecars)
	}
	s := l.Sidecars[0]
	if s.Name != "editor" || strings.Join(s.Command, " ") != "pnpm dev" || s.Cwd != "/ui/org.knime.ui.js" {
		t.Fatalf("sidecar: %+v", s)
	}
	if s.Env["KNIME_EDITOR_PORT"] != "30012" || s.Env["KNIME_BROWSER_DEV_WS_URL"] != "ws://localhost:30010" || s.Env["KNIME_BROWSER_DEV_HTTP_URL"] != "http://localhost:30010" {
		t.Fatalf("sidecar env: %v", s.Env)
	}
	if s.ReadyPattern != `Local:\s+(http\S+)` {
		t.Fatalf("readyPattern: %q", s.ReadyPattern)
	}
	for _, want := range []string{"dev_green", "/Users/T/S/G/KEY-1", "https://hub/app/t/spaces/S/G/KEY-1~id", "30013-30019 spare", "devserver-hub-down", "dev:NodeDialog"} {
		if !strings.Contains(body, want) {
			t.Errorf("comment missing %q", want)
		}
	}
	if !strings.HasPrefix(body, "# "+GeneratedMarker) {
		t.Errorf("header missing")
	}
}

func TestRenderAPPresetWithoutDevserver(t *testing.T) {
	body := string(Render(Options{TicketID: "kn-1", Target: "/t", DataDir: "/d", ProgramArgs: []string{"-preferences", "/p.epf"}}))
	if !strings.Contains(body, "name: AP") || !strings.Contains(body, knimeProduct) || strings.Contains(body, "sidecars:") {
		t.Fatalf("AP preset:\n%s", body)
	}
}

func TestAppendFrontendBundle(t *testing.T) {
	fe := &Frontend{
		Dir:     "/wt/primary/org.knime.ui.js",
		Primary: dir.Entry{Path: "/wt/primary"},
		Others:  []dir.Entry{{Path: "/wt/other"}},
	}
	got := appendFrontendBundle([]Bundle{
		{Path: "/wt/other/org.knime.ui.js", Note: "changed"},
		{Path: "/wt/x/org.knime.a", Note: "changed"},
	}, fe)
	paths := make([]string, 0, len(got))
	for _, b := range got {
		paths = append(paths, b.Path)
	}
	if strings.Join(paths, ",") != "/wt/x/org.knime.a,/wt/primary/org.knime.ui.js" {
		t.Fatalf("bundles: %v", paths)
	}
	got = appendFrontendBundle([]Bundle{{Path: fe.Dir, Note: "changed"}}, fe)
	if len(got) != 1 || !strings.Contains(got[0].Note, "frontend") {
		t.Fatalf("listed frontend duplicated or unmarked: %+v", got)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
