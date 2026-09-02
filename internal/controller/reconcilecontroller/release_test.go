package reconcilecontroller_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactcontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/reconcilecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/gitadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// A released revision is released once. golden run 19 reran a revision
// already out as v0.5.6, derived v0.5.7 from the tag line, and tagged six
// members before meeting a file the fresh runner never built. The record
// is what stops the second derivation: the same revision answers the same
// number and nothing is published again.
func TestARerunOfAReleasedRevisionReusesItsVersion(t *testing.T) {
	f := newFakeEngines(t)
	c := reconcilecontroller.New(f.caller(), gitAt(t, "abc123", "v0.1.9"), clock())
	p := pipeline(releasingStage("build", substage("default", []string{"build"})))

	first, err := c.Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.Equal(t, "v0.1.10", first.Version)
	require.Len(t, f.published, 1)

	// The line moved on: the tag home now answers the version the first
	// apply released, so a derivation would say v0.1.11.
	second, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc123", "v0.1.10"), clock()).
		Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.True(t, second.Skipped)
	require.Equal(t, reconcilecontroller.IntentSkip, second.Intent)
	require.Equal(t, "v0.1.10", second.Version, "the recorded number, never a bump")
	require.Contains(t, second.Reason, "already released as v0.1.10")
	require.Empty(t, second.Stages, "nothing runs for a revision that was already released")
	require.Len(t, f.published, 1, "the artifact engine is never called again")
}

// A push to a repo outside the release set makes a new revision and
// changes nothing a release tags. Same shas in the set as the last release
// means the same release, so the run converges on it with no version
// derived and no engine called.
func TestAnUnchangedReleaseSetConvergesWithoutTheEngine(t *testing.T) {
	f := newFakeEngines(t)

	s := releasingStage("build", substage("default", []string{"build"}))
	s.ReleaseRepos = []string{"golden-rust"}

	p := pipeline(s)
	p.Repos = append(p.Repos, config.Repo{Name: "golden-go", URL: "https://example.com/golden-go"})

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc123", "v0.1.9"), clock()).
		Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.Len(t, f.published, 1)

	// golden-go moved, golden-rust did not. The revision is new; the set is
	// not.
	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HeadSHA(mock.Anything, "/work/golden-go").Return("def456", nil).Maybe()
	git.EXPECT().HeadSHA(mock.Anything, mock.Anything).Return("abc123", nil).Maybe()
	git.EXPECT().WorktreeHash(mock.Anything, mock.Anything).Return("", nil).Maybe()
	git.EXPECT().LatestTag(mock.Anything, mock.Anything, mock.Anything).Return("v0.1.10", nil).Maybe()
	git.EXPECT().TreeHash(mock.Anything, mock.Anything).Return("tree", nil).Maybe()

	second, err := reconcilecontroller.New(f.caller(), git, clock()).Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.True(t, second.Skipped)
	require.Contains(t, second.Reason, "release set is unchanged since v0.1.10")
	require.Len(t, f.published, 1)
}

// A range of commits the vocabulary ignores releases nothing, and it does
// so before anything builds: the run is skipped, not proven and released as
// a patch nobody asked for.
func TestAnIgnoredOnlyRangeSkipsBeforeAnyBuild(t *testing.T) {
	f := newFakeEngines(t)

	git := gitAt(t, "abc123", "v0.2.4")
	git.EXPECT().SubjectsSince(mock.Anything, mock.Anything, "v0.2.4").
		Return([]string{"docs: how to open the door", "chore: tidy"}, nil).Maybe()

	p := pipeline(releasingStage("build", substage("default", []string{"build"})))
	p.Versioning.Strategy = config.StrategySemantic
	p.Versioning.Semantic = config.Semantic{Patch: []string{"fix:"}, Ignore: []string{"docs:", "chore:"}}

	report, err := reconcilecontroller.New(f.caller(), git, clock()).Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.True(t, report.Skipped)
	require.Contains(t, report.Reason, "nothing releasable since v0.2.4")
	require.Empty(t, report.Stages)
	require.Empty(t, f.published)

	for _, call := range f.calls {
		require.NotEqual(t, "run", call.Tool, "a skipped run builds nothing")
	}
}

// unmatched: error makes the vocabulary a rule. A subject outside it fails
// the run before anything builds, naming the subject.
func TestAnUnclaimedSubjectFailsBeforeAnyBuild(t *testing.T) {
	f := newFakeEngines(t)

	git := gitAt(t, "abc123", "v0.2.4")
	git.EXPECT().SubjectsSince(mock.Anything, mock.Anything, "v0.2.4").
		Return([]string{"fix: fine", "yolo pushed from the train"}, nil).Maybe()

	p := pipeline(releasingStage("build", substage("default", []string{"build"})))
	p.Versioning.Strategy = config.StrategySemantic
	p.Versioning.Semantic = config.Semantic{Patch: []string{"fix:"}, Unmatched: config.UnmatchedError}

	_, err := reconcilecontroller.New(f.caller(), git, clock()).Apply(context.Background(), p, "/work", plain)
	require.ErrorIs(t, err, reconcilecontroller.ErrUnclaimedSubject)
	require.Contains(t, err.Error(), "yolo pushed from the train")
	require.Empty(t, f.published)

	for _, call := range f.calls {
		require.NotEqual(t, "run", call.Tool, "a refused run builds nothing")
	}
}

// One apply cut into phases, each in its own process, reaches the same
// release the whole apply reaches: the intent phase records its decision,
// the stages phase builds under that number and releases nothing, and the
// release phase releases under it.
func TestAPhasedApplyCarriesTheDecisionThroughState(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(releasingStage("build", substage("default", []string{"build"})))

	phase := func(name string) reconcilecontroller.Report {
		t.Helper()

		report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc123", "v0.1.9"), clock()).
			Apply(context.Background(), p, "/work", reconcilecontroller.Options{Phase: name})
		require.NoError(t, err)

		return report
	}

	reconcile := phase(reconcilecontroller.PhaseReconcile)
	require.Empty(t, reconcile.Revision.ID, "the reconcile phase resolves no revision")

	intent := phase(reconcilecontroller.PhaseIntent)
	require.Equal(t, reconcilecontroller.IntentProceed, intent.Intent)
	require.Equal(t, "v0.1.10", intent.Version)
	require.Empty(t, intent.Stages)

	stages := phase(reconcilecontroller.PhaseStages)
	require.Len(t, stages.Stages, 1)
	require.True(t, stages.Minted)
	require.Empty(t, stages.Released, "the stages phase releases nothing")
	require.Empty(t, f.published)

	release := phase(reconcilecontroller.PhaseRelease)
	require.Len(t, release.Released, 1)
	require.Equal(t, 1, release.Stages[0].Reused, "the release phase reuses what the stages phase proved")
	require.Len(t, f.published, 1)
	require.Equal(t, "v0.1.10", f.published[0].Version)
}

// A stages or release phase with no intent recorded is a phase run out of
// order, and it says so rather than deriving a number of its own.
func TestAPhaseWithoutAnIntentRefuses(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(releasingStage("build", substage("default", []string{"build"})))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc123", "v0.1.9"), clock()).
		Apply(context.Background(), p, "/work", reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages})
	require.ErrorIs(t, err, reconcilecontroller.ErrNoIntent)
}

// The last check before a release writes anything: the bytes. A build that
// produced, name for name, the digests the previous release shipped is that
// release, so the revision is recorded under the previous number and the
// engine is never called. The stamps in real binaries make this rare; the
// record makes it cheap to keep asking.
func TestIdenticalBytesConvergeOnThePreviousRelease(t *testing.T) {
	root := t.TempDir()
	rel := filepath.Join("golden-rust", "build", "dist", "tool_linux_amd64")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "golden-rust", "build", "dist"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte("same bytes"), 0o600))

	digest, _, err := fsadapter.Digest(filepath.Join(root, rel))
	require.NoError(t, err)

	index, err := artifactcontroller.BuildIndex("prev", "", artifactcontroller.Release{Tag: "v0.1.10"},
		[]artifactcontroller.UploadDigest{{Path: rel, Digest: digest, Size: 10}})
	require.NoError(t, err)

	f := newFakeEngines(t)
	f.index = string(index)
	f.runOutputs["build/default"] = citypes.RunOutput{
		Status: citypes.StatusPassed,
		Forge:  &citypes.ForgeResult{Artifacts: []forge.Artifact{{Name: "tool", Type: "binary", Location: rel}}},
	}

	p := pipeline(releasingStage("build", substage("default", []string{"build"})))

	first, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc123", "v0.1.9"), clock()).
		Apply(context.Background(), p, root, plain)
	require.NoError(t, err)
	require.Equal(t, "v0.1.10", first.Version)
	require.Len(t, f.published, 1)

	// A new commit that changed nothing the build reads: new revision,
	// same bytes.
	git := gitAt(t, "def456", "v0.1.10")

	second, err := reconcilecontroller.New(f.caller(), git, clock()).Apply(context.Background(), p, root, plain)
	require.NoError(t, err)
	require.False(t, second.Skipped, "the build runs; only the release converges")
	require.Len(t, second.Released, 1)
	require.False(t, second.Released[0].Published)
	require.Contains(t, second.Released[0].Reason, "byte for byte what v0.1.10 shipped")
	require.Len(t, f.published, 1, "the engine is never called for identical bytes")

	// And the record now answers for this revision too.
	third, err := reconcilecontroller.New(f.caller(), gitAt(t, "def456", "v0.1.10"), clock()).
		Apply(context.Background(), p, root, plain)
	require.NoError(t, err)
	require.True(t, third.Skipped)
	require.Equal(t, "v0.1.10", third.Version)
}
