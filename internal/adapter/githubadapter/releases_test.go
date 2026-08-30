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
	"strings"
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

		fmt.Fprintf(w, `{"html_url":"http://releases/1","upload_url":"%s/assets{?name,label}"}`, uploads.URL)
	}))
	defer api.Close()

	client := githubadapter.New(nil, api.URL, "pat")

	release, err := client.CreateRelease(context.Background(), "o/r", "v0.2.0")
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

// The dedupe matches an exact title against the open issues rather than
// asking the search index, which is asynchronous: two runs seconds apart can
// both see nothing there and both file.
func TestOpenIssueByTitleMatchesExactlyAndListsRatherThanSearches(t *testing.T) {
	t.Parallel()

	var gotQuery string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery

		fmt.Fprint(w, `[{"number":1,"title":"other is failing","html_url":"http://issues/1"},
		                {"number":2,"title":"intake is failing","html_url":"http://issues/2"}]`)
	}))
	defer srv.Close()

	client := githubadapter.New(nil, srv.URL, "pat")

	issue, found, err := client.OpenIssueByTitle(context.Background(), "o/r", "intake is failing")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 2, issue.Number)
	assert.Contains(t, gotQuery, "state=open")
	assert.NotContains(t, gotQuery, "search")

	// A title that merely looks similar is a different failure.
	_, found, err = client.OpenIssueByTitle(context.Background(), "o/r", "intake is fail")
	require.NoError(t, err)
	assert.False(t, found)
}

func TestCreateIssueSendsTitleAndBody(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]any

		require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
		assert.Equal(t, "intake is failing", in["title"])
		assert.Equal(t, "the body", in["body"])
		assert.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "))

		fmt.Fprint(w, `{"number":9,"html_url":"http://issues/9"}`)
	}))
	defer srv.Close()

	issue, err := githubadapter.New(nil, srv.URL, "pat").
		CreateIssue(context.Background(), "o/r", "intake is failing", "the body")
	require.NoError(t, err)
	assert.Equal(t, 9, issue.Number)
	assert.Equal(t, "http://issues/9", issue.HTMLURL)
}
