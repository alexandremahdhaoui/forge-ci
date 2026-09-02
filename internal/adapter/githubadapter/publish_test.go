package githubadapter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Publishing a draft is one PATCH on the release's id with draft false:
// the last write of a release, after every asset is attached and every tag
// is on its remote.
func TestPublishReleasePatchesTheDraftById(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPatch, r.Method)
		require.Equal(t, "/repos/o/r/releases/42", r.URL.Path)

		var in map[string]any

		require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
		assert.Equal(t, false, in["draft"])

		fmt.Fprint(w, `{"id":42,"draft":false,"html_url":"http://releases/42"}`)
	}))
	defer srv.Close()

	release, err := githubadapter.New(nil, srv.URL, "pat").PublishRelease(context.Background(), "o/r", 42)
	require.NoError(t, err)
	assert.Equal(t, int64(42), release.ID)
	assert.False(t, release.Draft)
	assert.Equal(t, "http://releases/42", release.HTMLURL)
}

// A draft that is gone is an error a caller must see, never a silent
// nothing: the release it was going to publish does not exist.
func TestPublishReleaseFailsOnAMissingDraft(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := githubadapter.New(nil, srv.URL, "pat").PublishRelease(context.Background(), "o/r", 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publishing release 42")
}
