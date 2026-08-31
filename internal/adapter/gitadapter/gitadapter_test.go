package gitadapter_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	r.EXPECT().Run(mock.Anything, "/repo", "git", "var", "GIT_COMMITTER_IDENT").
		Return(ok("A Dev <dev@example.com> 1 +0000\n"), nil).Once()
	r.EXPECT().Run(mock.Anything, "/repo", "git", "commit", "-m", "ci: x").Return(ok(""), nil).Once()

	g := gitadapter.New(r)
	require.NoError(t, g.Add(context.Background(), "/repo", "."))
	require.NoError(t, g.Commit(context.Background(), "/repo", "ci: x"))
}

// A host that already names a committer keeps it, so a local run stays
// attributed to whoever ran it rather than to the engine.
func TestCommitLeavesAnExistingIdentityAlone(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "/repo", "git", "var", "GIT_COMMITTER_IDENT").
		Return(ok("A Dev <dev@example.com> 1 +0000\n"), nil).Once()
	r.EXPECT().Run(mock.Anything, "/repo", "git", "commit", "-m", "ci: x").
		Return(ok(""), nil).Once()

	require.NoError(t, gitadapter.New(r).Commit(context.Background(), "/repo", "ci: x"))
}

// A runner has no identity and git exits 128 on "empty ident name". The
// engine supplies its own rather than failing, and turns signing off with it:
// a host with no identity has no signing key either.
func TestCommitSuppliesAnIdentityWhenTheHostHasNone(t *testing.T) {
	r := execadaptermock.NewMockRunner(t)
	r.EXPECT().Run(mock.Anything, "/repo", "git", "var", "GIT_COMMITTER_IDENT").
		Return(execadapter.Result{ExitCode: 128, Stderr: "fatal: empty ident name"}, nil).Once()
	r.EXPECT().Run(mock.Anything, "/repo", "git",
		"-c", "user.name=Alexandre Mahdhaoui",
		"-c", "user.email=alexandre.mahdhaoui@gmail.com",
		"-c", "commit.gpgsign=false",
		"-c", "tag.gpgsign=false",
		"commit", "-m", "ci: x").
		Return(ok(""), nil).Once()

	require.NoError(t, gitadapter.New(r).Commit(context.Background(), "/repo", "ci: x"))
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

// The test that would have caught this, and the only shape that can. Every
// mocked case above passes against a broken Commit, because the thing that
// failed was real git reading real config. This runs real git with no
// ambient identity anywhere - no global config, no system config, an empty
// HOME - which is exactly the state a CI runner is in.
//
// Live case: seven consecutive forge-self pipeline runs died here with
// "fatal: empty ident name", while every developer machine passed.
func TestCommitWorksOnAHostWithNoGitIdentityAtAll(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	// Unset, not empty. A runner leaves these absent, and that distinction
	// decides the fix: git reads an empty GIT_COMMITTER_NAME as an identity
	// of "", and the environment beats -c, so an empty one cannot be
	// overridden while an absent one can. t.Setenv registers the restore.
	for _, k := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
	} {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}

	g := gitadapter.New(execadapter.New())
	ctx := context.Background()

	run := func(args ...string) {
		t.Helper()

		res, err := execadapter.New().Run(ctx, dir, "git", args...)
		require.NoError(t, err)
		require.Zero(t, res.ExitCode, "git %v: %s", args, res.Stderr)
	}

	run("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "record.json"), []byte("{}\n"), 0o600))
	require.NoError(t, g.Add(ctx, dir, "record.json"))

	// Prove the premise before proving the fix: a bare commit here fails.
	bare, err := execadapter.New().Run(ctx, dir, "git", "commit", "-m", "bare")
	require.NoError(t, err)
	require.NotZero(t, bare.ExitCode, "a bare commit must fail with no identity, or this test proves nothing")
	require.Contains(t, bare.Stderr, "ident")

	require.NoError(t, g.Commit(ctx, dir, "ci: record"))

	sha, err := g.HeadSHA(ctx, dir)
	require.NoError(t, err)
	require.NotEmpty(t, sha)

	author, err := execadapter.New().Run(ctx, dir, "git", "log", "-1", "--format=%an <%ae>")
	require.NoError(t, err)
	require.Zero(t, author.ExitCode, author.Stderr)
	require.Contains(t, author.Stdout, "alexandre.mahdhaoui@gmail.com")
}

// stripIdentity puts the process in the state a CI runner is in: no global
// config, no system config, an empty HOME, and no identity variables.
//
// Unset, not empty. That distinction is the whole fix: git reads an empty
// GIT_COMMITTER_NAME as an identity of "", and the environment beats -c, so
// an empty one cannot be overridden while an absent one can. t.Setenv
// registers the restore before the unset.
func stripIdentity(t *testing.T) {
	t.Helper()

	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)

	for _, k := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
	} {
		t.Setenv(k, "")
		require.NoError(t, os.Unsetenv(k))
	}
}

// An annotated tag is an object git writes, so it needs a committer identity
// exactly as a commit does. This shipped as a second instance of the same
// defect after the commit one was fixed, in a different adapter, and it was
// the release step of a run that had already passed every stage.
//
// Real git again, for the same reason: a mock cannot fail on empty ident.
func TestTagWorksOnAHostWithNoGitIdentityAtAll(t *testing.T) {
	dir := t.TempDir()

	stripIdentity(t)

	g := gitadapter.New(execadapter.New())
	ctx := context.Background()
	exec := execadapter.New()

	run := func(args ...string) {
		t.Helper()

		res, err := exec.Run(ctx, dir, "git", args...)
		require.NoError(t, err)
		require.Zero(t, res.ExitCode, "git %v: %s", args, res.Stderr)
	}

	run("init", "-q")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-file"), []byte("x\n"), 0o600))
	require.NoError(t, g.Add(ctx, dir, "a-file"))
	require.NoError(t, g.Commit(ctx, dir, "first"))

	sha, err := g.HeadSHA(ctx, dir)
	require.NoError(t, err)

	// Absent is not an error. It is what a first release of a member looks
	// like, and a release that treated it as one would never publish.
	at, found, err := g.TagAt(ctx, dir, "v0.2.0")
	require.NoError(t, err)
	require.False(t, found)
	require.Empty(t, at)

	// Prove the premise: a bare annotated tag here fails.
	bare, err := exec.Run(ctx, dir, "git", "tag", "-m", "bare", "bare", sha)
	require.NoError(t, err)
	require.NotZero(t, bare.ExitCode, "a bare annotated tag must fail with no identity, or this test proves nothing")

	// No origin here, so the push half fails and the tag half must already
	// have run: what is under test is that git wrote the object at all.
	_ = g.Tag(ctx, dir, "v0.2.0", sha)

	at, found, err = g.TagAt(ctx, dir, "v0.2.0")
	require.NoError(t, err)
	require.True(t, found, "the annotated tag must exist; a missing identity is what stopped it before")

	// rev-list resolves the tag OBJECT to the commit it points at. Without
	// that step an annotated tag answers its own sha, and the release then
	// reads every already-tagged member as pointing somewhere else and
	// refuses to publish.
	require.Equal(t, sha, at)

	tagger, err := exec.Run(ctx, dir, "git", "for-each-ref", "--format=%(taggeremail)", "refs/tags/v0.2.0")
	require.NoError(t, err)
	require.Zero(t, tagger.ExitCode, tagger.Stderr)
	require.Contains(t, tagger.Stdout, "alexandre.mahdhaoui@gmail.com")
}

// A tag is never moved, because moving it changes what a consumer already
// pinned.
func TestTagRefusesToMoveOne(t *testing.T) {
	dir := t.TempDir()

	stripIdentity(t)

	g := gitadapter.New(execadapter.New())
	ctx := context.Background()

	res, err := execadapter.New().Run(ctx, dir, "git", "init", "-q")
	require.NoError(t, err)
	require.Zero(t, res.ExitCode)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-file"), []byte("x\n"), 0o600))
	require.NoError(t, g.Add(ctx, dir, "a-file"))
	require.NoError(t, g.Commit(ctx, dir, "first"))

	sha, err := g.HeadSHA(ctx, dir)
	require.NoError(t, err)

	require.NoError(t, g.Tag(ctx, dir, "v0.2.0", sha))

	// The same sha is convergence, not a move: refusing it is what made a
	// re-run of an already-tagged member terminal instead of idempotent.
	require.NoError(t, g.Tag(ctx, dir, "v0.2.0", sha))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-file"), []byte("y\n"), 0o600))
	require.NoError(t, g.Add(ctx, dir, "a-file"))
	require.NoError(t, g.Commit(ctx, dir, "second"))

	moved, err := g.HeadSHA(ctx, dir)
	require.NoError(t, err)

	err = g.Tag(ctx, dir, "v0.2.0", moved)
	require.Error(t, err)
	require.Contains(t, err.Error(), "never moved")
}

// tagFixture stands up a checkout with one commit and a bare origin, which is
// the shape every release actually runs against: the remote exists and is the
// only party that knows what was already published.
func tagFixture(t *testing.T) (g *gitadapter.CLI, dir, sha string) {
	t.Helper()

	stripIdentity(t)

	ctx := context.Background()
	g = gitadapter.New(execadapter.New())
	exec := execadapter.New()
	dir = filepath.Join(t.TempDir(), "checkout")
	bare := filepath.Join(t.TempDir(), "origin.git")

	run := func(wd string, args ...string) {
		t.Helper()

		res, err := exec.Run(ctx, wd, "git", args...)
		require.NoError(t, err)
		require.Zero(t, res.ExitCode, "git %v: %s", args, res.Stderr)
	}

	run("", "init", "-q", "--bare", bare)
	run("", "init", "-q", "-b", "main", dir)
	run(dir, "remote", "add", "origin", bare)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-file"), []byte("x\n"), 0o600))
	require.NoError(t, g.Add(ctx, dir, "a-file"))
	require.NoError(t, g.Commit(ctx, dir, "first"))
	run(dir, "push", "-q", "origin", "main")

	sha, err := g.HeadSHA(ctx, dir)
	require.NoError(t, err)

	return g, dir, sha
}

// The defect this pins: run 35 published v0.45.9, run 36 was a fresh clone
// whose local tag list said "absent", so it re-created the tag and the push
// was rejected by the only party that knew. The remote is the authority, and
// a tag already there at the same commit is convergence, not work.
func TestATagOnTheRemoteButNotInTheCheckoutConverges(t *testing.T) {
	g, dir, sha := tagFixture(t)
	ctx := context.Background()

	require.NoError(t, g.Tag(ctx, dir, "v0.9.0", sha))

	// A second checkout of the same origin: no local tags, like every CI
	// re-run. The old code re-created the tag here and died on the push.
	fresh := filepath.Join(t.TempDir(), "fresh")

	urlRes, err := execadapter.New().Run(ctx, dir, "git", "remote", "get-url", "origin")
	require.NoError(t, err)
	origin := strings.TrimSpace(urlRes.Stdout)

	cloneRes, err := execadapter.New().Run(ctx, "", "git", "clone", "-q", "--no-tags", origin, fresh)
	require.NoError(t, err)
	require.Zero(t, cloneRes.ExitCode, cloneRes.Stderr)

	_, found, err := g.TagAt(ctx, fresh, "v0.9.0")
	require.NoError(t, err)
	require.False(t, found, "the fixture must have no local tag, or this test proves nothing")

	require.NoError(t, g.Tag(ctx, fresh, "v0.9.0", sha))

	at, found, err := g.RemoteTagAt(ctx, fresh, "v0.9.0")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, sha, at)
}

// A remote tag at another commit refuses loudly: moving it would change what
// a consumer already pinned, and no fresh clone may decide that.
func TestARemoteTagAtAnotherCommitRefuses(t *testing.T) {
	g, dir, sha := tagFixture(t)
	ctx := context.Background()

	require.NoError(t, g.Tag(ctx, dir, "v0.9.0", sha))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-file"), []byte("y\n"), 0o600))
	require.NoError(t, g.Add(ctx, dir, "a-file"))
	require.NoError(t, g.Commit(ctx, dir, "second"))

	moved, err := g.HeadSHA(ctx, dir)
	require.NoError(t, err)

	// Delete the local tag so only origin's copy can refuse - that is the
	// CI shape, and the refusal must name origin as the authority.
	res, err := execadapter.New().Run(ctx, dir, "git", "tag", "-d", "v0.9.0")
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, res.Stderr)

	err = g.Tag(ctx, dir, "v0.9.0", moved)
	require.Error(t, err)
	require.Contains(t, err.Error(), "never moved")
	require.Contains(t, err.Error(), "origin")
}

// An unreadable remote is an error, never "absent". Treating it as absent
// would re-create a tag the remote may hold, which is the exact mistake
// reading the remote exists to remove.
func TestAnUnreadableRemoteIsAnErrorNotAbsent(t *testing.T) {
	g, dir, sha := tagFixture(t)
	ctx := context.Background()

	res, err := execadapter.New().Run(ctx, dir, "git", "remote", "set-url", "origin",
		filepath.Join(t.TempDir(), "gone.git"))
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, res.Stderr)

	_, _, err = g.RemoteTagAt(ctx, dir, "v0.9.0")
	require.Error(t, err)

	err = g.Tag(ctx, dir, "v0.9.0", sha)
	require.Error(t, err)

	_, found, tagErr := g.TagAt(ctx, dir, "v0.9.0")
	require.NoError(t, tagErr)
	require.False(t, found, "an unreadable remote must stop the tag before it is created")
}

// A local tag left behind by an interrupted run - tagged, then the push was
// lost - is finished by the next run, not refused: the remote lacks it, the
// checkout has it at the right commit, and the push is the remaining half.
func TestALeftoverLocalTagIsPushedNotRefused(t *testing.T) {
	g, dir, sha := tagFixture(t)
	ctx := context.Background()
	exec := execadapter.New()

	res, err := exec.Run(ctx, dir, "git",
		"-c", "user.name=t", "-c", "user.email=t@t", "tag", "-m", "v0.9.0", "v0.9.0", sha)
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, res.Stderr)

	_, found, err := g.RemoteTagAt(ctx, dir, "v0.9.0")
	require.NoError(t, err)
	require.False(t, found, "the fixture's origin must lack the tag, or this proves nothing")

	require.NoError(t, g.Tag(ctx, dir, "v0.9.0", sha))

	at, found, err := g.RemoteTagAt(ctx, dir, "v0.9.0")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, sha, at)
}

// RemoteTagAt must answer the commit an annotated tag wraps, not the tag
// object's own sha - the same peel TagAt does with rev-list, done remotely
// with the ^{} ref. Without it every already-published member reads as
// pointing somewhere else and the release refuses to converge.
func TestRemoteTagAtPeelsAnAnnotatedTag(t *testing.T) {
	g, dir, sha := tagFixture(t)
	ctx := context.Background()

	require.NoError(t, g.Tag(ctx, dir, "v0.9.0", sha))

	at, found, err := g.RemoteTagAt(ctx, dir, "v0.9.0")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, sha, at, "an annotated tag must answer the commit it wraps")
}

// A push the remote refuses is ErrRejected and not a generic failure. The
// remote moved under this run, and merging is somebody's decision, never the
// pipeline's, so the caller has to be able to tell this apart.
func TestPushSeparatesARejectionFromABreakage(t *testing.T) {
	stripIdentity(t)

	g := gitadapter.New(execadapter.New())
	ctx := context.Background()
	exec := execadapter.New()

	remote := filepath.Join(t.TempDir(), "origin.git")
	dir := t.TempDir()

	for _, args := range [][]string{
		{"init", "--bare", "-q", "-b", "main", remote},
	} {
		res, err := exec.Run(ctx, "", "git", args...)
		require.NoError(t, err)
		require.Zero(t, res.ExitCode, res.Stderr)
	}

	res, err := exec.Run(ctx, dir, "git", "init", "-q", "-b", "main")
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, res.Stderr)

	has, err := g.HasRemote(ctx, dir)
	require.NoError(t, err)
	require.False(t, has, "no origin yet, and that is not an error")

	res, err = exec.Run(ctx, dir, "git", "remote", "add", "origin", remote)
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, res.Stderr)

	has, err = g.HasRemote(ctx, dir)
	require.NoError(t, err)
	require.True(t, has)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a"), []byte("1\n"), 0o600))
	require.NoError(t, g.Add(ctx, dir, "a"))
	require.NoError(t, g.Commit(ctx, dir, "first"))

	branch, err := g.Branch(ctx, dir)
	require.NoError(t, err)
	require.Equal(t, "main", branch)

	require.NoError(t, g.Push(ctx, dir, branch))

	// Somebody else pushes, then this checkout tries to push a commit that
	// does not build on theirs.
	other := t.TempDir()
	res, err = exec.Run(ctx, "", "git", "clone", "-q", remote, other)
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, res.Stderr)

	require.NoError(t, os.WriteFile(filepath.Join(other, "b"), []byte("2\n"), 0o600))
	require.NoError(t, g.Add(ctx, other, "b"))
	require.NoError(t, g.Commit(ctx, other, "theirs"))
	require.NoError(t, g.Push(ctx, other, "main"))

	require.NoError(t, os.WriteFile(filepath.Join(dir, "c"), []byte("3\n"), 0o600))
	require.NoError(t, g.Add(ctx, dir, "c"))
	require.NoError(t, g.Commit(ctx, dir, "mine"))

	err = g.Push(ctx, dir, "main")
	require.ErrorIs(t, err, gitadapter.ErrRejected)
}

// A detached HEAD is what a CI checkout of a tag looks like. There is no
// branch to push, and inventing one would put the commit somewhere nobody
// named.
func TestBranchIsEmptyOnADetachedHead(t *testing.T) {
	stripIdentity(t)

	g := gitadapter.New(execadapter.New())
	ctx := context.Background()
	dir := t.TempDir()

	res, err := execadapter.New().Run(ctx, dir, "git", "init", "-q", "-b", "main")
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, res.Stderr)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "a"), []byte("1\n"), 0o600))
	require.NoError(t, g.Add(ctx, dir, "a"))
	require.NoError(t, g.Commit(ctx, dir, "first"))

	sha, err := g.HeadSHA(ctx, dir)
	require.NoError(t, err)

	res, err = execadapter.New().Run(ctx, dir, "git", "checkout", "-q", "--detach", sha)
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, res.Stderr)

	branch, err := g.Branch(ctx, dir)
	require.NoError(t, err)
	require.Empty(t, branch)
}
