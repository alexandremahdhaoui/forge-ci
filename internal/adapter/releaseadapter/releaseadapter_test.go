package releaseadapter_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/releaseadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/execadaptermock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestTagPointsAVersionAtACommitAndPushes(t *testing.T) {
	t.Parallel()

	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, "/w/a", "git", "tag", "--list", "v0.2.0").
		Return(execadapter.Result{}, nil).Once()
	runner.EXPECT().Run(mock.Anything, "/w/a", "git", "var", "GIT_COMMITTER_IDENT").
		Return(execadapter.Result{Stdout: "A Dev <dev@example.com> 1 +0000\n"}, nil).Once()
	// Annotated with a message: a lightweight tag fails with "no tag
	// message" wherever the machine's git config signs tags.
	runner.EXPECT().Run(mock.Anything, "/w/a", "git", "tag", "-m", "v0.2.0", "v0.2.0", "abc123").
		Return(execadapter.Result{}, nil).Once()
	runner.EXPECT().Run(mock.Anything, "/w/a", "git", "push", "origin", "v0.2.0").
		Return(execadapter.Result{}, nil).Once()

	require.NoError(t, releaseadapter.New(runner).Tag(t.Context(), "/w/a", "v0.2.0", "abc123"))
}

func TestATagIsNeverMoved(t *testing.T) {
	t.Parallel()

	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, mock.Anything, "git", "tag", "--list", "v0.2.0").
		Return(execadapter.Result{Stdout: "v0.2.0\n"}, nil).Once()

	err := releaseadapter.New(runner).Tag(t.Context(), "/w/a", "v0.2.0", "abc123")
	require.ErrorContains(t, err, "already exists",
		"a moved tag changes what a consumer already pinned")
}

func TestANonZeroExitIsAFailure(t *testing.T) {
	t.Parallel()

	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, mock.Anything, "git", "tag", "--list", "v0.2.0").
		Return(execadapter.Result{ExitCode: 128, Stderr: "not a repository"}, nil).Once()

	err := releaseadapter.New(runner).Tag(t.Context(), "/w/a", "v0.2.0", "abc123")
	require.ErrorContains(t, err, "not a repository",
		"a command that exits non zero comes back with no error")
}

func TestReleaseAttachesEveryFileAndReturnsTheURL(t *testing.T) {
	t.Parallel()

	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, "/w", "gh",
		"release", "create", "v0.2.0", "--generate-notes", "/out/cli", "/out/a.tar").
		Return(execadapter.Result{Stdout: "https://github.com/x/y/releases/tag/v0.2.0\n"}, nil).Once()

	url, err := releaseadapter.New(runner).
		Release(t.Context(), "/w", "v0.2.0", []string{"/out/cli", "/out/a.tar"})
	require.NoError(t, err)
	assert.Equal(t, "https://github.com/x/y/releases/tag/v0.2.0", url)
}

func TestReleaseWithNoFiles(t *testing.T) {
	t.Parallel()

	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, "/w", "gh", "release", "create", "v0.2.0", "--generate-notes").
		Return(execadapter.Result{Stdout: "u"}, nil).Once()

	_, err := releaseadapter.New(runner).Release(t.Context(), "/w", "v0.2.0", nil)
	require.NoError(t, err)
}

func TestAFailureToRunIsReported(t *testing.T) {
	t.Parallel()

	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, mock.Anything, "gh", mock.Anything, mock.Anything,
		mock.Anything, mock.Anything).Return(execadapter.Result{}, assert.AnError).Once()

	_, err := releaseadapter.New(runner).Release(t.Context(), "/w", "v0.2.0", nil)
	require.ErrorIs(t, err, assert.AnError)
}

// TagAt is the only tag question a release engine gets to ask. There is no
// LatestTag any more: the version is decided by the core, and an engine that
// reads a tag line to compute one is the second authority that put every
// member of the workspace on a version line of its own.
func TestTagAtAnswersWhereTheTagPoints(t *testing.T) {
	t.Parallel()

	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, "/w/a", "git", "tag", "--list", "v0.50.0").
		Return(execadapter.Result{Stdout: "v0.50.0\n"}, nil).Once()
	runner.EXPECT().Run(mock.Anything, "/w/a", "git", "rev-list", "-n", "1", "v0.50.0").
		Return(execadapter.Result{Stdout: "abc123\n"}, nil).Once()

	sha, found, err := releaseadapter.New(runner).TagAt(t.Context(), "/w/a", "v0.50.0")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "abc123", sha,
		"an annotated tag's own object is not the commit, so rev-list is what answers")
}

// A repo that does not carry the tag is the ordinary case, not a failure: it
// is what a member's first release under a version looks like.
func TestTagAtOnARepoWithoutTheTagIsNotAnError(t *testing.T) {
	t.Parallel()

	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, "/w/a", "git", "tag", "--list", "v0.50.0").
		Return(execadapter.Result{Stdout: "\n"}, nil).Once()

	sha, found, err := releaseadapter.New(runner).TagAt(t.Context(), "/w/a", "v0.50.0")
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, sha)
}

// The test that catches this class, and the only shape that can. Every mocked
// case above passes against a Tag with no identity handling, because what
// failed was real git writing a real object. An annotated tag is an object,
// so it needs a committer exactly as a commit does.
//
// Live case: forge-self-factory run 33281063209 died here with "fatal: empty
// ident name" one stage after the same defect was fixed in gitadapter.
func TestTagWorksOnAHostWithNoGitIdentityAtAll(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	// Unset, not empty: git reads an empty GIT_COMMITTER_NAME as an identity
	// of "", and the environment beats -c, so only an absent one can be
	// overridden. Absent is the state a runner is in.
	for _, k := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
	} {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}

	exec := execadapter.New()
	ctx := context.Background()

	run := func(args ...string) execadapter.Result {
		t.Helper()

		res, err := exec.Run(ctx, dir, "git", args...)
		require.NoError(t, err)

		return res
	}

	require.Zero(t, run("init", "-q").ExitCode)

	// A commit to tag. gitadapter carries the identity for this half.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a"), []byte("a\n"), 0o600))
	require.Zero(t, run("add", "a").ExitCode)
	require.NoError(t, gitadapter.New(exec).Commit(ctx, dir, "seed"))

	sha, err := gitadapter.New(exec).HeadSHA(ctx, dir)
	require.NoError(t, err)

	// Prove the premise before proving the fix: a bare annotated tag fails
	// here, so a passing test cannot be passing for the wrong reason.
	bare := run("tag", "-m", "v0.0.1", "v0.0.1", sha)
	require.NotZero(t, bare.ExitCode, "a bare annotated tag must fail with no identity")
	require.Contains(t, bare.Stderr, "ident")

	// Tag pushes, and there is no remote here. The tag object is what this
	// test is about, so assert the object exists whatever the push did.
	_ = releaseadapter.New(exec).Tag(ctx, dir, "v0.2.0", sha)

	listed := run("tag", "--list", "v0.2.0")
	require.Zero(t, listed.ExitCode)
	require.Contains(t, listed.Stdout, "v0.2.0", "the annotated tag must exist with no ambient identity")

	tagger := run("for-each-ref", "--format=%(taggername) %(taggeremail)", "refs/tags/v0.2.0")
	require.Contains(t, tagger.Stdout, "alexandre.mahdhaoui@gmail.com")
}
