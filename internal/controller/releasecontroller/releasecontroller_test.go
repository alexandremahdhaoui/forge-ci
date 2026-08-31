package releasecontroller_test

import (
	"context"
	"errors"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/releasecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/gitadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/githubadaptermock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

const (
	dir  = "a-repo"
	repo = "o/r"
	tag  = "v0.2.0"
	sha  = "abc123"
)

func TestItTagsAndPublishes(t *testing.T) {
	t.Parallel()

	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HasRemote(context.Background(), dir).Return(true, nil).Once()
	git.EXPECT().RemoteTagAt(context.Background(), dir, tag).Return("", false, nil).Once()
	git.EXPECT().Tag(context.Background(), dir, tag, sha).Return(nil).Once()

	api := githubadaptermock.NewMockAPI(t)
	api.EXPECT().ReleaseByTag(context.Background(), repo, tag).
		Return(githubadapter.Release{}, false, nil).Once()
	api.EXPECT().CreateRelease(context.Background(), repo, tag).
		Return(githubadapter.Release{HTMLURL: "http://releases/1"}, nil).Once()

	report, err := releasecontroller.New(git, api).Publish(context.Background(), dir, repo, tag, sha)
	require.NoError(t, err)
	assert.True(t, report.Tagged)
	assert.True(t, report.Published)
	assert.Equal(t, "http://releases/1", report.URL)
}

// The two halves fail separately, so they recover separately: a run that
// tagged and then lost the network must finish the job on the next attempt
// rather than refuse it.
func TestItPublishesATagThatAlreadyExists(t *testing.T) {
	t.Parallel()

	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HasRemote(context.Background(), dir).Return(true, nil).Once()
	git.EXPECT().RemoteTagAt(context.Background(), dir, tag).Return(sha, true, nil).Once()

	api := githubadaptermock.NewMockAPI(t)
	api.EXPECT().ReleaseByTag(context.Background(), repo, tag).
		Return(githubadapter.Release{}, false, nil).Once()
	api.EXPECT().CreateRelease(context.Background(), repo, tag).
		Return(githubadapter.Release{HTMLURL: "http://releases/1"}, nil).Once()

	report, err := releasecontroller.New(git, api).Publish(context.Background(), dir, repo, tag, sha)
	require.NoError(t, err)
	assert.False(t, report.Tagged)
	assert.True(t, report.Published)
}

// The remote is the authority: a fresh checkout carries no tags, so only
// origin knows the tag was already published. A local tag left behind by an
// interrupted run is Tag's business - the controller calls it whenever the
// remote lacks the tag, and Tag is what pushes the leftover.
func TestALocalOnlyTagIsFinishedNotRefused(t *testing.T) {
	t.Parallel()

	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HasRemote(context.Background(), dir).Return(true, nil).Once()
	git.EXPECT().RemoteTagAt(context.Background(), dir, tag).Return("", false, nil).Once()
	git.EXPECT().Tag(context.Background(), dir, tag, sha).Return(nil).Once()

	api := githubadaptermock.NewMockAPI(t)
	api.EXPECT().ReleaseByTag(context.Background(), repo, tag).
		Return(githubadapter.Release{HTMLURL: "http://releases/1"}, true, nil).Once()

	report, err := releasecontroller.New(git, api).Publish(context.Background(), dir, repo, tag, sha)
	require.NoError(t, err)
	assert.True(t, report.Tagged)
	assert.False(t, report.Published)
}

func TestItIsQuietWhenBothHalvesAlreadyExist(t *testing.T) {
	t.Parallel()

	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HasRemote(context.Background(), dir).Return(true, nil).Once()
	git.EXPECT().RemoteTagAt(context.Background(), dir, tag).Return(sha, true, nil).Once()

	api := githubadaptermock.NewMockAPI(t)
	api.EXPECT().ReleaseByTag(context.Background(), repo, tag).
		Return(githubadapter.Release{HTMLURL: "http://releases/1"}, true, nil).Once()

	report, err := releasecontroller.New(git, api).Publish(context.Background(), dir, repo, tag, sha)
	require.NoError(t, err)
	assert.False(t, report.Tagged)
	assert.False(t, report.Published)
	assert.Equal(t, "http://releases/1", report.URL)
}

// Moving a tag changes what a consumer already pinned, so the refusal names
// both commits rather than picking one.
func TestItRefusesToRepointATag(t *testing.T) {
	t.Parallel()

	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HasRemote(context.Background(), dir).Return(true, nil).Once()
	git.EXPECT().RemoteTagAt(context.Background(), dir, tag).Return("999999", true, nil).Once()

	_, err := releasecontroller.New(git, githubadaptermock.NewMockAPI(t)).
		Publish(context.Background(), dir, repo, tag, sha)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "999999")
	assert.Contains(t, err.Error(), sha)
	assert.Contains(t, err.Error(), "never moved")
}

// An unreadable remote is an error, never "absent". Falling through to
// "absent" would re-create a tag the remote may already hold, which is the
// exact mistake reading the remote exists to remove.
func TestAnUnreadableRemoteIsNeverAbsent(t *testing.T) {
	t.Parallel()

	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HasRemote(context.Background(), dir).Return(true, nil).Once()
	git.EXPECT().RemoteTagAt(context.Background(), dir, tag).Return("", false, errBoom).Once()

	_, err := releasecontroller.New(git, githubadaptermock.NewMockAPI(t)).
		Publish(context.Background(), dir, repo, tag, sha)
	require.ErrorIs(t, err, errBoom)
}

// A checkout with no origin is a legitimate state: the remote read is
// skipped, the tag half runs, and the release half proceeds.
func TestACheckoutWithNoOriginStillTags(t *testing.T) {
	t.Parallel()

	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HasRemote(context.Background(), dir).Return(false, nil).Once()
	git.EXPECT().Tag(context.Background(), dir, tag, sha).Return(nil).Once()

	api := githubadaptermock.NewMockAPI(t)
	api.EXPECT().ReleaseByTag(context.Background(), repo, tag).
		Return(githubadapter.Release{HTMLURL: "http://releases/1"}, true, nil).Once()

	report, err := releasecontroller.New(git, api).Publish(context.Background(), dir, repo, tag, sha)
	require.NoError(t, err)
	assert.True(t, report.Tagged)
}

func TestItRefusesWithoutARepoTagOrSHA(t *testing.T) {
	t.Parallel()

	c := releasecontroller.New(gitadaptermock.NewMockGit(t), githubadaptermock.NewMockAPI(t))

	_, err := c.Publish(context.Background(), dir, "  ", tag, sha)
	require.ErrorIs(t, err, releasecontroller.ErrRepo)

	_, err = c.Publish(context.Background(), dir, repo, "  ", sha)
	require.ErrorIs(t, err, releasecontroller.ErrTag)

	_, err = c.Publish(context.Background(), dir, repo, tag, "  ")
	require.ErrorIs(t, err, releasecontroller.ErrSHA)
}

func TestItReportsAFailureFromEitherHalf(t *testing.T) {
	t.Parallel()

	remoteless := gitadaptermock.NewMockGit(t)
	remoteless.EXPECT().HasRemote(context.Background(), dir).Return(false, errBoom).Once()

	_, err := releasecontroller.New(remoteless, githubadaptermock.NewMockAPI(t)).
		Publish(context.Background(), dir, repo, tag, sha)
	require.ErrorIs(t, err, errBoom)

	tagging := gitadaptermock.NewMockGit(t)
	tagging.EXPECT().HasRemote(context.Background(), dir).Return(true, nil).Once()
	tagging.EXPECT().RemoteTagAt(context.Background(), dir, tag).Return("", false, nil).Once()
	tagging.EXPECT().Tag(context.Background(), dir, tag, sha).Return(errBoom).Once()

	_, err = releasecontroller.New(tagging, githubadaptermock.NewMockAPI(t)).
		Publish(context.Background(), dir, repo, tag, sha)
	require.ErrorIs(t, err, errBoom)

	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HasRemote(context.Background(), dir).Return(true, nil).Once()
	git.EXPECT().RemoteTagAt(context.Background(), dir, tag).Return(sha, true, nil).Once()

	reader := githubadaptermock.NewMockAPI(t)
	reader.EXPECT().ReleaseByTag(context.Background(), repo, tag).
		Return(githubadapter.Release{}, false, errBoom).Once()

	_, err = releasecontroller.New(git, reader).Publish(context.Background(), dir, repo, tag, sha)
	require.ErrorIs(t, err, errBoom)

	git2 := gitadaptermock.NewMockGit(t)
	git2.EXPECT().HasRemote(context.Background(), dir).Return(true, nil).Once()
	git2.EXPECT().RemoteTagAt(context.Background(), dir, tag).Return(sha, true, nil).Once()

	creator := githubadaptermock.NewMockAPI(t)
	creator.EXPECT().ReleaseByTag(context.Background(), repo, tag).
		Return(githubadapter.Release{}, false, nil).Once()
	creator.EXPECT().CreateRelease(context.Background(), repo, tag).
		Return(githubadapter.Release{}, errBoom).Once()

	_, err = releasecontroller.New(git2, creator).Publish(context.Background(), dir, repo, tag, sha)
	require.ErrorIs(t, err, errBoom)
}
