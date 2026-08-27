//go:build integration

package integration_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/engineadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/stretchr/testify/require"
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
