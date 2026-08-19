//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/stretchr/testify/require"
)

var binDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "forge-ci-e2e-bin")
	if err != nil {
		panic(err)
	}

	binDir = dir

	build := exec.Command("go", "build", "-o", dir, "./cmd/...")
	build.Dir = repoRoot()
	build.Stderr = os.Stderr

	if err := build.Run(); err != nil {
		panic(err)
	}

	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		panic(err)
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

func run(t *testing.T, dir, name string, args ...string) (string, error) {
	t.Helper()

	cmd := exec.CommandContext(context.Background(), name, args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()

	out, err := cmd.CombinedOutput()

	return string(out), err
}

func mustRun(t *testing.T, dir, name string, args ...string) string {
	t.Helper()

	out, err := run(t, dir, name, args...)
	require.NoError(t, err, out)

	return out
}

const forgeYAML = `name: demo-repo

artifactStorePath: .forge/artifact-store.yaml

build:
  - name: noop
    src: .
    engine: go://generic-builder
    spec:
      command: "true"

test:
  - name: unit
    runner: go://generic-test-runner
    spec:
      command: sh
      args: ["-c", "%s"]
`

func pipelineYAML(root, statePath string) string {
	return `name: demo
repos:
  - name: demo-repo
    url: file://` + filepath.Join(root, "demo-repo") + `
managers:
  - alias: local
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-local@v0.1.0"
    spec:
      statePath: ` + filepath.Join(root, "manager-local.json") + `
engines:
  - alias: here
    type: compute
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-compute-local@v0.1.0"
    manager: local
  - alias: ci-state
    type: state
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-state-git@v0.1.0"
    manager: local
    spec:
      path: ` + statePath + `
  - alias: on-change
    type: trigger
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-trigger-watch@v0.1.0"
    manager: local
    spec:
      watch: ["` + filepath.Join(root, "demo-repo") + `"]
  - alias: all-pass
    type: promotion
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-promotion-all@v0.1.0"
    manager: local
state: ci-state
triggers: [on-change]
targets:
  - alias: build-all
    forge: test-all
    in: [demo-repo]
stages:
  - name: build
    promotion: all-pass
    substages:
      - name: default
        engine: here
        manager: local
        targets: [build-all]
`
}

func workspace(t *testing.T, testCommand string) (root, statePath string) {
	t.Helper()

	root = t.TempDir()
	repo := filepath.Join(root, "demo-repo")
	statePath = filepath.Join(root, "state")

	require.NoError(t, os.MkdirAll(repo, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "forge.yaml"),
		[]byte(strings.Replace(forgeYAML, "%s", testCommand, 1)), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".envrc"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("one"), 0o600))

	mustRun(t, repo, "git", "init", "-b", "main")
	mustRun(t, repo, "git", "config", "user.email", "e2e@example.com")
	mustRun(t, repo, "git", "config", "user.name", "e2e")
	mustRun(t, repo, "git", "add", ".")
	mustRun(t, repo, "git", "commit", "-m", "first")

	require.NoError(t, os.WriteFile(filepath.Join(root, "pipeline.yaml"),
		[]byte(pipelineYAML(root, statePath)), 0o600))

	return root, statePath
}

func revisionID(t *testing.T, statePath string) string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(statePath, "revisions"))
	require.NoError(t, err)
	require.Len(t, entries, 1)

	return strings.TrimSuffix(entries[0].Name(), ".json")
}

func readRun(t *testing.T, statePath, revision string) citypes.Run {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join(statePath, "runs", revision, "build", "default.json"))
	require.NoError(t, err)

	var run citypes.Run
	require.NoError(t, json.Unmarshal(raw, &run))

	return run
}

func TestTheWholeLoopRunsLocallyWithNoCloud(t *testing.T) {
	root, statePath := workspace(t, "true")

	out := mustRun(t, root, "forge-ci", "bootstrap", "--config", filepath.Join(root, "pipeline.yaml"))
	require.Contains(t, out, "created directory "+statePath)
	require.DirExists(t, filepath.Join(statePath, "revisions"))
	require.DirExists(t, filepath.Join(statePath, "runs"))

	out = mustRun(t, root, "forge-ci", "apply", "--config", filepath.Join(root, "pipeline.yaml"))
	require.Contains(t, out, "stage build")
	require.Contains(t, out, "default passed")

	revision := revisionID(t, statePath)
	run := readRun(t, statePath, revision)

	require.Equal(t, citypes.StatusPassed, run.Status)
	require.Equal(t, "here", run.Engine)
	require.NotNil(t, run.Forge, "the artifact store must be harvested from the repo forge ran in")
	require.NotEmpty(t, run.Forge.TestReports)
	require.Equal(t, "unit", run.Forge.TestReports[0].Stage)
	require.Equal(t, "passed", run.Forge.TestReports[0].Status)

	require.DirExists(t, filepath.Join(statePath, ".git"))
}

func TestStatusReadsBackWhatApplyRecorded(t *testing.T) {
	root, statePath := workspace(t, "true")
	config := filepath.Join(root, "pipeline.yaml")

	mustRun(t, root, "forge-ci", "apply", "--config", config)

	out := mustRun(t, root, "forge-ci", "status", "--config", config)
	require.Contains(t, out, "revision "+revisionID(t, statePath))
	require.Contains(t, out, "every substage passed")
	require.Contains(t, out, "default passed")
}

func TestASecondApplyDoesNotRunAgain(t *testing.T) {
	root, statePath := workspace(t, "true")
	config := filepath.Join(root, "pipeline.yaml")

	mustRun(t, root, "forge-ci", "apply", "--config", config)
	first := readRun(t, statePath, revisionID(t, statePath))

	mustRun(t, root, "forge-ci", "apply", "--config", config)
	second := readRun(t, statePath, revisionID(t, statePath))

	require.Equal(t, first.StartedAt, second.StartedAt, "a passed substage must not be run again")
}

func TestANewCommitIsANewRevision(t *testing.T) {
	root, statePath := workspace(t, "true")
	config := filepath.Join(root, "pipeline.yaml")
	repo := filepath.Join(root, "demo-repo")

	mustRun(t, root, "forge-ci", "apply", "--config", config)
	before := revisionID(t, statePath)

	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("two"), 0o600))
	mustRun(t, repo, "git", "add", ".")
	mustRun(t, repo, "git", "commit", "-m", "second")

	mustRun(t, root, "forge-ci", "apply", "--config", config)

	entries, err := os.ReadDir(filepath.Join(statePath, "revisions"))
	require.NoError(t, err)
	require.Len(t, entries, 2)

	names := []string{
		strings.TrimSuffix(entries[0].Name(), ".json"),
		strings.TrimSuffix(entries[1].Name(), ".json"),
	}
	require.Contains(t, names, before)
}

func TestAFailingStageBlocksAndExitsNonZero(t *testing.T) {
	root, statePath := workspace(t, "exit 1")
	config := filepath.Join(root, "pipeline.yaml")

	out, err := run(t, root, "forge-ci", "apply", "--config", config)
	require.Error(t, err, out)
	require.Contains(t, out, "default failed")

	run := readRun(t, statePath, revisionID(t, statePath))
	require.Equal(t, citypes.StatusFailed, run.Status)
}

func TestPollSeesTheRepoMoveOnce(t *testing.T) {
	root, _ := workspace(t, "true")
	config := filepath.Join(root, "pipeline.yaml")
	repo := filepath.Join(root, "demo-repo")

	mustRun(t, root, "forge-ci", "bootstrap", "--config", config)

	first := mustRun(t, root, "forge-ci", "poll", "--config", config)
	require.Contains(t, first, "changed:")

	second := mustRun(t, root, "forge-ci", "poll", "--config", config)
	require.Contains(t, second, "nothing moved")

	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("three"), 0o600))
	mustRun(t, repo, "git", "add", ".")
	mustRun(t, repo, "git", "commit", "-m", "third")

	third := mustRun(t, root, "forge-ci", "poll", "--config", config)
	require.Contains(t, third, "changed:")
}

func TestSwappingTheManagerIsRefusedEndToEnd(t *testing.T) {
	root, _ := workspace(t, "true")
	config := filepath.Join(root, "pipeline.yaml")

	mustRun(t, root, "forge-ci", "bootstrap", "--config", config)

	raw, err := os.ReadFile(config)
	require.NoError(t, err)

	swapped := strings.Replace(string(raw), "cmd/ci-manager-local@v0.1.0", "cmd/ci-manager-dryrun@v0.1.0", 1)
	swapped = strings.Replace(swapped, "alias: local", "alias: dryrun", 1)
	swapped = strings.ReplaceAll(swapped, "manager: local", "manager: dryrun")
	require.NoError(t, os.WriteFile(config, []byte(swapped), 0o600))

	out, err := run(t, root, "forge-ci", "bootstrap", "--config", config)
	require.Error(t, err, out)
	require.Contains(t, out, "owned by a different manager")
	require.Contains(t, out, "import it or destroy it first")
}
