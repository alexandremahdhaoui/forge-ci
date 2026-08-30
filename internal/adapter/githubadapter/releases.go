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

// CreateRelease publishes the release for a tag that already exists on the
// remote, with notes GitHub generates from the commits since the previous
// tag.
func (c *Client) CreateRelease(ctx context.Context, repo, tag string) (Release, error) {
	in := map[string]any{"tag_name": tag, "generate_release_notes": true}

	var out Release

	if err := c.do(ctx, http.MethodPost, "/repos/"+repo+"/releases", in, &out); err != nil {
		return Release{}, fmt.Errorf("creating release %q in %q: %w", tag, repo, err)
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
