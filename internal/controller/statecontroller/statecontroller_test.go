package statecontroller_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/statecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/fsadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/gitadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func setup(t *testing.T) (*statecontroller.Controller, *gitadaptermock.MockGit, map[string]any) {
	t.Helper()

	git := gitadaptermock.NewMockGit(t)
	spec := map[string]any{"path": t.TempDir()}

	return statecontroller.New(fsadapter.New(), git), git, spec
}

// expectCommit allows the write path a record takes. The push half answers
// "no remote", which is the scratch store every fixture here is: the real
// push is proved against real git in push_test.go, because a mock cannot
// tell whether a record reached a remote.
func expectCommit(git *gitadaptermock.MockGit, dirty bool) {
	git.EXPECT().IsRepo(mock1(), mock2()).Return(true, nil).Maybe()
	git.EXPECT().Add(mock1(), mock2(), mock.Anything).Return(nil).Maybe()
	git.EXPECT().Staged(mock1(), mock2()).Return(dirty, nil).Maybe()
	git.EXPECT().Commit(mock1(), mock2(), mock3()).Return(nil).Maybe()
	git.EXPECT().HasRemote(mock1(), mock2()).Return(false, nil).Maybe()
}

func TestPutThenGetRoundTrips(t *testing.T) {
	c, git, spec := setup(t)
	expectCommit(git, true)

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: statecontroller.KindRevision, Key: "abc123", Payload: `{"id":"abc123"}`, Spec: spec,
	})
	require.NoError(t, err)

	got, err := c.Get(context.Background(), citypes.StateGetInput{
		Kind: statecontroller.KindRevision, Key: "abc123", Spec: spec,
	})
	require.NoError(t, err)
	require.True(t, got.Found)
	require.JSONEq(t, `{"id":"abc123"}`, got.Payload)
}

func TestGetMissingIsNotAnError(t *testing.T) {
	c, _, spec := setup(t)

	got, err := c.Get(context.Background(), citypes.StateGetInput{
		Kind: statecontroller.KindRevision, Key: "nope", Spec: spec,
	})
	require.NoError(t, err)
	require.False(t, got.Found)
	require.Empty(t, got.Payload)
}

func TestRunKeysNestByRevisionAndStage(t *testing.T) {
	c, git, spec := setup(t)
	expectCommit(git, true)

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: statecontroller.KindRun, Key: "rev1/build/default", Payload: `{}`, Spec: spec,
	})
	require.NoError(t, err)

	root := spec["path"].(string)
	require.FileExists(t, filepath.Join(root, "runs", "rev1", "build", "default.json"))

	list, err := c.List(context.Background(), citypes.StateGetInput{
		Kind: statecontroller.KindRun, Key: "rev1/build", Spec: spec,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"default"}, list.Keys)
}

func TestListOfNothingIsEmptyNotAnError(t *testing.T) {
	c, _, spec := setup(t)

	list, err := c.List(context.Background(), citypes.StateGetInput{
		Kind: statecontroller.KindRevision, Spec: spec,
	})
	require.NoError(t, err)
	require.Empty(t, list.Keys)
}

func TestPathIsRequired(t *testing.T) {
	c, _, _ := setup(t)

	_, err := c.Get(context.Background(), citypes.StateGetInput{Kind: statecontroller.KindRevision, Key: "a"})
	require.ErrorIs(t, err, statecontroller.ErrPath)
}

func TestUnknownKindIsRejected(t *testing.T) {
	c, _, spec := setup(t)

	_, err := c.Get(context.Background(), citypes.StateGetInput{Kind: "banana", Key: "a", Spec: spec})
	require.ErrorIs(t, err, statecontroller.ErrKind)

	_, err = c.List(context.Background(), citypes.StateGetInput{Kind: "banana", Spec: spec})
	require.ErrorIs(t, err, statecontroller.ErrKind)
}

func TestKeyIsRequired(t *testing.T) {
	c, _, spec := setup(t)

	_, err := c.Get(context.Background(), citypes.StateGetInput{Kind: statecontroller.KindRevision, Spec: spec})
	require.Error(t, err)
	require.Contains(t, err.Error(), "key is required")
}

func TestKeyCannotEscapeTheStateRoot(t *testing.T) {
	c, git, spec := setup(t)
	expectCommit(git, true)

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: statecontroller.KindRevision, Key: "../../escape", Payload: `{}`, Spec: spec,
	})
	require.NoError(t, err)

	root := spec["path"].(string)
	require.FileExists(t, filepath.Join(root, "revisions", "escape.json"))
}

func TestDeclareAsksForTheStateDirectories(t *testing.T) {
	c, _, spec := setup(t)

	out, err := c.Declare(spec)
	require.NoError(t, err)
	require.Len(t, out.Resources, 4)

	for _, r := range out.Resources {
		require.Equal(t, "directory", r.Kind)
	}
}

func TestDeclareNeedsAPath(t *testing.T) {
	c, _, _ := setup(t)

	_, err := c.Declare(nil)
	require.ErrorIs(t, err, statecontroller.ErrPath)
}

func TestAnEmptyRepoIsInitialised(t *testing.T) {
	c, git, spec := setup(t)
	git.EXPECT().IsRepo(mock1(), mock2()).Return(false, nil).Once()
	git.EXPECT().Init(mock1(), mock2()).Return(nil).Once()
	git.EXPECT().Add(mock1(), mock2(), mock.Anything).Return(nil).Once()
	git.EXPECT().Staged(mock1(), mock2()).Return(true, nil).Once()
	git.EXPECT().Commit(mock1(), mock2(), "ci: revision abc").Return(nil).Once()
	git.EXPECT().HasRemote(mock1(), mock2()).Return(false, nil).Once()

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: statecontroller.KindRevision, Key: "abc", Payload: `{}`, Spec: spec,
	})
	require.NoError(t, err)
}

// A store named by a relative path stages a repo-relative pathspec:
// git -C <root> resolves inside the repo, so a root-prefixed target would
// match nothing. This broke a pipeline whose state engine named its store
// as a plain sibling directory.
func TestARelativeStorePathStagesARepoRelativeTarget(t *testing.T) {
	git := gitadaptermock.NewMockGit(t)
	c := statecontroller.New(fsadapter.New(), git)

	t.Chdir(t.TempDir())
	require.NoError(t, os.MkdirAll("golden-state", 0o750))

	git.EXPECT().IsRepo(mock1(), "golden-state").Return(true, nil).Once()
	git.EXPECT().Add(mock1(), "golden-state", filepath.Join("revisions", "abc.json")).
		Return(nil).Once()
	git.EXPECT().Staged(mock1(), "golden-state").Return(false, nil).Once()

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: statecontroller.KindRevision, Key: "abc", Payload: `{}`,
		Spec: map[string]any{"path": "golden-state"},
	})
	require.NoError(t, err)
}

func TestNothingToCommitIsNotAnError(t *testing.T) {
	c, git, spec := setup(t)
	git.EXPECT().IsRepo(mock1(), mock2()).Return(true, nil).Once()
	git.EXPECT().Add(mock1(), mock2(), mock.Anything).Return(nil).Once()
	git.EXPECT().Staged(mock1(), mock2()).Return(false, nil).Once()

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: statecontroller.KindRevision, Key: "abc", Payload: `{}`, Spec: spec,
	})
	require.NoError(t, err)
}

func TestCommitFailureNamesWhatItWasRecording(t *testing.T) {
	c, git, spec := setup(t)
	git.EXPECT().IsRepo(mock1(), mock2()).Return(true, nil).Once()
	git.EXPECT().Add(mock1(), mock2(), mock.Anything).Return(errBoom).Once()

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: statecontroller.KindRevision, Key: "abc", Payload: `{}`, Spec: spec,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), `recording revision "abc"`)
}

func TestWithoutGitTheStateIsStillWritten(t *testing.T) {
	spec := map[string]any{"path": t.TempDir()}
	c := statecontroller.New(fsadapter.New(), nil)

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: statecontroller.KindRevision, Key: "abc", Payload: `{}`, Spec: spec,
	})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(spec["path"].(string), "revisions", "abc.json"))
}

func failingFS(t *testing.T) *fsadaptermock.MockFS {
	t.Helper()

	return fsadaptermock.NewMockFS(t)
}

func TestGetReportsAnExistsFailure(t *testing.T) {
	fs := failingFS(t)
	fs.EXPECT().Exists(mock.Anything).Return(false, errBoom).Once()

	c := statecontroller.New(fs, nil)

	_, err := c.Get(context.Background(), citypes.StateGetInput{
		Kind: statecontroller.KindRevision, Key: "a", Spec: map[string]any{"path": "/tmp/x"},
	})
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), `reading revision "a"`)
}

func TestGetReportsAReadFailure(t *testing.T) {
	fs := failingFS(t)
	fs.EXPECT().Exists(mock.Anything).Return(true, nil).Once()
	fs.EXPECT().ReadFile(mock.Anything).Return(nil, errBoom).Once()

	c := statecontroller.New(fs, nil)

	_, err := c.Get(context.Background(), citypes.StateGetInput{
		Kind: statecontroller.KindRevision, Key: "a", Spec: map[string]any{"path": "/tmp/x"},
	})
	require.ErrorIs(t, err, errBoom)
}

func TestPutNeedsAPathAndAKnownKind(t *testing.T) {
	c := statecontroller.New(fsadapter.New(), nil)

	_, err := c.Put(context.Background(), citypes.StatePutInput{Kind: statecontroller.KindRevision, Key: "a"})
	require.ErrorIs(t, err, statecontroller.ErrPath)

	_, err = c.Put(context.Background(), citypes.StatePutInput{
		Kind: "banana", Key: "a", Spec: map[string]any{"path": t.TempDir()},
	})
	require.ErrorIs(t, err, statecontroller.ErrKind)
}

func TestPutReportsAWriteFailure(t *testing.T) {
	fs := failingFS(t)
	fs.EXPECT().WriteFile(mock.Anything, mock.Anything).Return(errBoom).Once()

	c := statecontroller.New(fs, nil)

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: statecontroller.KindRevision, Key: "a", Spec: map[string]any{"path": "/tmp/x"},
	})
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), `writing revision "a"`)
}

func TestListReportsAFailure(t *testing.T) {
	fs := failingFS(t)
	fs.EXPECT().Walk(mock.Anything).Return(nil, errBoom).Once()

	c := statecontroller.New(fs, nil)

	_, err := c.List(context.Background(), citypes.StateGetInput{
		Kind: statecontroller.KindRevision, Spec: map[string]any{"path": "/tmp/x"},
	})
	require.ErrorIs(t, err, errBoom)
}

func TestListRecursesUnderAPrefix(t *testing.T) {
	root := t.TempDir()
	c := statecontroller.New(fsadapter.New(), nil)
	spec := map[string]any{"path": root, "kinds": []any{"index"}}

	for _, key := range []string{"go/example.com/pkg/1", "go/example.com/pkg/2", "rust/serde/1"} {
		_, err := c.Put(context.Background(), citypes.StatePutInput{
			Kind: "index", Key: key, Payload: "{}", Spec: spec,
		})
		require.NoError(t, err)
	}

	list, err := c.List(context.Background(), citypes.StateGetInput{
		Kind: "index", Key: "go", Spec: spec,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"example.com/pkg/1", "example.com/pkg/2"}, list.Keys)
}

func TestAnExtraKindComesFromTheSpec(t *testing.T) {
	root := t.TempDir()
	c := statecontroller.New(fsadapter.New(), nil)
	spec := map[string]any{"path": root, "kinds": []any{"request"}}

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: "request", Key: "r1", Payload: "{}", Spec: spec,
	})
	require.NoError(t, err)

	got, err := c.Get(context.Background(), citypes.StateGetInput{
		Kind: "request", Key: "r1", Spec: spec,
	})
	require.NoError(t, err)
	require.True(t, got.Found)

	// The same kind without the spec entry stays unknown.
	_, err = c.Get(context.Background(), citypes.StateGetInput{
		Kind: "request", Key: "r1", Spec: map[string]any{"path": root},
	})
	require.ErrorIs(t, err, statecontroller.ErrKind)
}

func TestAMalformedKindsSpecIsAnError(t *testing.T) {
	c := statecontroller.New(fsadapter.New(), nil)

	for _, kinds := range []any{"index", []any{"Not Kebab"}, []any{42}} {
		_, err := c.Get(context.Background(), citypes.StateGetInput{
			Kind: statecontroller.KindRevision, Key: "a",
			Spec: map[string]any{"path": t.TempDir(), "kinds": kinds},
		})
		require.ErrorIs(t, err, statecontroller.ErrKinds)
	}
}

func TestABuiltInKindCannotBeRemapped(t *testing.T) {
	root := t.TempDir()
	c := statecontroller.New(fsadapter.New(), nil)

	// Naming a built-in in spec.kinds is harmless: the built-in mapping wins.
	spec := map[string]any{"path": root, "kinds": []any{"revision"}}

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: "revision", Key: "k", Payload: "{}", Spec: spec,
	})
	require.NoError(t, err)

	got, err := c.Get(context.Background(), citypes.StateGetInput{
		Kind: "revision", Key: "k", Spec: map[string]any{"path": root},
	})
	require.NoError(t, err)
	require.True(t, got.Found)
}

func TestListNeedsAPath(t *testing.T) {
	c := statecontroller.New(fsadapter.New(), nil)

	_, err := c.List(context.Background(), citypes.StateGetInput{Kind: statecontroller.KindRevision})
	require.ErrorIs(t, err, statecontroller.ErrPath)
}

func TestEveryCommitFailureIsReported(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*gitadaptermock.MockGit)
	}{
		{"is-repo", func(g *gitadaptermock.MockGit) {
			g.EXPECT().IsRepo(mock.Anything, mock.Anything).Return(false, errBoom).Once()
		}},
		{"init", func(g *gitadaptermock.MockGit) {
			g.EXPECT().IsRepo(mock.Anything, mock.Anything).Return(false, nil).Once()
			g.EXPECT().Init(mock.Anything, mock.Anything).Return(errBoom).Once()
		}},
		{"dirty", func(g *gitadaptermock.MockGit) {
			g.EXPECT().IsRepo(mock.Anything, mock.Anything).Return(true, nil).Once()
			g.EXPECT().Add(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			g.EXPECT().Staged(mock.Anything, mock.Anything).Return(false, errBoom).Once()
		}},
		{"commit", func(g *gitadaptermock.MockGit) {
			g.EXPECT().IsRepo(mock.Anything, mock.Anything).Return(true, nil).Once()
			g.EXPECT().Add(mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
			g.EXPECT().Staged(mock.Anything, mock.Anything).Return(true, nil).Once()
			g.EXPECT().Commit(mock.Anything, mock.Anything, mock.Anything).Return(errBoom).Once()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			git := gitadaptermock.NewMockGit(t)
			tc.setup(git)

			c := statecontroller.New(fsadapter.New(), git)

			_, err := c.Put(context.Background(), citypes.StatePutInput{
				Kind: statecontroller.KindRevision, Key: "a", Payload: `{}`,
				Spec: map[string]any{"path": t.TempDir()},
			})
			require.ErrorIs(t, err, errBoom)
		})
	}
}

// spec.path is relative to the pipeline root, not to whatever directory
// forge-ci started in. Every managed resource already resolved against
// --root; the state engine resolved against the process's cwd, so a run
// started inside a member wrote the store one level too deep - and that
// member's .gitignore then swallowed every record, silently.
//
// The generated CI does exactly that: `cd <member>` then `--root ..`.
func TestTheStorePathIsRelativeToTheRootAndNotTheWorkingDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	git := gitadaptermock.NewMockGit(t)
	expectCommit(git, true)
	c := statecontroller.New(fsadapter.New(), git)

	spec := map[string]any{"path": "a-state", "root": root}

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: "owned", Key: "resources", Payload: "{}", Spec: spec,
	})
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(root, "a-state", "owned", "resources.json"),
		"the store belongs under the pipeline root")
	require.NoFileExists(t, filepath.Join("a-state", "owned", "resources.json"),
		"and never under the working directory")
}

// No root keeps the old behaviour exactly, so a caller that passes none is
// unchanged, and an absolute path is taken as written because an instance
// naming one means it.
func TestAnAbsolutePathAndAnAbsentRootAreTakenAsWritten(t *testing.T) {
	t.Parallel()

	abs := t.TempDir()
	git := gitadaptermock.NewMockGit(t)
	expectCommit(git, true)
	c := statecontroller.New(fsadapter.New(), git)

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: "owned", Key: "resources", Payload: "{}",
		Spec: map[string]any{"path": abs, "root": t.TempDir()},
	})
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(abs, "owned", "resources.json"))
}
