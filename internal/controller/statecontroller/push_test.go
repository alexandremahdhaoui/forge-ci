package statecontroller_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/statecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

// store stands up a state repo with a bare remote, the way a CI run meets
// one: cloned from a remote it is expected to write back to.
func store(t *testing.T) (root, remote string, c *statecontroller.Controller) {
	t.Helper()

	remote = filepath.Join(t.TempDir(), "state.git")
	root = filepath.Join(t.TempDir(), "state")

	gitRun(t, "", "init", "--bare", "-b", "main", remote)
	gitRun(t, "", "clone", "-q", remote, root)

	require.NoError(t, os.WriteFile(filepath.Join(root, "README"), []byte("state\n"), 0o600))
	gitRun(t, root, "add", "README")
	gitRun(t, root, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "first")
	gitRun(t, root, "push", "-q", "origin", "main")

	return root, remote, statecontroller.New(fsadapter.New(), gitadapter.New(execadapter.New()))
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()

	res, err := execadapter.New().Run(t.Context(), dir, "git", args...)
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, "git %v: %s", args, res.Stderr)

	return res.Stdout
}

// The finding this exists for.
//
// forge-factory reads revisions/<id>.json from this repo's REMOTE to pin a
// remote run's member shas. Nothing pushed the store, every CI run is a
// fresh clone, so the record was never there and every remote run fell back
// to floating tags with one log line to say so.
func TestAPushedRecordIsOnTheRemoteWhereForgeFactoryLooksForIt(t *testing.T) {
	root, remote, c := store(t)

	_, err := c.Put(t.Context(), citypes.StatePutInput{
		Kind: "revision", Key: "abc123def456", Payload: `{"id":"abc123def456"}`,
		Spec: map[string]any{"path": root},
	})
	require.NoError(t, err)

	assert.Contains(t,
		gitRun(t, remote, "show", "main:revisions/abc123def456.json"),
		"abc123def456",
		"a fresh clone must find the record, which is the whole point of a store")
}

// A remote that moved under this run is not a conflict to resolve by
// choosing. Each record is a file of its own, so replaying this run's commit
// on top of the other run's keeps both.
func TestAPushTheRemoteRefusedIsRebasedAndRetried(t *testing.T) {
	root, remote, c := store(t)

	// Another run pushes first.
	other := filepath.Join(t.TempDir(), "other")
	gitRun(t, "", "clone", "-q", remote, other)
	require.NoError(t, os.MkdirAll(filepath.Join(other, "revisions"), 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(other, "revisions", "theirs.json"), []byte(`{"id":"theirs"}`), 0o600))
	gitRun(t, other, "add", "revisions/theirs.json")
	gitRun(t, other, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "theirs")
	gitRun(t, other, "push", "-q", "origin", "main")

	_, err := c.Put(t.Context(), citypes.StatePutInput{
		Kind: "revision", Key: "mine", Payload: `{"id":"mine"}`,
		Spec: map[string]any{"path": root},
	})
	require.NoError(t, err)

	assert.Contains(t, gitRun(t, remote, "show", "main:revisions/mine.json"), "mine")
	assert.Contains(t, gitRun(t, remote, "show", "main:revisions/theirs.json"), "theirs",
		"neither run loses a record, and nobody picked a winner")
}

// A store with no origin is a legitimate state and every test fixture is in
// it. It commits and says nothing about a push it could not make.
func TestAStoreWithNoRemoteCommitsAndDoesNotFail(t *testing.T) {
	root := t.TempDir()
	c := statecontroller.New(fsadapter.New(), gitadapter.New(execadapter.New()))

	_, err := c.Put(t.Context(), citypes.StatePutInput{
		Kind: "revision", Key: "abc", Payload: `{"id":"abc"}`,
		Spec: map[string]any{"path": root},
	})
	require.NoError(t, err)

	assert.Contains(t, gitRun(t, root, "log", "--oneline", "-1"), "revision abc")
}

// Re-recording a payload the store already carries stages nothing, so there
// is no commit and therefore nothing to push. Without this every run would
// push on every record it re-wrote unchanged.
func TestAnIdenticalRecordCommitsNothingAndPushesNothing(t *testing.T) {
	root, remote, c := store(t)

	in := citypes.StatePutInput{
		Kind: "revision", Key: "abc", Payload: `{"id":"abc"}`,
		Spec: map[string]any{"path": root},
	}

	_, err := c.Put(t.Context(), in)
	require.NoError(t, err)

	before := gitRun(t, remote, "rev-parse", "main")

	_, err = c.Put(t.Context(), in)
	require.NoError(t, err)

	assert.Equal(t, before, gitRun(t, remote, "rev-parse", "main"))
}

// The store often shares a repo with other work. This asked git about the
// whole index and then committed with no pathspec, so a developer's staged
// file was swept into a "ci:" commit under the engine's name - and pushed.
// FOLLOWUP carried it as "the state engine sweeps a dirty store repo".
func TestARecordCommitDoesNotSweepSomebodyElsesStagedWork(t *testing.T) {
	root, remote, c := store(t)

	mine := filepath.Join(root, "half-finished.go")
	require.NoError(t, os.WriteFile(mine, []byte("package broken\n"), 0o600))
	gitRun(t, root, "add", "half-finished.go")

	_, err := c.Put(t.Context(), citypes.StatePutInput{
		Kind: "revision", Key: "abc", Payload: `{"id":"abc"}`,
		Spec: map[string]any{"path": root},
	})
	require.NoError(t, err)

	assert.NotContains(t, gitRun(t, root, "show", "--name-only", "HEAD"), "half-finished.go",
		"the record commit carries the record and nothing else")
	assert.Contains(t, gitRun(t, root, "diff", "--cached", "--name-only"), "half-finished.go",
		"and the developer's file is still staged, where they left it")
	assert.NotContains(t, gitRun(t, remote, "log", "--all", "--name-only"), "half-finished.go",
		"least of all pushed")
}

// The same defect from the index side: re-recording an identical payload
// stages nothing, so there is nothing to commit - however dirty the index is
// with somebody else's work.
func TestAnIdenticalRecordIgnoresSomebodyElsesStagedWork(t *testing.T) {
	root, _, c := store(t)

	in := citypes.StatePutInput{
		Kind: "revision", Key: "abc", Payload: `{"id":"abc"}`,
		Spec: map[string]any{"path": root},
	}

	_, err := c.Put(t.Context(), in)
	require.NoError(t, err)

	head := gitRun(t, root, "rev-parse", "HEAD")

	mine := filepath.Join(root, "half-finished.go")
	require.NoError(t, os.WriteFile(mine, []byte("package broken\n"), 0o600))
	gitRun(t, root, "add", "half-finished.go")

	_, err = c.Put(t.Context(), in)
	require.NoError(t, err)

	assert.Equal(t, head, gitRun(t, root, "rev-parse", "HEAD"),
		"the payload did not change, so the engine had nothing to record")
}
