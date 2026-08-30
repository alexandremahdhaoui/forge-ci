package clidriver_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/notifycontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/releasecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/driver/clidriver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeGitHub struct {
	tokenEnv string
	base     string

	publishArgs []string
	report      releasecontroller.Report

	announceArgs []string
	announcement notifycontroller.Report
}

func (f *fakeGitHub) Publish(_ context.Context, dir, repo, tag, sha string) (releasecontroller.Report, error) {
	f.publishArgs = []string{dir, repo, tag, sha}

	return f.report, nil
}

func (f *fakeGitHub) Announce(_ context.Context, repo, title, body string) (notifycontroller.Report, error) {
	f.announceArgs = []string{repo, title, body}

	return f.announcement, nil
}

func (f *fakeGitHub) wire() clidriver.GitHubFor {
	return func(tokenEnv, base string) (clidriver.Publisher, clidriver.Announcer) {
		f.tokenEnv, f.base = tokenEnv, base

		return f, f
	}
}

// Both verbs answer before any config is read. The workflow that calls them
// has a checkout and no forge-ci.yaml, so loading one would fail on a file
// that has no business existing there.
func TestTheGitHubVerbsNeedNoPipelineFile(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{
		{"release", "--repo", "o/r", "--tag", "v0.2.0", "--sha", "abc"},
		{"report-failure", "--repo", "o/r", "--title", "intake is failing"},
	} {
		fake := &fakeGitHub{}
		out := &bytes.Buffer{}

		err := clidriver.New(out, nil).WithGitHub(fake.wire()).
			Run(context.Background(), args)
		require.NoErrorf(t, err, "%v must not read a pipeline", args)
	}
}

func TestReleasePassesEveryFlagThrough(t *testing.T) {
	t.Parallel()

	fake := &fakeGitHub{report: releasecontroller.Report{Reason: "published", URL: "http://releases/1"}}
	out := &bytes.Buffer{}

	err := clidriver.New(out, nil).WithGitHub(fake.wire()).Run(context.Background(),
		[]string{
			"release", "--repo", "o/r", "--tag", "v0.2.0", "--sha", "abc",
			"--dir", "a-repo", "--token-env", "A_TOKEN", "--api-base-url", "http://fake",
		})
	require.NoError(t, err)

	assert.Equal(t, []string{"a-repo", "o/r", "v0.2.0", "abc"}, fake.publishArgs)
	assert.Contains(t, out.String(), "http://releases/1")

	// The credential is named by a flag and read nowhere else. This is the
	// whole difference from shelling out to a CLI that takes whatever token
	// the host happens to carry.
	assert.Equal(t, "A_TOKEN", fake.tokenEnv)
	assert.Equal(t, "http://fake", fake.base)
}

func TestReportFailurePassesEveryFlagThrough(t *testing.T) {
	t.Parallel()

	fake := &fakeGitHub{announcement: notifycontroller.Report{Reason: "filed", URL: "http://issues/9"}}
	out := &bytes.Buffer{}

	err := clidriver.New(out, nil).WithGitHub(fake.wire()).Run(context.Background(),
		[]string{"report-failure", "--repo", "o/r", "--title", "intake is failing", "--body", "run 1 failed"})
	require.NoError(t, err)

	assert.Equal(t, []string{"o/r", "intake is failing", "run 1 failed"}, fake.announceArgs)
	assert.Contains(t, out.String(), "http://issues/9")

	// Actions injects GITHUB_TOKEN into every job, so the ordinary case needs
	// no flag and no secret anybody has to remember to seal.
	assert.Equal(t, clidriver.DefaultTokenEnv, fake.tokenEnv)
}

func TestTheGitHubVerbsSayWhenNothingIsWired(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"release"}, {"report-failure"}} {
		err := clidriver.New(&bytes.Buffer{}, nil).Run(context.Background(), args)
		require.ErrorIsf(t, err, clidriver.ErrNoGit, "%v", args)
	}
}

func TestTheGitHubVerbsRejectAnUnknownFlag(t *testing.T) {
	t.Parallel()

	fake := &fakeGitHub{}

	for _, args := range [][]string{{"release", "--nope"}, {"report-failure", "--nope"}} {
		err := clidriver.New(&bytes.Buffer{}, nil).WithGitHub(fake.wire()).
			Run(context.Background(), args)
		require.Errorf(t, err, "%v", args)
	}
}
