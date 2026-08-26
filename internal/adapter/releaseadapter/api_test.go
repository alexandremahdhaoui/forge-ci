package releaseadapter_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/releaseadapter"
)

// fakeGitHub answers the two API calls a release needs and records what it
// was sent.
type fakeGitHub struct {
	server   *httptest.Server
	created  map[string]string // repo -> tag
	uploaded map[string][]byte // asset name -> bytes
	auth     string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()

	f := &fakeGitHub{created: map[string]string{}, uploaded: map[string][]byte{}}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/{owner}/{repo}/releases", func(w http.ResponseWriter, r *http.Request) {
		f.auth = r.Header.Get("Authorization")

		var in struct {
			TagName string `json:"tag_name"`
		}

		require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
		f.created[r.PathValue("owner")+"/"+r.PathValue("repo")] = in.TagName

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"html_url":   f.server.URL + "/releases/" + in.TagName,
			"upload_url": f.server.URL + "/uploads{?name,label}",
		})
	})
	mux.HandleFunc("POST /uploads", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		f.uploaded[r.URL.Query().Get("name")] = data
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)

	return f
}

// gitRepo builds a repo whose origin names the owner/repo the API path
// needs; the remote itself is never pushed to.
func gitRepo(t *testing.T, remote string) string {
	t.Helper()

	dir := t.TempDir()
	runner := execadapter.New()

	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"remote", "add", "origin", remote},
	} {
		res, err := runner.Run(context.Background(), dir, "git", args...)
		require.NoError(t, err)
		require.Zero(t, res.ExitCode, res.Stderr)
	}

	return dir
}

func TestTheAPIPublisherCreatesTheReleaseAndUploads(t *testing.T) {
	t.Parallel()

	fake := newFakeGitHub(t)
	dir := gitRepo(t, "git@github.com:owner/home.git")

	asset := filepath.Join(t.TempDir(), "tool_linux_amd64")
	require.NoError(t, os.WriteFile(asset, []byte("binary-bytes"), 0o600))

	api := releaseadapter.NewAPI(execadapter.New(), fake.server.URL, "token-x")

	url, err := api.Release(context.Background(), dir, "dist-abc123def456", []string{asset})
	require.NoError(t, err)

	assert.Contains(t, url, "/releases/dist-abc123def456")
	assert.Equal(t, "dist-abc123def456", fake.created["owner/home"])
	assert.Equal(t, "binary-bytes", string(fake.uploaded["tool_linux_amd64"]))
	assert.Equal(t, "Bearer token-x", fake.auth)
}

func TestTheAPIPublisherFailsLoudOnAPIErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Validation Failed"}`, http.StatusUnprocessableEntity)
	}))
	t.Cleanup(server.Close)

	dir := gitRepo(t, "https://github.com/owner/home")

	api := releaseadapter.NewAPI(execadapter.New(), server.URL, "")

	_, err := api.Release(context.Background(), dir, "dist-x", nil)
	require.ErrorContains(t, err, "Validation Failed")
}

func TestARemoteWithoutARepoFailsLoud(t *testing.T) {
	t.Parallel()

	dir := gitRepo(t, "not-a-remote")

	api := releaseadapter.NewAPI(execadapter.New(), "http://127.0.0.1:1", "")

	_, err := api.Release(context.Background(), dir, "dist-x", nil)
	require.ErrorContains(t, err, "names no owner/repo")
}

func TestDigestFileMeasuresWhatIsThere(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "tool")
	require.NoError(t, os.WriteFile(path, []byte("content"), 0o600))

	digest, size, err := releaseadapter.DigestFile(path)
	require.NoError(t, err)

	sum := sha256.Sum256([]byte("content"))
	assert.Equal(t, hex.EncodeToString(sum[:]), digest)
	assert.Equal(t, int64(len("content")), size)

	_, _, err = releaseadapter.DigestFile(filepath.Join(t.TempDir(), "missing"))
	require.ErrorContains(t, err, "digesting")
}
