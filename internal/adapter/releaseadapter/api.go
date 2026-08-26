package releaseadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
)

// API publishes through the GitHub REST API directly, for hosts that carry
// no gh binary - a CI runner, a container, an airgapped mirror of the API.
// Tagging stays plain git; only the release itself needs the API.
type API struct {
	*GH

	client *http.Client
	base   string
	token  string
}

var _ Publisher = (*API)(nil)

// NewAPI builds the API publisher. base defaults to https://api.github.com;
// an empty token sends no authorization, which only a fake accepts.
func NewAPI(runner execadapter.Runner, base, token string) *API {
	if base == "" {
		base = "https://api.github.com"
	}

	return &API{
		GH:     New(runner),
		client: &http.Client{Timeout: 5 * time.Minute},
		base:   strings.TrimSuffix(base, "/"),
		token:  token,
	}
}

type wireRelease struct {
	HTMLURL   string `json:"html_url"`
	UploadURL string `json:"upload_url"`
}

// Release creates the release named by tag in dir's origin repo and uploads
// the files as assets. The repo is read off the checkout's own remote, so
// nothing here names a project.
func (a *API) Release(ctx context.Context, dir, tag string, files []string) (string, error) {
	remote, err := a.run(ctx, dir, "reading the origin remote", "git", "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}

	repo, err := parseRepo(remote)
	if err != nil {
		return "", err
	}

	payload, err := json.Marshal(map[string]any{
		"tag_name":               tag,
		"generate_release_notes": true,
	})
	if err != nil {
		return "", fmt.Errorf("encoding release %s: %w", tag, err)
	}

	var release wireRelease

	err = a.do(ctx, http.MethodPost, a.base+"/repos/"+repo+"/releases",
		"application/json", payload, &release)
	if err != nil {
		return "", fmt.Errorf("creating release %s in %s: %w", tag, repo, err)
	}

	for _, file := range files {
		if err := a.upload(ctx, release.UploadURL, file); err != nil {
			return "", fmt.Errorf("attaching %s to release %s: %w", file, tag, err)
		}
	}

	return release.HTMLURL, nil
}

// upload attaches one file to the release through its upload URL, which
// arrives as an RFC 6570 template ending in {?name,label}.
func (a *API) upload(ctx context.Context, uploadURL, file string) error {
	if i := strings.Index(uploadURL, "{"); i >= 0 {
		uploadURL = uploadURL[:i]
	}

	if uploadURL == "" {
		return fmt.Errorf("the release answered no upload URL")
	}

	data, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return err
	}

	target := uploadURL + "?name=" + url.QueryEscape(filepath.Base(file))

	return a.do(ctx, http.MethodPost, target, "application/octet-stream", data, nil)
}

func (a *API) do(ctx context.Context, method, target, contentType string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", contentType)

	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		excerpt := strings.TrimSpace(string(raw))
		if len(excerpt) > 300 {
			excerpt = excerpt[:300]
		}

		return fmt.Errorf("%s %s: %s: %s", method, target, resp.Status, excerpt)
	}

	if out == nil {
		return nil
	}

	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decoding %s %s: %w", method, target, err)
	}

	return nil
}

// parseRepo answers "owner/name" from a git remote URL in its ssh scp-like,
// ssh URL, or http(s) form.
func parseRepo(remote string) (string, error) {
	remote = strings.TrimSpace(remote)
	trimmed := strings.TrimSuffix(remote, ".git")

	if at := strings.Index(trimmed, "@"); at >= 0 && !strings.Contains(trimmed, "://") {
		// scp-like: git@host:owner/repo
		if colon := strings.Index(trimmed, ":"); colon > at {
			return trimmed[colon+1:], nil
		}
	}

	if u, err := url.Parse(trimmed); err == nil && u.Host != "" {
		path := strings.Trim(u.Path, "/")
		if strings.Count(path, "/") >= 1 {
			parts := strings.Split(path, "/")

			return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
		}
	}

	// A filesystem remote - a local bare mirror, the hermetic e2e's origin -
	// still names owner/repo in its last two segments.
	if strings.HasPrefix(trimmed, "/") && strings.Count(trimmed, "/") >= 2 {
		parts := strings.Split(trimmed, "/")

		return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
	}

	return "", fmt.Errorf("remote %q names no owner/repo", remote)
}
