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
      args: ["-c", "mkdir -p build/dist && printf '#!/bin/sh\necho demo-tool works\n' > build/dist/demo-tool_linux_amd64 && chmod +x build/dist/demo-tool_linux_amd64 && printf '#!/bin/sh\necho extra\n' > build/dist/extra-tool_linux_arm64"]

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
      assets: ["demo-repo/build/dist/extra-tool_linux_*"]
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

	// One aggregated release per version, in the repo the spec named, under
	// the same tag the members carry. The revision is still recorded, in the
	// index below, so a release still says which tuple it was proven from.
	require.Equal(t, "v0.1.0", fake.releases["owner/demo-repo"])

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
	require.Equal(t, "v0.1.0", index.Release.Tag)
	require.Len(t, index.Tools, 2)
	require.Equal(t, "demo-tool", index.Tools[0].Name)

	// spec.assets is the door for files no artifact record carries: the
	// glob-matched extra binary rides the same release and index.
	require.Equal(t, "extra-tool", index.Tools[1].Name)
	_, ok = fake.assets["extra-tool_linux_arm64"]
	require.True(t, ok, "a spec.assets glob match must ride the release")

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

// A repo that carries release history from before this pipeline existed
// continues that line rather than starting over. Live case: forge carried
// v0.1.0..v0.44.4 and the first release died trying to re-tag v0.1.0.
//
// The line lives in the repo the release is created in, and every member is
// tagged with the one number read off it. That is the decision: one workspace,
// one version, so a consumer who knows one member's version knows all of them.
func TestAPreTaggedLineContinuesRatherThanStartingOver(t *testing.T) {
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

	// The pre-existing history: tags minted long before this pipeline.
	mustRun(t, repo, "git", "tag", "-m", "v0.1.0", "v0.1.0")
	mustRun(t, repo, "git", "tag", "-m", "v0.44.4", "v0.44.4")

	// A tag from another factory's line sits alongside them. It must not be
	// read as this line's history: with no prefix configured, only the plain
	// semver line counts, so the next version is v0.44.5 and not v9.0.1.
	mustRun(t, repo, "git", "tag", "-m", "other-v9.0.0", "other-v9.0.0")

	// The head moves past the tagged commit, so the release tags fresh work.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("two"), 0o600))
	mustRun(t, repo, "git", "add", ".")
	mustRun(t, repo, "git", "commit", "-m", "second")

	origin := filepath.Join(root, "remotes", "owner", "demo-repo.git")
	require.NoError(t, os.MkdirAll(origin, 0o750))
	mustRun(t, origin, "git", "init", "--bare")
	mustRun(t, repo, "git", "remote", "add", "origin", origin)

	require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci.yaml"),
		[]byte(releasePipelineYAML(root, statePath, fake.server.URL)), 0o600))

	out := mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".")
	require.Contains(t, out, "released")

	// The line continues: v0.44.4 bumps to v0.44.5. Nothing tries to reuse
	// v0.1.0, and the other factory's v9.0.0 is not this line's history.
	tags := mustRun(t, origin, "git", "tag")
	require.Contains(t, tags, "v0.44.5")
	require.NotContains(t, tags, "v9.0.1")
	require.NotContains(t, mustRun(t, repo, "git", "tag", "--points-at", "HEAD"), "v0.1.0")

	// The release carries that same number. One workspace, one version.
	require.Equal(t, "v0.44.5", fake.releases["owner/demo-repo"])
}
