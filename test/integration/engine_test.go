//go:build integration

package integration_test

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/engineadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

var binDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "forge-ci-bin")
	if err != nil {
		panic(err)
	}

	binDir = dir

	cmd := exec.Command("go", "build", "-o", dir, "./cmd/...")
	cmd.Dir = repoRoot()
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		panic(err)
	}

	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		panic(err)
	}

	// The suite is hermetic: the spawned engines inherit this process's
	// environment, and a developer machine's global signing config would
	// turn the state engine's plain commits into signed ones that demand
	// a key. Identity comes from env so commits still carry an author.
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull,
		"GIT_AUTHOR_NAME": "integration", "GIT_AUTHOR_EMAIL": "integration@example.com",
		"GIT_COMMITTER_NAME": "integration", "GIT_COMMITTER_EMAIL": "integration@example.com",
	} {
		if err := os.Setenv(k, v); err != nil {
			panic(err)
		}
	}

	code := m.Run()

	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func repoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	return filepath.Dir(filepath.Dir(wd))
}

func caller() *engineadapter.MCPCaller {
	return engineadapter.NewMCPCaller("", "test", os.Stderr)
}

func TestAManagerRealizesADirectoryOverMCP(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")

	var out citypes.ReconcileOutput

	err := caller().Call(context.Background(),
		"forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-local@v0.1.0",
		"reconcile",
		citypes.ReconcileInput{
			Manager:   "local",
			Resources: []citypes.Resource{{Kind: "directory", Name: dir}},
		}, &out)
	require.NoError(t, err)
	require.DirExists(t, dir)
	require.Equal(t, []citypes.Ownership{{Resource: "directory/" + dir, Manager: "local"}}, out.Owned)
}

func TestTheDryRunManagerChangesNothingOverMCP(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")

	var out citypes.ReconcileOutput

	err := caller().Call(context.Background(),
		"forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-dryrun@v0.1.0",
		"reconcile",
		citypes.ReconcileInput{
			Manager:   "dryrun",
			Resources: []citypes.Resource{{Kind: "directory", Name: dir}},
		}, &out)
	require.NoError(t, err)
	require.NoDirExists(t, dir)
	require.Equal(t, []string{"would realize directory/" + dir}, out.Actions)
}

func TestSwappingTheManagerIsRefusedOverMCP(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")

	err := caller().Call(context.Background(),
		"forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-dryrun@v0.1.0",
		"reconcile",
		citypes.ReconcileInput{
			Manager:   "dryrun",
			Resources: []citypes.Resource{{Kind: "directory", Name: dir}},
			Owned:     []citypes.Ownership{{Resource: "directory/" + dir, Manager: "local"}},
		}, &citypes.ReconcileOutput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "owned by a different manager")
}

func TestTheStateEngineRoundTripsOverMCP(t *testing.T) {
	root := t.TempDir()
	c := caller()
	uri := "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-state-git@v0.1.0"

	err := c.Call(context.Background(), uri, "put", citypes.StatePutInput{
		Kind: "revision", Key: "abc", Payload: `{"id":"abc"}`,
		Spec: map[string]any{"path": root},
	}, &citypes.StateGetOutput{})
	require.NoError(t, err)

	var got citypes.StateGetOutput

	err = c.Call(context.Background(), uri, "get", citypes.StateGetInput{
		Kind: "revision", Key: "abc", Spec: map[string]any{"path": root},
	}, &got)
	require.NoError(t, err)
	require.True(t, got.Found)
	require.JSONEq(t, `{"id":"abc"}`, got.Payload)
	require.DirExists(t, filepath.Join(root, ".git"))
}

func TestThePromotionEngineDecidesOverMCP(t *testing.T) {
	var out citypes.PromotionOutput

	err := caller().Call(context.Background(),
		"forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-promotion-all@v0.1.0",
		"evaluate",
		citypes.PromotionInput{
			Stage: "prod",
			Runs:  []citypes.Run{{Status: citypes.StatusPassed}, {Status: citypes.StatusFailed}},
		}, &out)
	require.NoError(t, err)
	require.False(t, out.Advance)
	require.Contains(t, out.Reason, "below the 100 percent needed")
}

func TestTheGateEngineWaitsForItsFileOverMCP(t *testing.T) {
	approval := filepath.Join(t.TempDir(), "approved")
	c := caller()
	uri := "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-gate-manual@v0.1.0"

	var pending citypes.GateResult

	err := c.Call(context.Background(), uri, "evaluate", citypes.GateInput{
		Run:  citypes.Run{Status: citypes.StatusPassed},
		Spec: map[string]any{"approvalPath": approval},
	}, &pending)
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPending, pending.Status)

	require.NoError(t, os.WriteFile(approval, nil, 0o600))

	var passed citypes.GateResult

	err = c.Call(context.Background(), uri, "evaluate", citypes.GateInput{
		Run:  citypes.Run{Status: citypes.StatusPassed},
		Spec: map[string]any{"approvalPath": approval},
	}, &passed)
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPassed, passed.Status)
}

func TestAnUnknownToolIsAnError(t *testing.T) {
	err := caller().Call(context.Background(),
		"forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-promotion-all@v0.1.0",
		"fly", map[string]any{}, &citypes.PromotionOutput{})
	require.Error(t, err)
}

func TestAnUnresolvableEngineIsAnError(t *testing.T) {
	err := caller().Call(context.Background(), "https://example.com/engine", "x", nil, nil)
	require.ErrorIs(t, err, engineadapter.ErrScheme)
}

// TestTheStateEngineLeavesUnrelatedDirtyFilesAlone pins the scoped-commit
// contract: a store often shares a repo with other work (the register
// stores its index beside its own sources), and a "ci:" commit must
// record exactly the file the write produced - never sweep the rest of a
// dirty tree along with it. That happened live: a pipeline run buried an
// operator's uncommitted config edit inside an index commit.
func TestTheStateEngineLeavesUnrelatedDirtyFilesAlone(t *testing.T) {
	root := t.TempDir()

	gitIn := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		// The machine's global git config stays out: a global signing
		// setup would turn these plain commits into signed ones.
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))

		return string(out)
	}

	gitIn("init", "-b", "main")
	gitIn("config", "user.email", "it@example.com")
	gitIn("config", "user.name", "it")
	require.NoError(t, os.WriteFile(filepath.Join(root, "unrelated.yaml"), []byte("v: 1\n"), 0o600))
	gitIn("add", ".")
	gitIn("commit", "-m", "base")

	// The operator's uncommitted work: one tracked edit, one new file.
	require.NoError(t, os.WriteFile(filepath.Join(root, "unrelated.yaml"), []byte("v: 2\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("mine\n"), 0o600))

	err := caller().Call(context.Background(),
		"forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-state-git@v0.1.0",
		"put", citypes.StatePutInput{
			Kind: "revision", Key: "abc", Payload: `{"id":"abc"}`,
			Spec: map[string]any{"path": root},
		}, &citypes.StateGetOutput{})
	require.NoError(t, err)

	committed := gitIn("show", "--name-only", "--format=", "HEAD")
	require.Contains(t, committed, "revisions/abc.json")
	require.NotContains(t, committed, "unrelated.yaml")
	require.NotContains(t, committed, "untracked.txt")

	status := gitIn("status", "--short")
	require.Contains(t, status, "unrelated.yaml", "the operator's edit must still be uncommitted")
	require.Contains(t, status, "untracked.txt", "the operator's new file must still be untracked")
}

// TestAContainerReleasePublishesOverTheWire runs ci-artifact-container as a
// real MCP server against a real registry.
//
// The wire is where the types are really checked. A container release carries
// an artifact list, a version and a tag prefix across it, and a field that
// maps in process and not on the wire is exactly the class of defect this
// suite exists for: the release engine carried tagPrefix on the wire and
// dropped it at the boundary, and every unit test passed.
func TestAContainerReleasePublishesOverTheWire(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")

	// The layout the build engine writes: one index over two architectures.
	layoutPath := filepath.Join(t.TempDir(), "toolchain.oci")
	require.NoError(t, os.MkdirAll(layoutPath, 0o750))

	idx := v1.ImageIndex(empty.Index)
	idx = mutate.IndexMediaType(idx, types.OCIImageIndex)

	for _, arch := range []string{"amd64", "arm64"} {
		cf, err := empty.Image.ConfigFile()
		require.NoError(t, err)

		cf = cf.DeepCopy()
		cf.OS, cf.Architecture = "linux", arch

		img, err := mutate.ConfigFile(empty.Image, cf)
		require.NoError(t, err)

		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{
			Add:        img,
			Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: arch}},
		})
	}

	_, err := layout.Write(layoutPath, idx)
	require.NoError(t, err)

	var out citypes.ArtifactOutput

	err = caller().Call(context.Background(),
		"forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-artifact-container@v0.1.0",
		"publish",
		citypes.ArtifactInput{
			Revision:  "abc123def456",
			Version:   "v0.50.0",
			TagPrefix: "forge",
			Artifacts: []forge.Artifact{
				{Name: "forge", Type: "binary", Location: "build/dist/forge_linux_amd64"},
				{Name: "toolchain", Type: "container", Location: "file://" + layoutPath},
			},
			Spec: map[string]any{
				"image":      host + "/owner/forge",
				"movingTags": []any{"latest"},
			},
		}, &out)
	require.NoError(t, err)

	require.True(t, out.Published)
	require.Equal(t, host+"/owner/forge:forge-v0.50.0", out.URL,
		"the prefix must survive the wire, or the image tag and the git tag disagree")
	require.Equal(t, []string{
		host + "/owner/forge:forge-v0.50.0",
		host + "/owner/forge:latest",
	}, out.Tagged)

	// It is really in the registry, both architectures, with the revision
	// somebody holding only the image can read back.
	ref, err := name.ParseReference(out.URL)
	require.NoError(t, err)

	back, err := remote.Index(ref)
	require.NoError(t, err)

	manifest, err := back.IndexManifest()
	require.NoError(t, err)
	require.Len(t, manifest.Manifests, 2)

	img, err := remote.Image(ref, remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "arm64"}))
	require.NoError(t, err)

	cf, err := img.ConfigFile()
	require.NoError(t, err)
	require.Equal(t, "abc123def456", cf.Config.Labels["org.opencontainers.image.revision"])
	require.Equal(t, "v0.50.0", cf.Config.Labels["org.opencontainers.image.version"])
}

// A stage that built no container fails the release rather than publishing
// nothing quietly. A tag silently left pointing at last week's image is worse
// than a red build: the operator reads a version number and gets something
// else.
func TestAContainerReleaseWithNothingToPublishFails(t *testing.T) {
	var out citypes.ArtifactOutput

	err := caller().Call(context.Background(),
		"forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-artifact-container@v0.1.0",
		"publish",
		citypes.ArtifactInput{
			Revision:  "abc123def456",
			Version:   "v0.50.0",
			Artifacts: []forge.Artifact{{Name: "forge", Type: "binary", Location: "x_linux_amd64"}},
			Spec:      map[string]any{"image": "example.invalid/owner/forge"},
		}, &out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no container artifact was built")
}

// The wire is where the types are really checked. Declare returns a nested
// object inside every resource spec, and a nested object is exactly the shape
// that has broken over MCP before.
func TestTheWatchTriggerDeclaresItsNotifyWorkflowsOverMCP(t *testing.T) {
	var out citypes.DeclareOutput

	err := caller().Call(context.Background(),
		"forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-trigger-watch@v0.1.0",
		"declare",
		map[string]any{"spec": map[string]any{
			"watch": []any{"one", "two"},
			"notify": map[string]any{
				"owner":     "an-owner",
				"factory":   "a-factory",
				"eventType": "member-pushed",
				"secret":    "A_TOKEN",
			},
		}}, &out)
	require.NoError(t, err)

	kinds := map[string]string{}
	for _, r := range out.Resources {
		kinds[r.Name] = r.Kind
	}

	require.Equal(t, map[string]string{
		"one/.github/workflows/notify.yaml": "file-content",
		"an-owner/one/A_TOKEN":              "actions-secret",
		"an-owner/one/notify.yaml":          "workflow-enabled",
		"two/.github/workflows/notify.yaml": "file-content",
		"an-owner/two/A_TOKEN":              "actions-secret",
		"an-owner/two/notify.yaml":          "workflow-enabled",
	}, kinds)

	for _, r := range out.Resources {
		if r.Name != "one/.github/workflows/notify.yaml" {
			continue
		}

		content, ok := r.Spec["content"].(string)
		require.True(t, ok, "content must survive the wire as a string")
		require.Contains(t, content, "gh api repos/an-owner/a-factory/dispatches")
		require.Contains(t, content, "${{ secrets.A_TOKEN }}")
	}
}

func TestTheWatchTriggerDeclaresNothingWithoutANotifyBlockOverMCP(t *testing.T) {
	var out citypes.DeclareOutput

	err := caller().Call(context.Background(),
		"forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-trigger-watch@v0.1.0",
		"declare",
		map[string]any{"spec": map[string]any{"watch": []any{"one"}}}, &out)
	require.NoError(t, err)
	require.Empty(t, out.Resources)
}

// The version the core decided has to reach the target's environment, or a
// binary stamps whatever tag its own repo happens to carry. It crosses the
// wire as RunInput.version, and the hand-written mapping in each engine is
// what turns it into an environment variable.
//
// Live case: both compute engines dropped it at that mapping, so every binary
// in the v0.45.1 release was stamped v0.44.5-6-gcb456c5 by git describe while
// the release itself was correct. A dropped field is a zero value, not a
// compile error, which is why only the wire can catch it.
func TestTheDecidedVersionReachesTheTargetEnvironmentOverMCP(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "seen.txt")

	// A target names "forge" or "forge-ci" and nothing else, both resolved by
	// name on PATH. So the probe is a shim called forge, ahead of the real one.
	shimDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(shimDir, "forge"),
		[]byte("#!/bin/sh\nprintf '%s|%s' \"$FORGE_CI_VERSION\" \"$FORGE_CI_REVISION\" > "+out+"\n"),
		0o700))
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var runOut citypes.RunOutput

	err := caller().Call(context.Background(),
		"forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-compute-local@v0.1.0",
		"run",
		citypes.RunInput{
			Revision: "rev-abc",
			Version:  "v9.9.9",
			Stage:    "publish",
			Substage: "dist",
			Root:     dir,
			Targets:  []citypes.Target{{Alias: "stamp", Forge: "test run stamp", In: []string{"."}}},
			Repos:    []citypes.RepoCheckout{{Name: ".", Path: dir, SHA: "abc"}},
		}, &runOut)
	require.NoError(t, err)

	seen, readErr := os.ReadFile(out)
	require.NoError(t, readErr, "the target must have run: %s", runOut.Output)
	require.Equal(t, "v9.9.9|rev-abc", string(seen),
		"the version the core decided must reach the target, not be dropped at the engine boundary")
}
