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
	"slices"
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
	releaseURI       = "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-artifact-release@v0.1.0"
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

	// releasedTag is the tag the last created release carried, and uploaded
	// is every asset name attached to it.
	releasedTag string
	uploaded    []string
	issues      []string
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
	case strings.HasSuffix(r.URL.Path, "/releases"):
		var in struct {
			TagName string `json:"tag_name"`
		}

		_ = json.NewDecoder(r.Body).Decode(&in)
		f.releasedTag = in.TagName

		// The real upload_url is on uploads.github.com, a different host
		// from the API base. One server is enough to prove the engine sends
		// where the RELEASE said rather than where the base is: the path is
		// one this handler would not otherwise serve.
		fmt.Fprintf(w, `{"id":1,"draft":true,"html_url":"http://fake/releases/1","upload_url":"http://%s/upload/assets{?name,label}"}`, r.Host)
	case strings.HasSuffix(r.URL.Path, "/releases/1") && r.Method == http.MethodPatch:
		// The publish of the draft: the last write of a release.
		fmt.Fprint(w, `{"id":1,"draft":false,"html_url":"http://fake/releases/1"}`)
	case strings.HasPrefix(r.URL.Path, "/upload/assets"):
		f.uploaded = append(f.uploaded, r.URL.Query().Get("name"))
		w.WriteHeader(http.StatusCreated)
	case strings.HasSuffix(r.URL.Path, "/issues") && r.Method == http.MethodGet:
		fmt.Fprint(w, `[]`)
	case strings.HasSuffix(r.URL.Path, "/issues"):
		var in struct {
			Title string `json:"title"`
		}

		_ = json.NewDecoder(r.Body).Decode(&in)
		f.issues = append(f.issues, in.Title)

		fmt.Fprint(w, `{"number":1,"html_url":"http://fake/issues/1"}`)
	case strings.HasSuffix(r.URL.Path, "/actions/secrets/public-key"):
		fmt.Fprintf(w, `{"key_id":"k1","key":"%s"}`, base64.StdEncoding.EncodeToString(f.pub[:]))
	case strings.Contains(r.URL.Path, "/actions/secrets/") && r.Method == http.MethodGet:
		// The existence read. GitHub answers metadata and never the value,
		// which is why existence is the only state a manager can compare
		// against, and 404 when the repo does not carry it.
		if _, ok := f.secrets[filepath.Base(r.URL.Path)]; !ok {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		fmt.Fprintf(w, `{"name":"%s"}`, filepath.Base(r.URL.Path))
	case strings.Contains(r.URL.Path, "/actions/secrets/"):
		var in struct {
			EncryptedValue string `json:"encrypted_value"`
		}

		_ = json.NewDecoder(r.Body).Decode(&in)
		f.secrets[filepath.Base(r.URL.Path)] = in.EncryptedValue
		w.WriteHeader(http.StatusCreated)
	case strings.Contains(r.URL.Path, "/actions/workflows/") &&
		strings.HasSuffix(r.URL.Path, ".yaml") && r.Method == http.MethodGet:
		// The state read that stops the enable running every time. An
		// enable of an already-enabled workflow succeeds, so without this a
		// reconcile would report a change forever and no run would ever
		// reach a stage.
		if !slices.Contains(f.enabled, filepath.Base(r.URL.Path)) {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		fmt.Fprint(w, `{"state":"active"}`)
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
					Kind: "actions-secret", Name: "o/r/FORGE_CI_GITHUB_TOKEN",
					Spec: map[string]any{"repo": "o/r", "secret": "FORGE_CI_GITHUB_TOKEN"},
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
	require.Equal(t, "pat-under-test", fake.open(t, fake.secrets["FORGE_CI_GITHUB_TOKEN"]),
		"the sealed secret must decrypt to the token the environment carried")
	require.Equal(t, []string{"intake.yaml"}, fake.enabled)

	// A bool is the field a handler drops without a compile error, and the
	// whole stop mechanism rides on this one. In process it passed while the
	// wire carried false; only this can tell.
	require.True(t, out.Changed, "nothing existed, so every kind converged something")
}

// dryRun and force are bools, so a handler that drops one compiles, passes
// every unit test, and fails only here. Dropping dryRun is a plan that
// writes, which is the worst failure this surface has.
func TestADryRunOverMCPWritesNothingAndStillSaysWhatWouldChange(t *testing.T) {
	fake, srv := newFakeGitHub(t, "success")
	t.Setenv("GITHUB_TOKEN", "pat-under-test")

	file := filepath.Join(t.TempDir(), ".github", "workflows", "intake.yaml")

	var out citypes.ReconcileOutput

	err := caller().Call(context.Background(), githubManagerURI, "reconcile",
		citypes.ReconcileInput{
			Manager: "github",
			DryRun:  true,
			Spec:    map[string]any{"apiBaseURL": srv.URL},
			Resources: []citypes.Resource{
				{Kind: "file-content", Name: file, Spec: map[string]any{"content": "on: push\n"}},
				{
					Kind: "actions-secret", Name: "o/r/FORGE_CI_GITHUB_TOKEN",
					Spec: map[string]any{"repo": "o/r", "secret": "FORGE_CI_GITHUB_TOKEN"},
				},
			},
		}, &out)
	require.NoError(t, err)

	require.True(t, out.Changed, "the plan says a real run would change something")
	require.NoFileExists(t, file, "and it wrote nothing")
	require.Empty(t, fake.secrets, "and sealed nothing")

	for _, action := range out.Actions {
		require.Contains(t, action, "would ",
			"every line of a plan must read as something that has not happened")
	}
}

// An existing secret is kept unless force says otherwise. This is the one
// collision git cannot arbitrate: the API never returns a value, so two
// operators with different credentials would leave one winner in silence.
func TestForceOverMCPIsWhatRewritesAnExistingSecret(t *testing.T) {
	fake, srv := newFakeGitHub(t, "success")

	secret := []citypes.Resource{{
		Kind: "actions-secret", Name: "o/r/FORGE_CI_GITHUB_TOKEN",
		Spec: map[string]any{"repo": "o/r", "secret": "FORGE_CI_GITHUB_TOKEN"},
	}}

	t.Setenv("GITHUB_TOKEN", "the-first-token")

	var first citypes.ReconcileOutput

	require.NoError(t, caller().Call(context.Background(), githubManagerURI, "reconcile",
		citypes.ReconcileInput{
			Manager: "github", Resources: secret,
			Spec: map[string]any{"apiBaseURL": srv.URL},
		}, &first))
	require.Equal(t, "the-first-token", fake.open(t, fake.secrets["FORGE_CI_GITHUB_TOKEN"]))

	t.Setenv("GITHUB_TOKEN", "a-second-operators-token")

	var second citypes.ReconcileOutput

	require.NoError(t, caller().Call(context.Background(), githubManagerURI, "reconcile",
		citypes.ReconcileInput{
			Manager: "github", Resources: secret, Owned: first.Owned,
			Spec: map[string]any{"apiBaseURL": srv.URL},
		}, &second))
	require.False(t, second.Changed)
	require.Equal(t, "the-first-token", fake.open(t, fake.secrets["FORGE_CI_GITHUB_TOKEN"]),
		"the second operator must not silently replace the first one's credential")

	var forced citypes.ReconcileOutput

	require.NoError(t, caller().Call(context.Background(), githubManagerURI, "reconcile",
		citypes.ReconcileInput{
			Manager: "github", Resources: secret, Owned: first.Owned, Force: true,
			Spec: map[string]any{"apiBaseURL": srv.URL},
		}, &forced))
	require.True(t, forced.Changed)
	require.Equal(t, "a-second-operators-token", fake.open(t, fake.secrets["FORGE_CI_GITHUB_TOKEN"]),
		"a rotation is asked for, and force is how")
}

// The second half, and the one that decides whether a pipeline ever runs
// again: over the same fake, with everything already as declared, the
// manager must answer changed false. A file it kept, a workflow already
// active and a secret that already exists are all no-ops, and each of the
// three has a shape that succeeds while changing nothing.
func TestTheGitHubManagerReportsNoChangeWhenEverythingAlreadyMatchesOverMCP(t *testing.T) {
	_, srv := newFakeGitHub(t, "success")
	t.Setenv("GITHUB_TOKEN", "pat-under-test")

	file := filepath.Join(t.TempDir(), ".github", "workflows", "intake.yaml")

	resources := []citypes.Resource{
		{Kind: "file-content", Name: file, Spec: map[string]any{"content": "on: push\n"}},
		{
			Kind: "actions-secret", Name: "o/r/FORGE_CI_GITHUB_TOKEN",
			Spec: map[string]any{"repo": "o/r", "secret": "FORGE_CI_GITHUB_TOKEN"},
		},
		{
			Kind: "workflow-enabled", Name: "o/r/intake.yaml",
			Spec: map[string]any{"repo": "o/r", "workflow": "intake.yaml"},
		},
	}

	in := citypes.ReconcileInput{
		Manager:   "github",
		Spec:      map[string]any{"apiBaseURL": srv.URL},
		Resources: resources,
	}

	var first citypes.ReconcileOutput

	require.NoError(t, caller().Call(context.Background(), githubManagerURI, "reconcile", in, &first))
	require.True(t, first.Changed)

	in.Owned = first.Owned

	var second citypes.ReconcileOutput

	require.NoError(t, caller().Call(context.Background(), githubManagerURI, "reconcile", in, &second))
	require.False(t, second.Changed,
		"a second reconcile over converged state must let the run reach its stages")
}

func TestTheGitHubComputeDeclaresItsSurfaceOverMCP(t *testing.T) {
	var out citypes.DeclareOutput

	err := caller().Call(context.Background(), githubComputeURI, "declare",
		map[string]any{"spec": map[string]any{
			"repo": "o/r",
			"workspace": map[string]any{
				"bootstrapCommand": "true", "toolchainScript": "true\n",
			},
			"secrets":   []any{map[string]any{"name": "FORGE_CI_GITHUB_TOKEN"}},
			"workflows": []any{map[string]any{"name": "release", "kind": "release", "secret": "FORGE_CI_GITHUB_TOKEN"}},
			"runner":    map[string]any{"name": "ci-runner", "secret": "FORGE_CI_GITHUB_TOKEN"},
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
		"actions-secret/o/r/FORGE_CI_GITHUB_TOKEN",
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

// The release engine over real MCP against a real HTTP server.
//
// The wire is what this proves. spec.repo is new, and a key the core sends
// and the engine drops is invisible to every unit test on either side: that
// is exactly how the version field went missing and shipped a release of
// mis-stamped binaries. It also proves the asset goes to the host the
// RELEASE named rather than the API base.
func TestTheReleaseEngineCreatesAReleaseOverMCP(t *testing.T) {
	fake, srv := newFakeGitHub(t, "success")
	t.Setenv("GITHUB_TOKEN", "pat-under-test")

	var out citypes.ArtifactOutput

	err := caller().Call(context.Background(), releaseURI, "publish",
		citypes.ArtifactInput{
			Revision: "abc123",
			Version:  "v0.2.0",
			// No repos: tagging is git in a checkout and has its own tests.
			// What is under test here is the half that crosses the network.
			Repos: map[string]string{},
			Spec: map[string]any{
				"repo":       "o/r",
				"apiBaseURL": srv.URL,
				"root":       t.TempDir(),
			},
		}, &out)
	require.NoError(t, err)

	require.True(t, out.Published)
	require.Equal(t, "http://fake/releases/1", out.URL)
	require.Equal(t, "v0.2.0", fake.releasedTag)

	// index.json is always published: it maps the revision to the measured
	// digest of every asset, and forge-factory sync consumes it.
	require.Contains(t, fake.uploaded, "index.json")
}

// A release with no repo named fails rather than guessing one off a
// checkout's remote. Every other engine here is told what it acts on.
func TestTheReleaseEngineRefusesWithoutARepo(t *testing.T) {
	_, srv := newFakeGitHub(t, "success")
	t.Setenv("GITHUB_TOKEN", "pat-under-test")

	var out citypes.ArtifactOutput

	err := caller().Call(context.Background(), releaseURI, "publish",
		citypes.ArtifactInput{
			Revision: "abc123", Version: "v0.2.0", Repos: map[string]string{},
			Spec: map[string]any{"apiBaseURL": srv.URL, "root": t.TempDir()},
		}, &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "spec.repo")
}
