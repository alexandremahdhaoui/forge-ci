package githubadapter_test

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/nacl/box"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
)

func TestSealRoundTrips(t *testing.T) {
	t.Parallel()

	pub, priv, err := box.GenerateKey(rand.Reader)
	require.NoError(t, err)

	sealed, err := githubadapter.Seal(base64.StdEncoding.EncodeToString(pub[:]), "hunter2")
	require.NoError(t, err)

	raw, err := base64.StdEncoding.DecodeString(sealed)
	require.NoError(t, err)

	opened, ok := box.OpenAnonymous(nil, raw, pub, priv)
	require.True(t, ok, "the sealed box must open with the private key")
	assert.Equal(t, "hunter2", string(opened))
}

func TestSealRefusesABadKey(t *testing.T) {
	t.Parallel()

	_, err := githubadapter.Seal("not base64!!!", "v")
	require.Error(t, err)

	_, err = githubadapter.Seal(base64.StdEncoding.EncodeToString([]byte("short")), "v")
	require.ErrorContains(t, err, "want 32")
}

func TestPublicKeyAndPutSecret(t *testing.T) {
	t.Parallel()

	var gotPut struct {
		EncryptedValue string `json:"encrypted_value"`
		KeyID          string `json:"key_id"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/actions/secrets/public-key":
			assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"key_id":"k1","key":"cHVibGljLWtleS1iYXNlNjQ="}`))
		case "/repos/o/r/actions/secrets/NAME":
			assert.Equal(t, http.MethodPut, r.Method)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&gotPut))
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := githubadapter.New(nil, srv.URL, "tok")

	keyID, keyB64, err := c.PublicKey(t.Context(), "o/r")
	require.NoError(t, err)
	assert.Equal(t, "k1", keyID)
	assert.Equal(t, "cHVibGljLWtleS1iYXNlNjQ=", keyB64)

	require.NoError(t, c.PutSecret(t.Context(), "o/r", "NAME", keyID, "sealed=="))
	assert.Equal(t, "sealed==", gotPut.EncryptedValue)
	assert.Equal(t, "k1", gotPut.KeyID)
}

func TestEnableAndDispatch(t *testing.T) {
	t.Parallel()

	var dispatched map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/actions/workflows/ci.yaml/enable":
			assert.Equal(t, http.MethodPut, r.Method)
			w.WriteHeader(http.StatusNoContent)
		case "/repos/o/r/actions/workflows/runner.yaml/dispatches":
			assert.Equal(t, http.MethodPost, r.Method)
			require.NoError(t, json.NewDecoder(r.Body).Decode(&dispatched))
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := githubadapter.New(nil, srv.URL, "tok")

	require.NoError(t, c.EnableWorkflow(t.Context(), "o/r", "ci.yaml"))
	require.NoError(t, c.Dispatch(t.Context(), "o/r", "runner.yaml", "main",
		map[string]string{"marker": "m-1"}))

	assert.Equal(t, "main", dispatched["ref"])
	assert.Equal(t, map[string]any{"marker": "m-1"}, dispatched["inputs"])
}

func TestListRunsAndRun(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/actions/workflows/runner.yaml/runs":
			assert.Equal(t, "workflow_dispatch", r.URL.Query().Get("event"))
			assert.Contains(t, r.URL.Query().Get("created"), ">=")
			_, _ = w.Write([]byte(`{"workflow_runs":[
				{"id":7,"display_title":"run m-1","status":"completed","conclusion":"failure","html_url":"http://x/7"}]}`))
		case "/repos/o/r/actions/runs/7":
			_, _ = w.Write([]byte(`{"id":7,"display_title":"run m-1","status":"completed","conclusion":"success","html_url":"http://x/7"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := githubadapter.New(nil, srv.URL, "tok")

	runs, err := c.ListRuns(t.Context(), "o/r", "runner.yaml", time.Unix(0, 0))
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, int64(7), runs[0].ID)
	assert.Equal(t, "run m-1", runs[0].DisplayTitle)
	assert.Equal(t, "failure", runs[0].Conclusion)

	run, err := c.Run(t.Context(), "o/r", 7)
	require.NoError(t, err)
	assert.Equal(t, "success", run.Conclusion)
	assert.Equal(t, "http://x/7", run.HTMLURL)
}

func TestNotFoundAndErrors(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/actions/workflows/gone.yaml/enable":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by personal access token"}`))
		}
	}))
	defer srv.Close()

	c := githubadapter.New(nil, srv.URL, "tok")

	err := c.EnableWorkflow(t.Context(), "o/r", "gone.yaml")
	require.ErrorIs(t, err, githubadapter.ErrNotFound)

	err = c.PutSecret(t.Context(), "o/r", "S", "k", "v")
	require.ErrorContains(t, err, "status 403")
	require.ErrorContains(t, err, "not accessible")
}

// A workflow file written but not yet pushed to the default branch is 403
// "not active", not 404. That is the answer a first bootstrap actually gets,
// and reading it as a hard failure killed the run on its first repo.
func TestAnInactiveWorkflowIsItsOwnErrorAndNotAPermissionDenial(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)

		if r.URL.Path == "/repos/o/r/actions/workflows/fresh.yaml/enable" {
			_, _ = w.Write([]byte(`{"message":"Unable to enable a workflow that is not active."}`))

			return
		}

		_, _ = w.Write([]byte(`{"message":"Resource not accessible by personal access token"}`))
	}))
	defer srv.Close()

	c := githubadapter.New(nil, srv.URL, "tok")

	err := c.EnableWorkflow(t.Context(), "o/r", "fresh.yaml")
	require.ErrorIs(t, err, githubadapter.ErrInactive)
	require.ErrorContains(t, err, "not active")

	// The same status with a different body is a real denial and must stay
	// fatal. 403 alone cannot tell the two apart, so the body decides.
	err = c.EnableWorkflow(t.Context(), "o/r", "denied.yaml")
	require.NotErrorIs(t, err, githubadapter.ErrInactive)
	require.NotErrorIs(t, err, githubadapter.ErrNotFound)
	require.ErrorContains(t, err, "status 403")
}
