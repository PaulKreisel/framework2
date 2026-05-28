package handover

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultJiraBaseURL mirrors the value used by the jiracli package. Kept
// local so the handover package does not depend on jiracli (which would
// pull in cobra-of-bd / GitHub deps it does not need).
const DefaultJiraBaseURL = "https://knime-com.atlassian.net"

const jiraHTTPTimeout = 60 * time.Second

// JiraOptions pins values that production reads from the environment. Tests
// use it to drive a fake Jira server.
type JiraOptions struct {
	// BaseURL overrides DefaultJiraBaseURL.
	BaseURL string
	// HTTPClient overrides http.DefaultClient (with jiraHTTPTimeout applied
	// when nil).
	HTTPClient *http.Client
	// Email + Token override the env-var auth lookup. Empty values fall back
	// to KNIME_ATLASSIAN_EMAIL / KNIME_ATLASSIAN_API_TOKEN.
	Email, Token string
}

// attachment is one file to upload. Body is consumed once.
type attachment struct {
	filename string
	body     io.Reader
}

// uploadAttachments POSTs every entry in files as multipart/form-data to
// /rest/api/3/issue/<jiraKey>/attachments. The Jira REST API accepts multiple
// `file` parts in one request and creates one attachment per part.
func uploadAttachments(ctx context.Context, opts JiraOptions, jiraKey string, files []attachment) error {
	email, token, err := jiraCredentials(opts)
	if err != nil {
		return err
	}
	base := opts.BaseURL
	if base == "" {
		base = DefaultJiraBaseURL
	}
	url := fmt.Sprintf("%s/rest/api/3/issue/%s/attachments", strings.TrimRight(base, "/"), jiraKey)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, f := range files {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition",
			fmt.Sprintf(`form-data; name="file"; filename=%q`, f.filename))
		h.Set("Content-Type", "application/octet-stream")
		part, err := mw.CreatePart(h)
		if err != nil {
			return fmt.Errorf("multipart create part %q: %w", f.filename, err)
		}
		if _, err := io.Copy(part, f.body); err != nil {
			return fmt.Errorf("multipart copy %q: %w", f.filename, err)
		}
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("multipart close: %w", err)
	}

	cctx, cancel := context.WithTimeout(ctx, jiraHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, url, &body)
	if err != nil {
		return fmt.Errorf("build attach request: %w", err)
	}
	req.SetBasicAuth(email, token)
	req.Header.Set("X-Atlassian-Token", "no-check")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", mw.FormDataContentType())

	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: jiraHTTPTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("attach to %s: %w", jiraKey, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("attach to %s: status %d: %s", jiraKey, resp.StatusCode, strings.TrimSpace(string(preview)))
	}
	return nil
}

func jiraCredentials(opts JiraOptions) (email, token string, err error) {
	email = opts.Email
	if email == "" {
		email = os.Getenv("KNIME_ATLASSIAN_EMAIL")
	}
	token = opts.Token
	if token == "" {
		token = os.Getenv("KNIME_ATLASSIAN_API_TOKEN")
	}
	if email == "" || token == "" {
		return "", "", errors.New("KNIME_ATLASSIAN_EMAIL and KNIME_ATLASSIAN_API_TOKEN must be set")
	}
	return email, token, nil
}

// buildAttachments opens the JSONL file and the embedded asset files for
// streaming into uploadAttachments. The JSONL handle is the caller's
// responsibility to close (via the closer returned).
func buildAttachments(jsonlPath string) ([]attachment, func(), error) {
	jsonl, err := os.Open(jsonlPath)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open handover jsonl: %w", err)
	}
	files := []attachment{
		{filename: filepath.Base(jsonlPath), body: jsonl},
	}
	for _, name := range AssetNames {
		data, err := Assets.ReadFile("assets/" + name)
		if err != nil {
			jsonl.Close()
			return nil, func() {}, fmt.Errorf("read embedded asset %s: %w", name, err)
		}
		files = append(files, attachment{filename: name, body: bytes.NewReader(data)})
	}
	return files, func() { jsonl.Close() }, nil
}
