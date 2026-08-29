//go:build integration

package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/nacl/box"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	githubManagerURI = "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-github@v0.1.0"
	githubComputeURI = "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-compute-github@v0.1.0"
)

// fakeGitHub is enough of the API for the manager and the compute engine:
// a repo key, write-only secrets, workflow enablement, dispatch and runs.
type fakeGitHub struct {
	mu       sync.Mutex
	pub      *[32]byte
	priv     *[32]byte
	secrets  map[string]string
	enabled  []string
	marker   string
	conclude string
}

func newFakeGitHub(t *testing.T, conclude string) (*fakeGitHub, *httptest.Server) {
	t.Helper()

	pub, priv, err := box.GenerateKey(rand.Reader)
	require.NoError(t, err)

	f := &fakeGitHub{pub: pub, priv: priv, secrets: map[string]string{}, conclude: conclude}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)

	return f, srv
}

func (f *fakeGitHub) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case strings.HasSuffix(r.URL.Path, "/actions/secrets/public-key"):
		fmt.Fprintf(w, `{"key_id":"k1","key":"%s"}`, base64.StdEncoding.EncodeToString(f.pub[:]))
	case strings.Contains(r.URL.Path, "/actions/secrets/"):
		var in struct {
			EncryptedValue string `json:"encrypted_value"`
		}

		_ = json.NewDecoder(r.Body).Decode(&in)
		f.secrets[filepath.Base(r.URL.Path)] = in.EncryptedValue
		w.WriteHeader(http.StatusCreated)
	case strings.HasSuffix(r.URL.Path, "/enable"):
		parts := strings.Split(r.URL.Path, "/")
		f.enabled = append(f.enabled, parts[len(parts)-2])
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(r.URL.Path, "/dispatches"):
		var in struct {
			Inputs map[string]string `json:"inputs"`
		}

		_ = json.NewDecoder(r.Body).Decode(&in)
		f.marker = in.Inputs["marker"]
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(r.URL.Path, "/runs"):
		fmt.Fprintf(w, `{"workflow_runs":[{"id":11,"display_title":"run %s","status":"completed","conclusion":"%s","html_url":"http://fake/11"}]}`,
			f.marker, f.conclude)
	case strings.Contains(r.URL.Path, "/actions/runs/"):
		fmt.Fprintf(w, `{"id":11,"display_title":"run %s","status":"completed","conclusion":"%s","html_url":"http://fake/11"}`,
			f.marker, f.conclude)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeGitHub) open(t *testing.T, sealedB64 string) string {
	t.Helper()

	raw, err := base64.StdEncoding.DecodeString(sealedB64)
	require.NoError(t, err)

	opened, ok := box.OpenAnonymous(nil, raw, f.pub, f.priv)
	require.True(t, ok)

	return string(opened)
}

func TestTheGitHubManagerRealizesEveryKindOverMCP(t *testing.T) {
	fake, srv := newFakeGitHub(t, "success")
	t.Setenv("GITHUB_TOKEN", "pat-under-test")

	file := filepath.Join(t.TempDir(), ".github", "workflows", "intake.yaml")

	var out citypes.ReconcileOutput

	err := caller().Call(context.Background(), githubManagerURI, "reconcile",
		citypes.ReconcileInput{
			Manager: "github",
			Spec:    map[string]any{"apiBaseURL": srv.URL},
			Resources: []citypes.Resource{
				{Kind: "file-content", Name: file, Spec: map[string]any{"content": "on: push\n"}},
				{
					Kind: "actions-secret", Name: "o/r/WORKSPACE_TOKEN",
					Spec: map[string]any{"repo": "o/r", "secret": "WORKSPACE_TOKEN"},
				},
				{
					Kind: "workflow-enabled", Name: "o/r/intake.yaml",
					Spec: map[string]any{"repo": "o/r", "workflow": "intake.yaml"},
				},
			},
		}, &out)
	require.NoError(t, err)

	require.FileExists(t, file)
	require.Len(t, out.Owned, 3)
	require.Equal(t, "pat-under-test", fake.open(t, fake.secrets["WORKSPACE_TOKEN"]),
		"the sealed secret must decrypt to the token the environment carried")
	require.Equal(t, []string{"intake.yaml"}, fake.enabled)
}

func TestTheGitHubComputeDeclaresItsSurfaceOverMCP(t *testing.T) {
	var out citypes.DeclareOutput

	err := caller().Call(context.Background(), githubComputeURI, "declare",
		map[string]any{"spec": map[string]any{
			"repo": "o/r",
			"workspace": map[string]any{
				"bootstrapCommand": "true", "toolchainScript": "true\n",
			},
			"secrets":   []any{map[string]any{"name": "WORKSPACE_TOKEN"}},
			"workflows": []any{map[string]any{"name": "release", "kind": "release"}},
			"runner":    map[string]any{"name": "ci-runner", "secret": "WORKSPACE_TOKEN"},
		}}, &out)
	require.NoError(t, err)

	ids := make([]string, 0, len(out.Resources))
	for _, r := range out.Resources {
		ids = append(ids, r.ID())
		require.NotNilf(t, r.Spec, "%s crossed the wire with a null spec", r.ID())
	}

	require.Equal(t, []string{
		"file-content/r/.github/workflows/release.yaml",
		"file-content/r/.github/workflows/ci-runner.yaml",
		"actions-secret/o/r/WORKSPACE_TOKEN",
		"workflow-enabled/o/r/release.yaml",
		"workflow-enabled/o/r/ci-runner.yaml",
	}, ids)
}

func TestTheGitHubComputeReportsARedRunAsFailedOverMCP(t *testing.T) {
	_, srv := newFakeGitHub(t, "failure")
	t.Setenv("GITHUB_TOKEN", "pat-under-test")

	var out citypes.RunOutput

	err := caller().Call(context.Background(), githubComputeURI, "run",
		citypes.RunInput{
			Revision: "0123456789abcdef",
			Stage:    "process",
			Substage: "default",
			Targets:  []citypes.Target{{Alias: "process", Forge: "test run process", In: []string{"r"}}},
			Spec: map[string]any{
				"repo":       "o/r",
				"apiBaseURL": srv.URL,
				"workspace": map[string]any{
					"bootstrapCommand": "true", "toolchainScript": "true\n",
				},
				"runner": map[string]any{"name": "ci-runner", "pollIntervalSeconds": 1},
			},
		}, &out)
	require.NoError(t, err, "a red conclusion is a failed run, never a tool error")
	require.Equal(t, citypes.StatusFailed, out.Status)
	require.Contains(t, out.Message, "concluded failure")
}
