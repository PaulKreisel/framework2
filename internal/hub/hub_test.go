// Tests in this repo are DISABLED ON PURPOSE. Do not remove this build tag
// and do not "fix" it — see TESTING.md ("Tests are disabled on purpose").
// To run this file anyway: go test -tags runtests ./...
//go:build runtests

package hub

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndWeb(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "hubs.yaml")
	os.WriteFile(file, []byte(`hubs:
  dev_green:
    url: https://api.dev-green.example.com/
    appPasswordIdEnv: DEVGREEN_AP_ID
    appPasswordEnv: DEVGREEN_AP_SECRET
  other:
    url: https://api.other.example.com
    webUrl: https://web.other.example.com
    appPasswordIdEnv: A
    appPasswordEnv: B
`), 0o644)
	h, err := Load(file, "dev_green")
	if err != nil {
		t.Fatal(err)
	}
	if h.URL != "https://api.dev-green.example.com" || h.Web() != "https://dev-green.example.com" {
		t.Fatalf("hub: %+v web %s", h, h.Web())
	}
	o, _ := Load(file, "other")
	if o.Web() != "https://web.other.example.com" {
		t.Fatalf("webUrl override ignored: %s", o.Web())
	}
	if _, err := Load(file, "nope"); err == nil || !strings.Contains(err.Error(), "dev_green, other") {
		t.Fatalf("unknown alias error: %v", err)
	}
}

func TestLoadCredentialsNeverLeaksValues(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.env"), []byte("export X_ID=\"id-1\"\n# comment\nX_SECRET='s3cret'\n"), 0o600)
	h := Hub{IDEnv: "X_ID", SecretEnv: "X_SECRET"}
	c, err := LoadCredentials(dir, h)
	if err != nil {
		t.Fatal(err)
	}
	if c.id != "id-1" || c.secret != "s3cret" {
		t.Fatalf("parsed: %q %q", c.id, c.secret)
	}
	if c.String() != "<redacted>" {
		t.Fatalf("String leaks: %s", c.String())
	}
	_, err = LoadCredentials(dir, Hub{IDEnv: "MISSING_ID", SecretEnv: "X_SECRET"})
	if !errors.Is(err, ErrCredentials) || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("missing credentials: %v", err)
	}
}

func TestClientItemsAndFrontendURL(t *testing.T) {
	var gotAuth, gotPut string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		gotAuth = user + ":" + pass
		switch {
		case r.URL.Path == "/accounts/identity":
			w.Write([]byte(`{"id":"account:user:1","name":"paul"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/repository/Users/Team A/Space/missing":
			w.WriteHeader(404)
		case r.Method == http.MethodGet && r.URL.Path == "/repository/Users/Team A/Space":
			w.Write([]byte(`{"id":"*space","path":"/Users/Team A/Space","type":"Space"}`))
		case r.Method == http.MethodPut:
			gotPut = r.URL.EscapedPath() + "|" + r.Header.Get("Content-Type")
			w.WriteHeader(201)
			w.Write([]byte(`{"id":"*new","path":"/Users/Team A/Space/missing","type":"Workflow"}`))
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	c := NewClient(Hub{URL: srv.URL}, Credentials{id: "i", secret: "s"})
	ctx := context.Background()
	who, err := c.Identity(ctx)
	if err != nil || who.Name != "paul" || gotAuth != "i:s" {
		t.Fatalf("identity: %+v %v auth=%s", who, err, gotAuth)
	}
	if _, ok, err := c.ItemByPath(ctx, "/Users/Team A/Space/missing"); err != nil || ok {
		t.Fatalf("missing item: ok=%v err=%v", ok, err)
	}
	space, ok, err := c.ItemByPath(ctx, "/Users/Team A/Space")
	if err != nil || !ok || space.ID != "*space" {
		t.Fatalf("space: %+v %v %v", space, ok, err)
	}
	item, err := c.CreateWorkflow(ctx, "/Users/Team A/Space/missing")
	if err != nil || item.ID != "*new" || !strings.Contains(gotPut, "application/json") || !strings.HasPrefix(gotPut, "/repository/Users/Team%20A/Space/missing|") {
		t.Fatalf("create: %+v %v put=%s", item, err, gotPut)
	}
	link := FrontendURL("https://hub.example.com", "/Users/Team A/Space/Group X/KEY-1", "*abc")
	if link != "https://hub.example.com/app/team%20a/spaces/Space/Group%20X/KEY-1~abc" {
		t.Fatalf("link: %s", link)
	}
}
