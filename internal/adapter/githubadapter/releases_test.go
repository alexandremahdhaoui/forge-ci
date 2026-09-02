package githubadapter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A release's upload_url is on a DIFFERENT host from the API base. This test
// serves the two from two servers on purpose: joining the upload path onto
// the base gives a URL that 404s, which reads like a missing release rather
// than like a bug in the client, and only two hosts can catch it.
func TestItUploadsToTheHostTheReleaseNames(t *testing.T) {
	t.Parallel()

	var (
		gotPath string
		gotBody []byte
		gotType string
	)

	uploads := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		gotType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)

		w.WriteHeader(http.StatusCreated)
	}))
	defer uploads.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/repos/o/r/releases", r.URL.Path)

		var in map[string]any

		require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
		assert.Equal(t, "v0.2.0", in["tag_name"])
		assert.Equal(t, true, in["generate_release_notes"])
		assert.Equal(t, true, in["draft"], "the first write of a release is a draft nobody can consume")

		fmt.Fprintf(w, `{"html_url":"http://releases/1","upload_url":"%s/assets{?name,label}"}`, uploads.URL)
	}))
	defer api.Close()

	client := githubadapter.New(nil, api.URL, "pat")

	release, err := client.CreateDraftRelease(context.Background(), "o/r", "v0.2.0")
	require.NoError(t, err)
	require.Equal(t, "http://releases/1", release.HTMLURL)

	file := filepath.Join(t.TempDir(), "a-tool_linux_amd64")
	require.NoError(t, os.WriteFile(file, []byte("payload"), 0o600))

	require.NoError(t, client.UploadAsset(context.Background(), release.UploadURL, file))

	// The RFC 6570 template is trimmed at the brace and the name is the file's
	// own, or GitHub attaches an asset nobody can find by name.
	assert.Equal(t, "/assets?name=a-tool_linux_amd64", gotPath)
	assert.Equal(t, "application/octet-stream", gotType)
	assert.Equal(t, "payload", string(gotBody))
}

func TestUploadRefusesAReleaseThatAnsweredNoURL(t *testing.T) {
	t.Parallel()

	err := githubadapter.New(nil, "http://unused", "pat").
		UploadAsset(context.Background(), "", "unused")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no upload URL")
}

// A tag with no release is the ordinary case on a first publish, so it is
// found=false and not an error. Anything else and the release workflow would
// refuse to do the job it exists for.
func TestReleaseByTagAnswersAbsentRatherThanFailing(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, found, err := githubadapter.New(nil, srv.URL, "pat").
		ReleaseByTag(context.Background(), "o/r", "v0.2.0")
	require.NoError(t, err)
	assert.False(t, found)
}
