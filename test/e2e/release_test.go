//go:build e2e

package e2e_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

type releaseFake struct {
	server *httptest.Server

	releases map[string]string

	drafts map[int64]fakeDraft
	assets map[string][]byte
	nextID int64
}

type fakeDraft struct {
	repo string
	tag  string
}

func newReleaseFake(t *testing.T) *releaseFake {
	t.Helper()

	f := &releaseFake{releases: map[string]string{}, drafts: map[int64]fakeDraft{}, assets: map[string][]byte{}}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/{owner}/{repo}/releases", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			TagName string `json:"tag_name"`
			Draft   bool   `json:"draft"`
		}

		require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
		require.True(t, in.Draft, "the first write of a release is a draft, so a crash leaves nothing a consumer can see")

		f.nextID++
		f.drafts[f.nextID] = fakeDraft{repo: r.PathValue("owner") + "/" + r.PathValue("repo"), tag: in.TagName}

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":         f.nextID,
			"draft":      true,
			"html_url":   f.server.URL + "/releases/" + in.TagName,
			"upload_url": f.server.URL + "/uploads{?name,label}",
		})
	})
	mux.HandleFunc("PATCH /repos/{owner}/{repo}/releases/{id}", func(w http.ResponseWriter, r *http.Request) {
		var id int64

		_, err := fmt.Sscanf(r.PathValue("id"), "%d", &id)
		require.NoError(t, err)

		draft, ok := f.drafts[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{}`))

			return
		}

		delete(f.drafts, id)
		f.releases[draft.repo] = draft.tag

		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":       id,
			"draft":    false,
			"html_url": f.server.URL + "/releases/" + draft.tag,
		})
	})
	mux.HandleFunc("GET /repos/{owner}/{repo}/releases/tags/{tag}", func(w http.ResponseWriter, r *http.Request) {
		tag, ok := f.releases[r.PathValue("owner")+"/"+r.PathValue("repo")]
		if !ok || tag != r.PathValue("tag") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{}`))

			return
		}

		_ = json.NewEncoder(w).Encode(map[string]string{
			"html_url": f.server.URL + "/releases/" + tag,
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

const releaseForgeYAML = `name: demo-repo

artifactStorePath: .forge/artifact-store.yaml

build:
  - name: demo-tool
    src: .
    dest: build/dist
    engine: forge://generic-builder
    platforms: [linux/amd64, linux/arm64]
    spec:
      command: sh
      args: ["-c", "mkdir -p build/dist && for f in build/dist/demo-tool build/dist/demo-tool_${FORGE_OS}_${FORGE_ARCH}; do printf '#!/bin/sh\necho demo-tool works %s\n' \"$(git rev-parse --short HEAD)\" > $f && chmod +x $f; done && printf '#!/bin/sh\necho extra\n' > build/dist/extra-tool_linux_arm64"]

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
      repo: owner/demo-repo
      apiBaseURL: ` + apiBaseURL + `
      assets: ["demo-repo/build/dist/extra-tool_linux_*"]
state: ci-state
triggers: [on-change]
targets:
  - alias: build-all
    binary: forge
    args: [test-all]
    in: [demo-repo]
stages:
  - name: build
    promotion: all-pass
    substages:
      - name: default
        engine: here
        targets: [build-all]
  - name: release
    promotion: all-pass
    substages:
      - name: publish
        engine: publish
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

	origin := filepath.Join(root, "remotes", "owner", "demo-repo.git")
	require.NoError(t, os.MkdirAll(origin, 0o750))
	mustRun(t, origin, "git", "init", "--bare")
	mustRun(t, repo, "git", "remote", "add", "origin", origin)

	require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci.yaml"),
		[]byte(releasePipelineYAML(root, statePath, fake.server.URL)), 0o600))

	mustRun(t, root, "forge-ci", "bootstrap", "--config", "forge-ci.yaml", "--root", ".")

	out := mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".")
	require.Contains(t, out, "released")

	revision := revisionID(t, statePath)

	require.Equal(t, "v0.1.0", fake.releases["owner/demo-repo"])

	tags := mustRun(t, origin, "git", "tag")
	require.Contains(t, tags, "v0.1.0")

	binary, ok := fake.assets["demo-tool_linux_amd64"]
	require.True(t, ok, "the built binary must ride the release; assets: %v", assetNames(fake))
	require.Contains(t, string(binary), "demo-tool works")

	rawIndex, ok := fake.assets["index.json"]
	require.True(t, ok, "the index must ride the release")

	var index artifactcontroller.Index
	require.NoError(t, json.Unmarshal(rawIndex, &index))
	require.Equal(t, revision, index.Revision)
	require.Equal(t, "v0.1.0", index.Release.Tag)
	require.Len(t, index.Tools, 1)
	require.Equal(t, "demo-tool", index.Tools[0].Name)
	require.Len(t, index.Tools[0].Platforms, 2)
	require.Contains(t, index.Tools[0].Platforms, "linux/arm64")
	_, ok = fake.assets["demo-tool_linux_arm64"]
	require.True(t, ok, "the cross build rides the release under its composed name")

	_, ok = fake.assets["extra-tool_linux_arm64"]
	require.True(t, ok, "a spec.assets glob match must ride the release")

	sum := sha256.Sum256(binary)
	require.Equal(t, "sha256:"+hex.EncodeToString(sum[:]),
		index.Tools[0].Platforms["linux/amd64"].Digest)
}

func TestASecondReleaseOfTheSameRevisionConverges(t *testing.T) {
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

	origin := filepath.Join(root, "remotes", "owner", "demo-repo.git")
	require.NoError(t, os.MkdirAll(origin, 0o750))
	mustRun(t, origin, "git", "init", "--bare")
	mustRun(t, origin, "git", "symbolic-ref", "HEAD", "refs/heads/main")
	mustRun(t, repo, "git", "remote", "add", "origin", origin)
	mustRun(t, repo, "git", "push", "origin", "main")

	require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci.yaml"),
		[]byte(releasePipelineYAML(root, statePath, fake.server.URL)), 0o600))

	mustRun(t, root, "forge-ci", "bootstrap", "--config", "forge-ci.yaml", "--root", ".")
	mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".")
	require.Equal(t, "v0.1.0", fake.releases["owner/demo-repo"])

	require.NoError(t, os.RemoveAll(repo))
	mustRun(t, root, "git", "clone", "--no-tags", origin, "demo-repo")
	mustRun(t, repo, "git", "config", "user.email", "e2e@example.com")
	mustRun(t, repo, "git", "config", "user.name", "e2e")
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".envrc"), nil, 0o600))

	local := mustRun(t, repo, "git", "tag")
	require.Empty(t, strings.TrimSpace(local),
		"the fresh clone must carry no tags, or this test proves nothing")

	out := mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".")
	require.NotContains(t, strings.ToLower(out), "rejected")

	tags := strings.Fields(mustRun(t, origin, "git", "tag"))
	require.Equal(t, []string{"v0.1.0"}, tags)
}

func assetNames(f *releaseFake) []string {
	names := make([]string, 0, len(f.assets))
	for name := range f.assets {
		names = append(names, name)
	}

	return names
}

func TestADirtyTreeNeverReleases(t *testing.T) {
	fake := newReleaseFake(t)

	root, statePath := workspace(t, "true")
	_ = statePath

	require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci.yaml"),
		[]byte(releasePipelineYAML(root, filepath.Join(root, "state2"), fake.server.URL)), 0o600))

	mustRun(t, root, "forge-ci", "bootstrap", "--config", "forge-ci.yaml", "--root", ".")

	repo := filepath.Join(root, "demo-repo")
	require.NoError(t, os.WriteFile(filepath.Join(repo, "forge.yaml"), []byte(releaseForgeYAML), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("uncommitted change"), 0o600))

	out, err := run(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".")
	require.Error(t, err, out)
	require.Contains(t, strings.ToLower(out), "dirty")
	require.Empty(t, fake.releases, "nothing may publish from a dirty tree")
}

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

	mustRun(t, repo, "git", "tag", "-m", "v0.1.0", "v0.1.0")
	mustRun(t, repo, "git", "tag", "-m", "v0.44.4", "v0.44.4")

	mustRun(t, repo, "git", "tag", "-m", "other-v9.0.0", "other-v9.0.0")

	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("two"), 0o600))
	mustRun(t, repo, "git", "add", ".")
	mustRun(t, repo, "git", "commit", "-m", "second")

	origin := filepath.Join(root, "remotes", "owner", "demo-repo.git")
	require.NoError(t, os.MkdirAll(origin, 0o750))
	mustRun(t, origin, "git", "init", "--bare")
	mustRun(t, repo, "git", "remote", "add", "origin", origin)

	require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci.yaml"),
		[]byte(releasePipelineYAML(root, statePath, fake.server.URL)), 0o600))

	mustRun(t, root, "forge-ci", "bootstrap", "--config", "forge-ci.yaml", "--root", ".")

	out := mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".")
	require.Contains(t, out, "released")

	tags := mustRun(t, origin, "git", "tag")
	require.Contains(t, tags, "v0.44.5")
	require.NotContains(t, tags, "v9.0.1")
	require.NotContains(t, mustRun(t, repo, "git", "tag", "--points-at", "HEAD"), "v0.1.0")

	require.Equal(t, "v0.44.5", fake.releases["owner/demo-repo"])
}
