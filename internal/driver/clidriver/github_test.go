package clidriver_test

import (
	"bytes"
	"context"
	"testing"

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
}

func (f *fakeGitHub) Publish(_ context.Context, dir, repo, tag, sha string) (releasecontroller.Report, error) {
	f.publishArgs = []string{dir, repo, tag, sha}

	return f.report, nil
}

func (f *fakeGitHub) wire() clidriver.GitHubFor {
	return func(tokenEnv, base string) clidriver.Publisher {
		f.tokenEnv, f.base = tokenEnv, base

		return f
	}
}

// release answers before any config is read. The workflow that calls it has a
// checkout and no forge-ci.yaml, so loading one would fail on a file that has
// no business existing there.
func TestReleaseNeedsNoPipelineFile(t *testing.T) {
	t.Parallel()

	fake := &fakeGitHub{}

	err := clidriver.New(&bytes.Buffer{}, nil).WithGitHub(fake.wire()).
		Run(context.Background(), []string{"release", "--repo", "o/r", "--tag", "v0.2.0", "--sha", "abc"})
	require.NoError(t, err)
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

func TestReleaseSaysWhenNothingIsWired(t *testing.T) {
	t.Parallel()

	err := clidriver.New(&bytes.Buffer{}, nil).Run(context.Background(), []string{"release"})
	require.ErrorIs(t, err, clidriver.ErrNoGit)
}

func TestReleaseRejectsAnUnknownFlag(t *testing.T) {
	t.Parallel()

	fake := &fakeGitHub{}

	err := clidriver.New(&bytes.Buffer{}, nil).WithGitHub(fake.wire()).
		Run(context.Background(), []string{"release", "--nope"})
	require.Error(t, err)
}
