package githubadapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Release is the slice of a GitHub release a caller acts on.
type Release struct {
	// ID is what a draft is published by: the tag is not yet a handle
	// while the release is a draft.
	ID      int64  `json:"id"`
	Draft   bool   `json:"draft"`
	HTMLURL string `json:"html_url"`
	// UploadURL is an RFC 6570 template on uploads.github.com, ending in
	// {?name,label}. It is a different host from the API base, which is why
	// the client can send to a full URL.
	UploadURL string `json:"upload_url"`
}

// ReleaseByTag answers the release published under a tag, and whether the
// repo carries one. Absent is not an error: it is what a first release of a
// tag looks like, and the caller publishes rather than failing.
func (c *Client) ReleaseByTag(ctx context.Context, repo, tag string) (Release, bool, error) {
	var out Release

	err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/repos/%s/releases/tags/%s", repo, url.PathEscape(tag)), nil, &out)
	if errors.Is(err, ErrNotFound) {
		return Release{}, false, nil
	}

	if err != nil {
		return Release{}, false, fmt.Errorf("reading release %q in %q: %w", tag, repo, err)
	}

	return out, true, nil
}

// CreateDraftRelease creates the release for a tag as a draft: invisible to
// consumers, assets attachable, and the tag itself not yet required to
// exist. It is the first write of a release, so a crash after it leaves a
// draft nobody can consume rather than a tag nobody can use.
func (c *Client) CreateDraftRelease(ctx context.Context, repo, tag string) (Release, error) {
	in := map[string]any{"tag_name": tag, "draft": true, "generate_release_notes": true}

	var out Release

	if err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/releases", in, &out); err != nil {
		return Release{}, fmt.Errorf("creating draft release %q in %q: %w", tag, repo, err)
	}

	return out, nil
}

// PublishRelease turns a draft into the release consumers see. It is the
// last write, after the assets are attached and the tags are on their
// remotes, so what appears appears whole.
func (c *Client) PublishRelease(ctx context.Context, repo string, id int64) (Release, error) {
	in := map[string]any{"draft": false}

	var out Release

	if err := c.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/releases/%d", repo, id), in, &out); err != nil {
		return Release{}, fmt.Errorf("publishing release %d in %q: %w", id, repo, err)
	}

	return out, nil
}

// UploadAsset attaches one file to a release through the upload URL the
// release itself answered.
func (c *Client) UploadAsset(ctx context.Context, uploadURL, file string) error {
	if i := strings.Index(uploadURL, "{"); i >= 0 {
		uploadURL = uploadURL[:i]
	}

	if uploadURL == "" {
		return errors.New("attaching an asset: the release answered no upload URL")
	}

	data, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return fmt.Errorf("attaching %s: %w", file, err)
	}

	target := uploadURL + "?name=" + url.QueryEscape(filepath.Base(file))

	if err := c.send(ctx, http.MethodPost, target, "application/octet-stream", data, nil); err != nil {
		return fmt.Errorf("attaching %s: %w", file, err)
	}

	return nil
}
