// Package githubadapter speaks the GitHub REST API for the github manager
// and the github compute engine. It is the only thing here that reaches
// api.github.com, and it holds no decision about what to provision or run.
//
// The base URL is always injectable so every test stays hermetic; the
// token is a bearer PAT handed in by the caller, never read from the
// environment here.
package githubadapter

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/crypto/nacl/box"
)

// API is what the realizer and the compute controller need from GitHub.
type API interface {
	// PublicKey answers the repo's Actions public key: its id and the
	// base64 key material a secret is sealed against.
	PublicKey(ctx context.Context, repo string) (keyID, keyB64 string, err error)
	// PutSecret writes one sealed Actions secret. The API is write-only:
	// a PUT is convergence, and GitHub never returns the value.
	PutSecret(ctx context.Context, repo, name, keyID, sealedB64 string) error
	// EnableWorkflow enables a workflow by file name.
	EnableWorkflow(ctx context.Context, repo, workflowFile string) error
	// Dispatch fires a workflow_dispatch. GitHub answers 204 with no run
	// id; correlation happens through ListRuns.
	Dispatch(ctx context.Context, repo, workflowFile, ref string, inputs map[string]string) error
	// ListRuns answers the workflow's dispatch runs created at or after
	// the given time, newest first.
	ListRuns(ctx context.Context, repo, workflowFile string, createdAfter time.Time) ([]RunInfo, error)
	// Run answers one run by id.
	Run(ctx context.Context, repo string, id int64) (RunInfo, error)
	// ReleaseByTag answers the release published under a tag, and whether
	// the repo carries one. Absent is not an error.
	ReleaseByTag(ctx context.Context, repo, tag string) (Release, bool, error)
	// CreateRelease publishes the release for a tag already on the remote.
	CreateRelease(ctx context.Context, repo, tag string) (Release, error)
	// UploadAsset attaches one file through the URL the release answered,
	// which is on a different host from the API base.
	UploadAsset(ctx context.Context, uploadURL, file string) error
	// OpenIssueByTitle answers the open issue with exactly this title, and
	// whether one is open at all.
	OpenIssueByTitle(ctx context.Context, repo, title string) (Issue, bool, error)
	// CreateIssue opens one issue.
	CreateIssue(ctx context.Context, repo, title, body string) (Issue, error)
}

// RunInfo is the slice of a workflow run the compute engine acts on.
type RunInfo struct {
	ID           int64
	DisplayTitle string
	Status       string
	Conclusion   string
	HTMLURL      string
}

// ErrNotFound marks a 404: the thing is not on the remote (yet).
var ErrNotFound = errors.New("not found on the remote")

// ErrInactive marks the other answer GitHub gives for a workflow file that
// is not on the default branch yet: 403 with "not active" in the body. It is
// the same condition as ErrNotFound and a different status, and the status
// alone cannot tell it apart from a real permission denial - a denied token
// is 403 too. So the body is the only discriminator there is.
var ErrInactive = errors.New("on the remote but not active yet")

// inactiveMarker is what GitHub says: "Unable to enable a workflow that is
// not active." Matching a message is brittle and it is the only signal the
// API gives, so a change in wording turns a tolerated state back into a hard
// failure rather than into a silent pass.
const inactiveMarker = "not active"

// Client is the HTTP implementation.
type Client struct {
	client *http.Client
	base   string
	token  string
}

var _ API = (*Client)(nil)

// New builds a client. A nil http.Client means http.DefaultClient; an
// empty base means the public API.
func New(client *http.Client, base, token string) *Client {
	if client == nil {
		client = http.DefaultClient
	}

	if base == "" {
		base = "https://api.github.com"
	}

	return &Client{client: client, base: base, token: token}
}

// Seal encrypts a secret value against a repo's Actions public key with
// an anonymous NaCl sealed box, the shape the secrets API demands.
func Seal(publicKeyB64, value string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return "", fmt.Errorf("decoding the repo public key: %w", err)
	}

	if len(raw) != 32 {
		return "", fmt.Errorf("decoding the repo public key: got %d bytes, want 32", len(raw))
	}

	var key [32]byte

	copy(key[:], raw)

	sealed, err := box.SealAnonymous(nil, []byte(value), &key, rand.Reader)
	if err != nil {
		return "", fmt.Errorf("sealing the secret: %w", err)
	}

	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (c *Client) PublicKey(ctx context.Context, repo string) (string, string, error) {
	var out struct {
		KeyID string `json:"key_id"`
		Key   string `json:"key"`
	}

	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/actions/secrets/public-key", repo), nil, &out)
	if err != nil {
		return "", "", fmt.Errorf("fetching the Actions public key of %q: %w", repo, err)
	}

	return out.KeyID, out.Key, nil
}

func (c *Client) PutSecret(ctx context.Context, repo, name, keyID, sealedB64 string) error {
	in := map[string]string{"encrypted_value": sealedB64, "key_id": keyID}

	err := c.do(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/actions/secrets/%s", repo, name), in, nil)
	if err != nil {
		return fmt.Errorf("putting secret %q on %q: %w", name, repo, err)
	}

	return nil
}

func (c *Client) EnableWorkflow(ctx context.Context, repo, workflowFile string) error {
	err := c.do(ctx, http.MethodPut,
		fmt.Sprintf("/repos/%s/actions/workflows/%s/enable", repo, workflowFile), nil, nil)
	if err != nil {
		return fmt.Errorf("enabling workflow %q on %q: %w", workflowFile, repo, err)
	}

	return nil
}

func (c *Client) Dispatch(
	ctx context.Context, repo, workflowFile, ref string, inputs map[string]string,
) error {
	if inputs == nil {
		inputs = map[string]string{}
	}

	in := map[string]any{"ref": ref, "inputs": inputs}

	err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/repos/%s/actions/workflows/%s/dispatches", repo, workflowFile), in, nil)
	if err != nil {
		return fmt.Errorf("dispatching workflow %q on %q: %w", workflowFile, repo, err)
	}

	return nil
}

func (c *Client) ListRuns(
	ctx context.Context, repo, workflowFile string, createdAfter time.Time,
) ([]RunInfo, error) {
	var out struct {
		WorkflowRuns []wireRun `json:"workflow_runs"`
	}

	query := url.Values{}
	query.Set("event", "workflow_dispatch")
	query.Set("created", ">="+createdAfter.UTC().Format(time.RFC3339))

	path := fmt.Sprintf("/repos/%s/actions/workflows/%s/runs?%s", repo, workflowFile, query.Encode())

	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, fmt.Errorf("listing runs of workflow %q on %q: %w", workflowFile, repo, err)
	}

	runs := make([]RunInfo, 0, len(out.WorkflowRuns))
	for _, r := range out.WorkflowRuns {
		runs = append(runs, r.info())
	}

	return runs, nil
}

func (c *Client) Run(ctx context.Context, repo string, id int64) (RunInfo, error) {
	var out wireRun

	err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/actions/runs/%d", repo, id), nil, &out)
	if err != nil {
		return RunInfo{}, fmt.Errorf("fetching run %d on %q: %w", id, repo, err)
	}

	return out.info(), nil
}

type wireRun struct {
	ID           int64  `json:"id"`
	DisplayTitle string `json:"display_title"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	HTMLURL      string `json:"html_url"`
}

func (w wireRun) info() RunInfo {
	return RunInfo(w)
}

// do sends one JSON request to a path on the API base and decodes the
// answer.
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var (
		body        []byte
		contentType string
	)

	if in != nil {
		payload, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encoding the request: %w", err)
		}

		body, contentType = payload, "application/json"
	}

	return c.send(ctx, method, c.base+path, contentType, body, out)
}

// send is do against a full URL rather than a path on the API base, because
// GitHub hands out absolute URLs of its own: a release's upload_url is on
// uploads.github.com, a different host from api.github.com. Joining one of
// those onto the base makes a URL that 404s, which reads like a missing
// release rather than like a bug in the caller.
//
// A 404 is ErrNotFound so a caller can treat "not there yet" apart from
// "broken"; any other non-2xx carries a body excerpt, which is where GitHub
// says why.
func (c *Client) send(
	ctx context.Context, method, target, contentType string, body []byte, out any,
) error {
	var reader io.Reader

	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("building the request: %w", err)
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)

	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("sending the request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		excerpt = bytes.TrimSpace(excerpt)

		if resp.StatusCode == http.StatusForbidden && bytes.Contains(excerpt, []byte(inactiveMarker)) {
			return fmt.Errorf("%w: %s", ErrInactive, excerpt)
		}

		return fmt.Errorf("status %d: %s", resp.StatusCode, excerpt)
	}

	if out == nil {
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding the answer: %w", err)
	}

	return nil
}
