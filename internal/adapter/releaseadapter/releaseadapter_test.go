package releaseadapter_test

import (
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
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
