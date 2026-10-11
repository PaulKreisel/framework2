// Package hub is the minimal KNIME Hub client `ticket pde` needs: resolve an
// alias from the hubs file, load its application password from the credential
// env files, and look up or create repository items.
//
// Credentials are loaded into private fields and never into the process
// environment, never logged and never part of an error message. The only
// place they leave this package is [Client.Credentials], which the devserver
// package uses to write the workflow context yaml the backend reads.
package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Hub is one alias from the hubs file.
type Hub struct {
	Alias string
	// URL is the API base, e.g. https://api.hubdev.knime.com.
	URL string
	// WebURL is the frontend base for links. Empty: derived from URL.
	WebURL string
	// IDEnv and SecretEnv name the environment variables (in the env files)
	// holding the application password id and secret.
	IDEnv     string
	SecretEnv string
}

type hubsFile struct {
	Hubs map[string]struct {
		URL              string `yaml:"url"`
		WebURL           string `yaml:"webUrl"`
		AppPasswordIdEnv string `yaml:"appPasswordIdEnv"`
		AppPasswordEnv   string `yaml:"appPasswordEnv"`
	} `yaml:"hubs"`
}

// Load returns the alias from the hubs file. The error names the known
// aliases when alias is unknown.
func Load(file, alias string) (Hub, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return Hub{}, fmt.Errorf("read hubs file: %w", err)
	}
	var parsed hubsFile
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return Hub{}, fmt.Errorf("parse %s: %w", file, err)
	}
	entry, ok := parsed.Hubs[alias]
	if !ok {
		known := make([]string, 0, len(parsed.Hubs))
		for k := range parsed.Hubs {
			known = append(known, k)
		}
		sort.Strings(known)
		return Hub{}, fmt.Errorf("hub alias %q not in %s (known: %s)", alias, file, strings.Join(known, ", "))
	}
	h := Hub{
		Alias:     alias,
		URL:       strings.TrimRight(entry.URL, "/"),
		WebURL:    strings.TrimRight(entry.WebURL, "/"),
		IDEnv:     entry.AppPasswordIdEnv,
		SecretEnv: entry.AppPasswordEnv,
	}
	switch {
	case h.URL == "":
		return Hub{}, fmt.Errorf("hub %q in %s has no url", alias, file)
	case h.IDEnv == "" || h.SecretEnv == "":
		return Hub{}, fmt.Errorf("hub %q in %s has no appPasswordIdEnv/appPasswordEnv", alias, file)
	}
	return h, nil
}

// Web returns the frontend base url: WebURL, else URL with a leading `api.`
// host label removed.
func (h Hub) Web() string {
	if h.WebURL != "" {
		return h.WebURL
	}
	u, err := url.Parse(h.URL)
	if err != nil {
		return h.URL
	}
	u.Host = strings.TrimPrefix(u.Host, "api.")
	return strings.TrimRight(u.String(), "/")
}

// Credentials is an application password. Both fields are private so the
// value cannot be printed by accident; %v renders it redacted.
type Credentials struct {
	id, secret string
}

// String renders redacted.
func (Credentials) String() string { return "<redacted>" }

// ErrCredentials marks a missing or empty application password.
var ErrCredentials = errors.New("hub credentials missing")

// LoadCredentials reads every `*.env` file in envDir (KEY=VALUE lines, an
// optional `export ` prefix, single or double quotes stripped) and returns the
// hub's id/secret pair. The error names the variables and files, never values.
func LoadCredentials(envDir string, h Hub) (Credentials, error) {
	files, err := filepath.Glob(filepath.Join(envDir, "*.env"))
	if err != nil {
		return Credentials{}, err
	}
	sort.Strings(files)
	vars := map[string]string{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return Credentials{}, fmt.Errorf("read credential file: %w", err)
		}
		parseEnv(string(data), vars)
	}
	c := Credentials{id: vars[h.IDEnv], secret: vars[h.SecretEnv]}
	if c.id == "" || c.secret == "" {
		return Credentials{}, fmt.Errorf("%w: %s / %s not set in %s/*.env (%d file(s) read)",
			ErrCredentials, h.IDEnv, h.SecretEnv, envDir, len(files))
	}
	return c, nil
}

func parseEnv(content string, into map[string]string) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		into[key] = value
	}
}

// Client talks to one hub with basic auth.
type Client struct {
	hub   Hub
	creds Credentials
	http  *http.Client
}

// NewClient returns a client for h.
func NewClient(h Hub, c Credentials) *Client {
	return &Client{hub: h, creds: c, http: &http.Client{Timeout: 30 * time.Second}}
}

// Hub returns the client's hub.
func (c *Client) Hub() Hub { return c.hub }

// Credentials hands out the application password for the one consumer that
// must persist it (the workflow context yaml). Do not print the result.
func (c *Client) Credentials() (id, secret string) { return c.creds.id, c.creds.secret }

// RepositoryEndpoint is the executor-facing repository API the dev server's
// `repositoryEndpoint` points at.
func (c *Client) RepositoryEndpoint() string { return c.hub.URL + "/knime/rest/v4/repository" }

// StatusError is a non-2xx hub response. The body is kept short and never
// contains credentials (the hub does not echo them).
type StatusError struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *StatusError) Error() string {
	msg := fmt.Sprintf("hub %s %s: HTTP %d", e.Method, e.Path, e.Status)
	switch e.Status {
	case 401:
		msg += " (application password empty, wrong or revoked)"
	case 403:
		msg += " (no permission on that path)"
	}
	if e.Body != "" {
		msg += ": " + e.Body
	}
	return msg
}

// Identity is the account behind the application password.
type Identity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Identity calls GET /accounts/identity.
func (c *Client) Identity(ctx context.Context) (Identity, error) {
	var out Identity
	err := c.do(ctx, http.MethodGet, "/accounts/identity", nil, &out)
	return out, err
}

// Item is a repository item.
type Item struct {
	ID             string `json:"id"`
	Path           string `json:"path"`
	Type           string `json:"type"`
	LastUploadedOn string `json:"lastUploadedOn"`
}

// ItemByPath calls GET /repository/<path>?deep=false. A 404 yields ok=false.
func (c *Client) ItemByPath(ctx context.Context, path string) (Item, bool, error) {
	var out Item
	err := c.do(ctx, http.MethodGet, "/repository"+encodePath(path)+"?deep=false", nil, &out)
	var se *StatusError
	if errors.As(err, &se) && se.Status == http.StatusNotFound {
		return Item{}, false, nil
	}
	if err != nil {
		return Item{}, false, err
	}
	return out, true, nil
}

// CreateFolder creates a workflow group (PUT without body). An existing group
// is returned as is.
func (c *Client) CreateFolder(ctx context.Context, path string) (Item, error) {
	var out Item
	err := c.do(ctx, http.MethodPut, "/repository"+encodePath(path), nil, &out)
	return out, err
}

// CreateWorkflow creates an empty workflow at path.
func (c *Client) CreateWorkflow(ctx context.Context, path string) (Item, error) {
	var out Item
	body := map[string]string{"itemType": "Workflow"}
	err := c.do(ctx, http.MethodPut, "/repository"+encodePath(path), body, &out)
	return out, err
}

// FrontendURL is the interactive editor page of an item, in the shape the hub
// frontend uses: `<web>/app/<team-lowercased>/spaces/<rest of path>~<id>`.
func FrontendURL(web, itemPath, itemID string) string {
	segs := strings.Split(strings.TrimPrefix(itemPath, "/"), "/")
	if len(segs) < 3 || segs[0] != "Users" {
		return ""
	}
	team := url.PathEscape(strings.ToLower(segs[1]))
	rest := make([]string, 0, len(segs)-2)
	for _, s := range segs[2:] {
		rest = append(rest, url.PathEscape(s))
	}
	return fmt.Sprintf("%s/app/%s/spaces/%s~%s", strings.TrimRight(web, "/"), team,
		strings.Join(rest, "/"), strings.TrimPrefix(itemID, "*"))
}

// encodePath escapes every segment of a repository path and keeps the `/`.
func encodePath(path string) string {
	segs := strings.Split(path, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.hub.URL+path, reader)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.creds.id, c.creds.secret)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Prefer", "representation=minimal")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("hub %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Status: resp.StatusCode, Method: method, Path: path, Body: trim(string(data), 300)}
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

func trim(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
