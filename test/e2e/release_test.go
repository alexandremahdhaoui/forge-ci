//go:build e2e

package e2e_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactcontroller"
)

// releaseFake is the GitHub API surface a release needs, recording what it
// was sent. gh is absent on this host, so the engine's API fallback is what
// executes - the same path a bare CI runner takes.
type releaseFake struct {
	server   *httptest.Server
	releases map[string]string
	assets   map[string][]byte
}

func newReleaseFake(t *testing.T) *releaseFake {
	t.Helper()

	f := &releaseFake{releases: map[string]string{}, assets: map[string][]byte{}}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/{owner}/{repo}/releases", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			TagName string `json:"tag_name"`
		}

		require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
		f.releases[r.PathValue("owner")+"/"+r.PathValue("repo")] = in.TagName

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"html_url":   f.server.URL + "/releases/" + in.TagName,
			"upload_url": f.server.URL + "/uploads{?name,label}",
		})
	})
	mux.HandleFunc("POST /uploads", func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		f.assets[r.URL.Query().Get("name")] = data
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	})

	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)

	return f
}

// The build produces a platform-suffixed binary the way an instance would
// (generic-builder, cross-building convention), the release stage tags the
// member and publishes ONE aggregated release per revision: the binary plus
// the index that pins its digest.
const releaseForgeYAML = `name: demo-repo

artifactStorePath: .forge/artifact-store.yaml

build:
  - name: demo-tool_linux_amd64
    src: .
    dest: build/dist
    engine: forge://generic-builder
    spec:
      command: sh
      args: ["-c", "mkdir -p build/dist && printf '#!/bin/sh\necho demo-tool works\n' > build/dist/demo-tool_linux_amd64 && chmod +x build/dist/demo-tool_linux_amd64"]

test:
  - name: unit
    runner: forge://generic-test-runner
    spec:
      command: sh
      args: ["-c", "true"]
`

func releasePipelineYAML(root, statePath, apiBaseURL string) string {
	return `name: demo
repos:
  - name: demo-repo
    url: file://` + filepath.Join(root, "demo-repo") + `
managers:
  - alias: local
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-local@v0.1.0"
    spec:
      statePath: ` + filepath.Join(root, "manager-local.json") + `
engines:
  - alias: here
    type: compute
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-compute-local@v0.1.0"
    manager: local
  - alias: ci-state
    type: state
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-state-git@v0.1.0"
    manager: local
    spec:
      path: ` + statePath + `
  - alias: on-change
    type: trigger
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-trigger-watch@v0.1.0"
    manager: local
    spec:
      watch: ["` + filepath.Join(root, "demo-repo") + `"]
  - alias: all-pass
    type: promotion
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-promotion-all@v0.1.0"
    manager: local
  - alias: publish
    type: artifact
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-artifact-release@v0.1.0"
    manager: local
    spec:
      releaseIn: demo-repo
      apiBaseURL: ` + apiBaseURL + `
state: ci-state
triggers: [on-change]
targets:
  - alias: build-all
    forge: test-all
    in: [demo-repo]
stages:
  - name: build
    mint: true
    release: publish
    promotion: all-pass
    substages:
      - name: default
        engine: here
        manager: local
        targets: [build-all]
`
}

func TestAGreenBuildReleasesTheAggregatedDistribution(t *testing.T) {
	fake := newReleaseFake(t)

	root := t.TempDir()
	repo := filepath.Join(root, "demo-repo")
	statePath := filepath.Join(root, "state")

	require.NoError(t, os.MkdirAll(repo, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "forge.yaml"), []byte(releaseForgeYAML), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".envrc"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"),
		[]byte("/.forge/\n.envrc\n/build/\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("one"), 0o600))

	mustRun(t, repo, "git", "init", "-b", "main")
	mustRun(t, repo, "git", "config", "user.email", "e2e@example.com")
	mustRun(t, repo, "git", "config", "user.name", "e2e")
	mustRun(t, repo, "git", "add", ".")
	mustRun(t, repo, "git", "commit", "-m", "first")

	// The origin is a local bare mirror whose path still names owner/repo;
	// the tag push and the API's repo derivation both ride it.
	origin := filepath.Join(root, "remotes", "owner", "demo-repo.git")
	require.NoError(t, os.MkdirAll(origin, 0o750))
	mustRun(t, origin, "git", "init", "--bare")
	mustRun(t, repo, "git", "remote", "add", "origin", origin)

	require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci.yaml"),
		[]byte(releasePipelineYAML(root, statePath, fake.server.URL)), 0o600))

	out := mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".")
	require.Contains(t, out, "released")

	revision := revisionID(t, statePath)

	// One aggregated release per revision, in the repo the spec named.
	require.Equal(t, "dist-"+revision, fake.releases["owner/demo-repo"])

	// The member is tagged with the semver the pipeline decided.
	tags := mustRun(t, origin, "git", "tag")
	require.Contains(t, tags, "v0.1.0")

	// The release carries the built binary and the index that pins it.
	binary, ok := fake.assets["demo-tool_linux_amd64"]
	require.True(t, ok, "the built binary must ride the release; assets: %v", assetNames(fake))
	require.Contains(t, string(binary), "demo-tool works")

	rawIndex, ok := fake.assets["index.json"]
	require.True(t, ok, "the index must ride the release")

	var index artifactcontroller.Index
	require.NoError(t, json.Unmarshal(rawIndex, &index))
	require.Equal(t, revision, index.Revision)
	require.Equal(t, "dist-"+revision, index.Release.Tag)
	require.Len(t, index.Tools, 1)
	require.Equal(t, "demo-tool", index.Tools[0].Name)

	// The digest in the index is the digest of the uploaded bytes: the
	// index never claims a byte nobody hashed.
	sum := sha256.Sum256(binary)
	require.Equal(t, "sha256:"+hex.EncodeToString(sum[:]),
		index.Tools[0].Platforms["linux/amd64"].Digest)
}

func assetNames(f *releaseFake) []string {
	names := make([]string, 0, len(f.assets))
	for name := range f.assets {
		names = append(names, name)
	}

	return names
}

// A dirty tree never releases: the guard that keeps an unproven byte out of
// every distribution.
func TestADirtyTreeNeverReleases(t *testing.T) {
	fake := newReleaseFake(t)

	root, statePath := workspace(t, "true")
	_ = statePath

	require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci.yaml"),
		[]byte(releasePipelineYAML(root, filepath.Join(root, "state2"), fake.server.URL)), 0o600))

	repo := filepath.Join(root, "demo-repo")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "forge.yaml"), []byte(releaseForgeYAML), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("uncommitted change"), 0o600))

	out, err := run(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".")
	require.Error(t, err, out)
	require.Contains(t, strings.ToLower(out), "dirty")
	require.Empty(t, fake.releases, "nothing may publish from a dirty tree")
}
