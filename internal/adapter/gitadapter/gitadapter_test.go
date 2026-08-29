package gitadapter_test

import (
	"context"
	"errors"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/execadaptermock"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

func ok(stdout string) execadapter.Result { return execadapter.Result{Stdout: stdout} }

func TestInitCreatesAMainBranch(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "/repo", "git", "init", "-b", "main").Return(ok(""), nil).Once()

	require.NoError(t, gitadapter.New(r).Init(context.Background(), "/repo"))
}

func TestCloneRunsFromNowhereInParticular(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "", "git", "clone", "git@x:y.git", "/repo").Return(ok(""), nil).Once()

	require.NoError(t, gitadapter.New(r).Clone(context.Background(), "git@x:y.git", "/repo"))
}

func TestIsRepoReadsTheExitCode(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "/repo", "git", "rev-parse", "--git-dir").Return(ok(".git"), nil).Once()

	yes, err := gitadapter.New(r).IsRepo(context.Background(), "/repo")
	require.NoError(t, err)
	require.True(t, yes)

	r2 := execadaptermock.NewMockRunner(t)
	r2.EXPECT().Run(mock.Anything, "/x", "git", "rev-parse", "--git-dir").
		Return(execadapter.Result{ExitCode: 128}, nil).Once()

	no, err := gitadapter.New(r2).IsRepo(context.Background(), "/x")
	require.NoError(t, err)
	require.False(t, no)
}

func TestIsRepoReportsABrokenRunner(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, mock.Anything, "git", "rev-parse", "--git-dir").
		Return(execadapter.Result{}, errBoom).Once()

	_, err := gitadapter.New(r).IsRepo(context.Background(), "/repo")
	require.ErrorIs(t, err, errBoom)
}

func TestHeadSHAIsTrimmed(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "/repo", "git", "rev-parse", "HEAD").Return(ok("abc123\n"), nil).Once()

	sha, err := gitadapter.New(r).HeadSHA(context.Background(), "/repo")
	require.NoError(t, err)
	require.Equal(t, "abc123", sha)
}

func TestRemoteSHATakesTheFirstField(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "", "git", "ls-remote", "git@x:y.git", "refs/heads/main").
		Return(ok("abc123\trefs/heads/main\n"), nil).Once()

	sha, err := gitadapter.New(r).RemoteSHA(context.Background(), "git@x:y.git", "")
	require.NoError(t, err)
	require.Equal(t, "abc123", sha)
}

func TestRemoteSHAWithNoSuchRef(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "", "git", "ls-remote", "git@x:y.git", "refs/heads/nope").
		Return(ok(""), nil).Once()

	_, err := gitadapter.New(r).RemoteSHA(context.Background(), "git@x:y.git", "nope")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no such ref")
}

func TestAddAndCommit(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "/repo", "git", "add", ".").Return(ok(""), nil).Once()
	r.EXPECT().Run(mock.Anything, "/repo", "git", "commit", "-m", "ci: x").Return(ok(""), nil).Once()

	g := gitadapter.New(r)
	require.NoError(t, g.Add(context.Background(), "/repo", "."))
	require.NoError(t, g.Commit(context.Background(), "/repo", "ci: x"))
}

// A state repo whose .gitignore carries an unanchored output pattern (build/
// instead of /build/) swallows record paths like runs/<rev>/build/x.json.
// The refusal must name the fix, not read like a broken engine. Live case:
// forge-self-state blocked every run record this way.
func TestAnIgnoredRecordPathNamesTheGitignoreFix(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "/state", "git", "add", "runs/abc/build/default.json").
		Return(execadapter.Result{
			ExitCode: 1,
			Stderr:   "The following paths are ignored by one of your .gitignore files:\nruns/abc/build\n",
		}, nil).Once()

	err := gitadapter.New(r).Add(context.Background(), "/state", "runs/abc/build/default.json")
	require.Error(t, err)
	require.Contains(t, err.Error(), "anchor output patterns to the repo root")
	require.Contains(t, err.Error(), "/build/, not build/")
}

func TestANonZeroGitExitCarriesStderr(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "/repo", "git", "rev-parse", "HEAD").
		Return(execadapter.Result{ExitCode: 128, Stderr: "fatal: no commits yet\n"}, nil).Once()

	_, err := gitadapter.New(r).HeadSHA(context.Background(), "/repo")
	require.Error(t, err)
	require.Contains(t, err.Error(), "reading HEAD of /repo")
	require.Contains(t, err.Error(), "fatal: no commits yet")
}

func TestABrokenRunnerIsWrappedWithWhatWasAttempted(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, mock.Anything, "git", "init", "-b", "main").
		Return(execadapter.Result{}, errBoom).Once()

	err := gitadapter.New(r).Init(context.Background(), "/repo")
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), "initialising /repo")
}

func TestACleanWorktreeHashesToNothing(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "/repo", "git", "status", "--porcelain").Return(ok("\n"), nil).Once()

	got, err := gitadapter.New(r).WorktreeHash(context.Background(), "/repo")
	require.NoError(t, err)
	require.Empty(t, got)
}

func TestADirtyWorktreeHashesItsContent(t *testing.T) {
	build := func(status, diff string) string {
		r := execadaptermock.NewMockRunner(t)
		r.EXPECT().Run(mock.Anything, "/repo", "git", "status", "--porcelain").Return(ok(status), nil).Once()
		r.EXPECT().Run(mock.Anything, "/repo", "git", "diff", "HEAD").Return(ok(diff), nil).Once()

		got, err := gitadapter.New(r).WorktreeHash(context.Background(), "/repo")
		require.NoError(t, err)
		require.Len(t, got, 12)

		return got
	}

	one := build(" M a.go\n", "-old\n+new\n")
	same := build(" M a.go\n", "-old\n+new\n")
	other := build(" M a.go\n", "-old\n+newer\n")

	require.Equal(t, one, same, "the same edit gives the same revision")
	require.NotEqual(t, one, other, "a different edit gives a different revision")
}

func TestWorktreeHashReportsBothFailures(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "/repo", "git", "status", "--porcelain").
		Return(execadapter.Result{ExitCode: 128, Stderr: "fatal\n"}, nil).Once()

	_, err := gitadapter.New(r).WorktreeHash(context.Background(), "/repo")
	require.Error(t, err)

	r2 := execadaptermock.NewMockRunner(t)
	r2.EXPECT().Run(mock.Anything, "/repo", "git", "status", "--porcelain").Return(ok(" M a.go\n"), nil).Once()
	r2.EXPECT().Run(mock.Anything, "/repo", "git", "diff", "HEAD").
		Return(execadapter.Result{ExitCode: 128, Stderr: "fatal\n"}, nil).Once()

	_, err = gitadapter.New(r2).WorktreeHash(context.Background(), "/repo")
	require.Error(t, err)
	require.Contains(t, err.Error(), "reading uncommitted changes in /repo")
}

func TestStagedIsTrueWhenTheIndexHoldsAnything(t *testing.T) {
	t.Parallel()

	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "/repo", "git", "diff", "--cached", "--name-only").
		Return(execadapter.Result{Stdout: "revisions/abc.json\n"}, nil)

	staged, err := gitadapter.New(r).Staged(context.Background(), "/repo")
	require.NoError(t, err)
	require.True(t, staged)

	r2 := execadaptermock.NewMockRunner(t)
	r2.EXPECT().Run(mock.Anything, "/repo", "git", "diff", "--cached", "--name-only").
		Return(execadapter.Result{Stdout: "\n"}, nil)

	clean, err := gitadapter.New(r2).Staged(context.Background(), "/repo")
	require.NoError(t, err)
	require.False(t, clean)
}

// git sorts every tag a repo carries, and a repo that predates this pipeline
// carries tags this pipeline cannot read.
func TestLatestTagSkipsWhatTheVersionRuleCannotRead(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "/w/a", "git", "tag", "--sort=-v:refname", "--list", "v[0-9]*").
		Return(ok("v1.2\nwip\nv0.44.4\nv0.44.3\n"), nil).Once()

	got, err := gitadapter.New(r).LatestTag(context.Background(), "/w/a", "")
	require.NoError(t, err)
	require.Equal(t, "v0.44.4", got)
}

// A prefix is a namespace. Reading another prefix's tag as this line's
// previous version is how two factories sharing one repo take turns
// overwriting each other, so the prefix is stripped and a tag without it is
// not this line's history.
func TestLatestTagReadsOnlyItsOwnPrefix(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "/w/a", "git", "tag", "--sort=-v:refname", "--list", "forge-v[0-9]*").
		Return(ok("forge-v0.50.0\nforge-v0.49.0\n"), nil).Once()

	got, err := gitadapter.New(r).LatestTag(context.Background(), "/w/a", "forge")
	require.NoError(t, err)
	require.Equal(t, "v0.50.0", got, "the prefix is a namespace, not part of the version")
}

// SubjectsSince is what the semantic strategy reads. A tag the repo does not
// carry answers the whole history, because a member joining a line that
// already exists brings all of itself with it.
func TestSubjectsSinceReadsTheWholeHistoryWhenTheTagIsUnknown(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "/w/a", "git", "tag", "--list", "v0.49.0").
		Return(ok(""), nil).Once()
	r.EXPECT().Run(mock.Anything, "/w/a", "git", "log", "--no-merges", "--format=%s").
		Return(ok("feat: one\nfix: two\n"), nil).Once()

	got, err := gitadapter.New(r).SubjectsSince(context.Background(), "/w/a", "v0.49.0")
	require.NoError(t, err)
	require.Equal(t, []string{"feat: one", "fix: two"}, got)
}

func TestSubjectsSinceReadsTheRangeWhenTheTagIsKnown(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "/w/a", "git", "tag", "--list", "v0.49.0").
		Return(ok("v0.49.0\n"), nil).Once()
	r.EXPECT().Run(mock.Anything, "/w/a", "git", "log", "--no-merges", "--format=%s", "v0.49.0..HEAD").
		Return(ok("feat: only the new one\n"), nil).Once()

	got, err := gitadapter.New(r).SubjectsSince(context.Background(), "/w/a", "v0.49.0")
	require.NoError(t, err)
	require.Equal(t, []string{"feat: only the new one"}, got)
}
