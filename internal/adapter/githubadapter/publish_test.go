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

func TestPublishReleaseIsOnePatchOnTheDraftsIdTurningDraftOff(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPatch, r.Method)
		require.Equal(t, "/repos/o/r/releases/42", r.URL.Path)

		var in map[string]any

		require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
		assert.Equal(t, false, in["draft"])

		_, _ = fmt.Fprint(w, `{"id":42,"draft":false,"html_url":"http://releases/42"}`)
	}))
	defer srv.Close()

	release, err := githubadapter.New(srv.Client(), srv.URL, "pat").PublishRelease(context.Background(), "o/r", 42)
	require.NoError(t, err)
	assert.Equal(t, int64(42), release.ID)
	assert.False(t, release.Draft)
	assert.Equal(t, "http://releases/42", release.HTMLURL)
}

func TestPublishReleaseOnADraftThatIsGoneIsAnErrorTheCallerSeesAndNeverASilentNothing(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := githubadapter.New(srv.Client(), srv.URL, "pat").PublishRelease(context.Background(), "o/r", 42)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publishing release 42")
}
