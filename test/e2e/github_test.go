//go:build e2e

package e2e_test

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/nacl/box"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

type ghFake struct {
	mu      sync.Mutex
	keyB64  string
	secrets []string
	enabled []string
	marker  string
}

func newGHFake(t *testing.T) (*ghFake, *httptest.Server) {
	t.Helper()

	pub, _, err := box.GenerateKey(rand.Reader)
	require.NoError(t, err)

	f := &ghFake{keyB64: base64.StdEncoding.EncodeToString(pub[:])}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)

	return f, srv
}

func (f *ghFake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case strings.HasSuffix(r.URL.Path, "/actions/secrets/public-key"):
		fmt.Fprintf(w, `{"key_id":"k1","key":"%s"}`, f.keyB64)
	case strings.Contains(r.URL.Path, "/actions/secrets/") && r.Method == http.MethodGet:
		if !slices.Contains(f.secrets, filepath.Base(r.URL.Path)) {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		fmt.Fprintf(w, `{"name":"%s"}`, filepath.Base(r.URL.Path))
	case strings.Contains(r.URL.Path, "/actions/secrets/"):
		f.secrets = append(f.secrets, filepath.Base(r.URL.Path))
		w.WriteHeader(http.StatusCreated)
	case strings.Contains(r.URL.Path, "/actions/workflows/") &&
		strings.HasSuffix(r.URL.Path, ".yaml") && r.Method == http.MethodGet:
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
		fmt.Fprintf(w, `{"workflow_runs":[{"id":5,"display_title":"run %s","status":"completed","conclusion":"success","html_url":"http://fake/5"}]}`, f.marker)
	case strings.Contains(r.URL.Path, "/actions/runs/"):
		fmt.Fprintf(w, `{"id":5,"display_title":"run %s","status":"completed","conclusion":"success","html_url":"http://fake/5"}`, f.marker)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func githubPipelineYAML(root, statePath, fakeURL string) string {
	return `name: demo
repos:
  - name: demo-repo
    url: file://` + filepath.Join(root, "demo-repo") + `
managers:
  - alias: local
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-local@v0.1.0"
    spec:
      statePath: ` + filepath.Join(root, "manager-local.json") + `
  - alias: github
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-github@v0.1.0"
    spec:
      statePath: ` + filepath.Join(root, "manager-github.json") + `
      apiBaseURL: ` + fakeURL + `
engines:
  - alias: here
    type: compute
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-compute-local@v0.1.0"
    manager: local
  - alias: gh-actions
    type: compute
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-compute-github@v0.1.0"
    manager: github
    spec:
      repo: acme/demo-repo
      dir: demo-repo
      apiBaseURL: ` + fakeURL + `
      workspace:
        bootstrapCommand: "true"
        toolchainScript: "true"
      secrets:
        - name: FORGE_CI_GITHUB_TOKEN
      workflows:
        - name: release
          kind: release
          secret: FORGE_CI_GITHUB_TOKEN
      runner:
        name: ci-runner
        secret: FORGE_CI_GITHUB_TOKEN
        pollIntervalSeconds: 1
  - alias: ci-state
    type: state
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-state-git@v0.1.0"
    manager: local
    spec:
      path: ` + statePath + `
  - alias: on-change
    type: trigger
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-trigger-watch@v0.1.0"
    manager: github
    spec:
      watch: ["` + filepath.Join(root, "demo-repo") + `"]
      notify:
        owner: acme
        factory: demo-factory
        eventType: member-pushed
        secret: FORGE_CI_DISPATCH_TOKEN
  - alias: all-pass
    type: promotion
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-promotion-all@v0.1.0"
    manager: local
state: ci-state
triggers: [on-change]
targets:
  - alias: build-all
    binary: forge
    args: [test-all]
    in: [demo-repo]
stages:
  - name: remote
    promotion: all-pass
    substages:
      - name: default
        engine: gh-actions
        targets: [build-all]
`
}

func TestTheGitHubSurfaceIsProvisionedAndConverged(t *testing.T) {
	fake, srv := newGHFake(t)
	t.Setenv("GITHUB_TOKEN", "pat-under-test")
	t.Setenv("FORGE_CI_GITHUB_TOKEN", "pat-under-test")
	t.Setenv("FORGE_CI_DISPATCH_TOKEN", "dispatch-pat-under-test")

	root, statePath := bareWorkspace(t, "true")
	require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci.yaml"),
		[]byte(githubPipelineYAML(root, statePath, srv.URL)), 0o600))

	mustRun(t, root, "forge-ci", "bootstrap", "--config", "forge-ci.yaml", "--root", root)

	repo := filepath.Join(root, "demo-repo")
	require.Empty(t, mustRun(t, repo, "git", "status", "--porcelain"),
		"the bootstrap committed the workflow files it converged")
	require.Contains(t, mustRun(t, repo, "git", "log", "--name-only", "-1"),
		".github/workflows/release.yaml")

	mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", root)

	releaseFile := filepath.Join(root, "demo-repo", ".github", "workflows", "release.yaml")
	runnerFile := filepath.Join(root, "demo-repo", ".github", "workflows", "ci-runner.yaml")
	require.FileExists(t, releaseFile)
	require.FileExists(t, runnerFile)
	require.Contains(t, fake.secrets, "FORGE_CI_GITHUB_TOKEN")
	require.Contains(t, fake.enabled, "release.yaml")
	require.Contains(t, fake.enabled, "ci-runner.yaml")

	notifyFile := filepath.Join(root, "demo-repo", ".github", "workflows", "notify.yaml")
	require.FileExists(t, notifyFile)
	require.Contains(t, fake.secrets, "FORGE_CI_DISPATCH_TOKEN")
	require.Contains(t, fake.enabled, "notify.yaml")

	notify, err := os.ReadFile(notifyFile)
	require.NoError(t, err)
	require.Contains(t, string(notify), "https://api.github.com/repos/acme/demo-factory/dispatches")
	require.Contains(t, string(notify), `"event_type":"member-pushed"`)
	require.NotContains(t, string(notify), "gh api",
		"a member repo carries no toolchain, so its notify workflow speaks the API through curl")

	ownership, err := os.ReadFile(filepath.Join(root, "manager-github.json"))
	require.NoError(t, err)
	require.Contains(t, string(ownership), "actions-secret/acme/demo-repo/FORGE_CI_GITHUB_TOKEN")

	entries, err := os.ReadDir(filepath.Join(statePath, "revisions"))
	require.NoError(t, err)

	revision := ""

	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		if !strings.HasSuffix(name, "-dirty") {
			revision = name
		}
	}

	require.NotEmpty(t, revision, "a clean revision must exist after the commit")
	raw, err := os.ReadFile(filepath.Join(statePath, "runs", revision, "remote", "default.json"))
	require.NoError(t, err)

	var remoteRun citypes.Run
	require.NoError(t, json.Unmarshal(raw, &remoteRun))
	require.Equal(t, citypes.StatusPassed, remoteRun.Status)

	want, err := os.ReadFile(releaseFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(releaseFile, []byte("edited: by hand\n"), 0o600))

	mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", root)

	got, err := os.ReadFile(releaseFile)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got), "a hand-edited workflow must drift back to its spec")
}
