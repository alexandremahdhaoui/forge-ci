package notifycontroller_test

import (
	"context"
	"errors"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/notifycontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/githubadaptermock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

const (
	repo  = "o/r"
	title = "intake is failing"
	body  = "http://runs/1 failed"
)

func TestItFilesAnIssueWhenNoneIsOpen(t *testing.T) {
	t.Parallel()

	api := githubadaptermock.NewMockAPI(t)
	api.EXPECT().OpenIssueByTitle(context.Background(), repo, title).
		Return(githubadapter.Issue{}, false, nil).Once()
	api.EXPECT().CreateIssue(context.Background(), repo, title, body).
		Return(githubadapter.Issue{Number: 7, HTMLURL: "http://issues/7"}, nil).Once()

	report, err := notifycontroller.New(api).Announce(context.Background(), repo, title, body)
	require.NoError(t, err)
	assert.True(t, report.Filed)
	assert.Equal(t, "http://issues/7", report.URL)
}

// The whole reason this is not a shell `if`: a job that fails every morning
// must leave one issue open, not thirty.
func TestItFilesNothingWhenAnIssueIsAlreadyOpen(t *testing.T) {
	t.Parallel()

	api := githubadaptermock.NewMockAPI(t)
	api.EXPECT().OpenIssueByTitle(context.Background(), repo, title).
		Return(githubadapter.Issue{Number: 7, HTMLURL: "http://issues/7"}, true, nil).Once()

	report, err := notifycontroller.New(api).Announce(context.Background(), repo, title, body)
	require.NoError(t, err)
	assert.False(t, report.Filed)
	assert.Equal(t, "http://issues/7", report.URL)
	assert.Contains(t, report.Reason, "already open")
}

func TestItRefusesWithoutARepoOrATitle(t *testing.T) {
	t.Parallel()

	c := notifycontroller.New(githubadaptermock.NewMockAPI(t))

	_, err := c.Announce(context.Background(), "  ", title, body)
	require.ErrorIs(t, err, notifycontroller.ErrRepo)

	// A blank title would dedupe against every other blank one, so the whole
	// mechanism collapses into a single issue for unrelated failures.
	_, err = c.Announce(context.Background(), repo, "  ", body)
	require.ErrorIs(t, err, notifycontroller.ErrTitle)
}

func TestItReportsAFailureReadingOrWriting(t *testing.T) {
	t.Parallel()

	reading := githubadaptermock.NewMockAPI(t)
	reading.EXPECT().OpenIssueByTitle(context.Background(), repo, title).
		Return(githubadapter.Issue{}, false, errBoom).Once()

	_, err := notifycontroller.New(reading).Announce(context.Background(), repo, title, body)
	require.ErrorIs(t, err, errBoom)

	writing := githubadaptermock.NewMockAPI(t)
	writing.EXPECT().OpenIssueByTitle(context.Background(), repo, title).
		Return(githubadapter.Issue{}, false, nil).Once()
	writing.EXPECT().CreateIssue(context.Background(), repo, title, body).
		Return(githubadapter.Issue{}, errBoom).Once()

	_, err = notifycontroller.New(writing).Announce(context.Background(), repo, title, body)
	require.ErrorIs(t, err, errBoom)
}
