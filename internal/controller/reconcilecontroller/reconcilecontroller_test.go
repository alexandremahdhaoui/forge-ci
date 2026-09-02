package reconcilecontroller_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/reconcilecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/gitadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

// noRepoGit is a git nobody asks a commit of. Every apply still derives a
// version, so a tag line must answer even when the pipeline has no repos.
func noRepoGit(t *testing.T) *gitadaptermock.MockGit {
	t.Helper()

	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().LatestTag(mock.Anything, mock.Anything, mock.Anything).Return("", nil).Maybe()

	return git
}

// gitAt is a checkout at one commit. previous is the version line the release
// reads back, and defaults to none, which is a workspace that never released.
func gitAt(t *testing.T, sha string, previous ...string) *gitadaptermock.MockGit {
	t.Helper()

	last := ""
	if len(previous) > 0 {
		last = previous[0]
	}

	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HeadSHA(mock.Anything, mock.Anything).Return(sha, nil).Maybe()
	git.EXPECT().WorktreeHash(mock.Anything, mock.Anything).Return("", nil).Maybe()
	git.EXPECT().LatestTag(mock.Anything, mock.Anything, mock.Anything).Return(last, nil).Maybe()
	git.EXPECT().TreeHash(mock.Anything, mock.Anything).Return("tree-"+sha, nil).Maybe()

	return git
}

func TestEveryStageRunsInOrderAndAdvances(t *testing.T) {
	f := newFakeEngines(t)
	c := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock())

	report, err := c.Apply(context.Background(), pipeline(
		stage("build", substage("default", []string{"build"})),
		stage("self", substage("default", []string{"self"})),
	), "/work", plain)
	require.NoError(t, err)
	require.True(t, report.Advanced())
	require.Len(t, report.Stages, 2)
	require.Equal(t, "build", report.Stages[0].Name)
	require.Equal(t, "self", report.Stages[1].Name)
	require.Equal(t, citypes.StatusPassed, report.Stages[0].Runs[0].Status)
}

func TestTheRevisionIsRecordedAndStable(t *testing.T) {
	f := newFakeEngines(t)
	c := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock())

	first, err := c.Apply(context.Background(), pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.NoError(t, err)
	require.NotEmpty(t, first.Revision.ID)
	require.Equal(t, "abc123", first.Revision.Repos["golden-rust"])
	require.Contains(t, f.store, "revision/"+first.Revision.ID)

	second, err := c.Apply(context.Background(), pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.NoError(t, err)
	require.Equal(t, first.Revision.ID, second.Revision.ID)
}

func TestANewCommitIsANewRevision(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(stage("build", substage("default", []string{"build"})))

	before, err := reconcilecontroller.New(f.caller(), gitAt(t, "aaa"), clock()).
		Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)

	after, err := reconcilecontroller.New(f.caller(), gitAt(t, "bbb"), clock()).
		Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.NotEqual(t, before.Revision.ID, after.Revision.ID)
}

func TestAPassedSubstageIsNotRunAgain(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(stage("build", substage("default", []string{"build"})))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).
		Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.Equal(t, 1, f.counted(call{uriCompute, "run"}))

	_, err = reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).
		Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.Equal(t, 1, f.counted(call{uriCompute, "run"}))
}

func TestAFailedSubstageStopsTheRestOfThePipeline(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["build/default"] = citypes.RunOutput{Status: citypes.StatusFailed, Message: "two tests failed"}

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), pipeline(
		stage("build", substage("default", []string{"build"})),
		stage("self", substage("default", []string{"self"})),
	), "/work", plain)
	require.NoError(t, err)
	require.False(t, report.Advanced())
	require.Len(t, report.Stages, 1)
	require.Equal(t, citypes.StatusFailed, report.Stages[0].Runs[0].Status)
	require.Equal(t, "two tests failed", report.Stages[0].Runs[0].Message)
}

func TestAFailedSubstageIsRetriedOnTheNextApply(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["build/default"] = citypes.RunOutput{Status: citypes.StatusFailed}

	p := pipeline(stage("build", substage("default", []string{"build"})))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)

	delete(f.runOutputs, "build/default")

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.True(t, report.Advanced())
	require.Equal(t, 2, f.counted(call{uriCompute, "run"}))
}

func TestGatesRunAfterTheSubstageAndAreRecorded(t *testing.T) {
	f := newFakeEngines(t)

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("prod", substage("eu-a", []string{"build"}, "approve"))), "/work", plain)
	require.NoError(t, err)
	require.Len(t, report.Stages[0].Runs[0].Gates, 1)
	require.Equal(t, "approve", report.Stages[0].Runs[0].Gates[0].Alias)
	require.Equal(t, citypes.StatusPassed, report.Stages[0].Runs[0].Gates[0].Status)
}

func TestAPendingGateBlocksTheStage(t *testing.T) {
	f := newFakeEngines(t)
	f.gateStatus = citypes.StatusPending

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(
			stage("prod", substage("eu-a", []string{"build"}, "approve")),
			stage("after", substage("default", []string{"self"})),
		), "/work", plain)
	require.NoError(t, err)
	require.False(t, report.Advanced())
	require.Len(t, report.Stages, 1)
	require.Contains(t, report.Stages[0].Reason, "gate")
}

func TestAPendingGateIsRetriedOnTheNextApply(t *testing.T) {
	f := newFakeEngines(t)
	f.gateStatus = citypes.StatusPending

	p := pipeline(stage("prod", substage("eu-a", []string{"build"}, "approve")))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)

	f.gateStatus = citypes.StatusPassed

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.True(t, report.Advanced())
}

func TestManySubstagesAllRun(t *testing.T) {
	f := newFakeEngines(t)

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("prod",
			config.Substage{
				Name: "eu-a", Engine: "here", Manager: "local", Targets: []string{"build"},
				Params: map[string]string{"region": "eu-west-1", "cell": "a"},
			},
			config.Substage{
				Name: "eu-b", Engine: "here", Manager: "local", Targets: []string{"build"},
				Params: map[string]string{"region": "eu-west-1", "cell": "b"},
			},
		)), "/work", plain)
	require.NoError(t, err)
	require.Len(t, report.Stages[0].Runs, 2)
	require.Equal(t, 2, f.counted(call{uriCompute, "run"}))
}

func TestOwnershipIsRecordedAndFedBackToTheManager(t *testing.T) {
	f := newFakeEngines(t)

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.NoError(t, err)

	payload, ok := f.store["owned/resources"]
	require.True(t, ok)

	var owned []citypes.Ownership
	require.NoError(t, json.Unmarshal([]byte(payload), &owned))
	require.NotNil(t, owned)
}

func TestAManagerRefusalStopsTheApply(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriManager, "reconcile"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), `manager "local"`)
}

// The whole reason the reconcile answers changed at all.
//
// Apply converges the pipeline's own resources and then resolves a revision
// that hashes each repo's HEAD plus its uncommitted changes. Continuing would
// measure the tree the reconcile just rewrote: the revision comes out dirty
// and the release refuses it, on every run, because a fresh clone starts from
// the same drift. Live run 33309087584 died exactly that way.
func TestAChangedReconcileStopsTheApplyBeforeTheRevision(t *testing.T) {
	f := newFakeEngines(t)
	f.reconcileChanged = true
	f.reconcilePublished = true

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.NoError(t, err, "a corrected drift is not a failure")

	require.True(t, report.Superseded)
	require.Empty(t, report.Revision.ID, "no revision was resolved, so none can be dirty")
	require.Empty(t, report.Stages)
	require.False(t, report.Minted)
	require.Equal(t, []string{"reconciled"}, report.Actions)
	require.Equal(t, 0, f.counted(call{uriCompute, "run"}))

	require.True(t, report.Advanced(),
		"nothing blocked: no stage failed and no gate refused")
}

// Superseding is a promise that the superseding run exists, and only a push
// makes that true. A change nobody published - a directory the local manager
// created, a commit with no remote - cannot re-fire the pipeline, so
// stopping for it would strand the pipeline forever: every fresh clone
// re-creates the change, reports superseded, and no run ever follows. Two
// live runs died exactly that way on an empty state directory.
func TestAChangedButUnpublishedReconcileContinues(t *testing.T) {
	f := newFakeEngines(t)
	f.reconcileChanged = true

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.NoError(t, err)

	require.False(t, report.Superseded, "no push means no superseding run; stopping would strand the pipeline")
	require.NotEmpty(t, report.Revision.ID, "the run continues and measures the converged state")
	require.NotEmpty(t, report.Stages)
	require.Equal(t, 1, f.counted(call{uriCompute, "run"}))

	var noted bool
	for _, a := range report.Actions {
		if strings.Contains(a, "no push will re-fire this pipeline") {
			noted = true
		}
	}
	require.True(t, noted, "continuing past unpublished changes is said out loud, never silent")
}

// The compatibility guarantee. A manager that reports no change leaves the
// apply exactly as it was before any of this existed.
func TestAnUnchangedReconcileRunsTheWholePipeline(t *testing.T) {
	f := newFakeEngines(t)

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.NoError(t, err)
	require.False(t, report.Superseded)
	require.NotEmpty(t, report.Revision.ID)
	require.Len(t, report.Stages, 1)
}

func TestADeclareFailureNamesTheEngine(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriCompute, "declare"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), `engine "here"`)
}

func TestAnUnknownComputeEngineIsReported(t *testing.T) {
	f := newFakeEngines(t)

	p := pipeline(stage("build", config.Substage{
		Name: "default", Engine: "missing", Manager: "local", Targets: []string{"build"},
	}))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work", plain)
	require.ErrorIs(t, err, reconcilecontroller.ErrEngine)
	require.Contains(t, err.Error(), `stage "build" substage "default"`)
}

func TestAWrongPortIsReported(t *testing.T) {
	f := newFakeEngines(t)

	p := pipeline(stage("build", config.Substage{
		Name: "default", Engine: "st", Manager: "local", Targets: []string{"build"},
	}))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work", plain)
	require.Error(t, err)
	require.Contains(t, err.Error(), "is a state engine, want compute")
}

func TestAnUnknownManagerIsReported(t *testing.T) {
	f := newFakeEngines(t)

	p := pipeline(stage("build", substage("default", []string{"build"})))
	p.Engines[0].Manager = "ghost"

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work", plain)
	require.ErrorIs(t, err, reconcilecontroller.ErrEngine)
	require.Contains(t, err.Error(), `manager "ghost"`)
}

func TestAGitFailureNamesTheRepo(t *testing.T) {
	f := newFakeEngines(t)
	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HeadSHA(mock.Anything, mock.Anything).Return("", errBoom).Once()

	_, err := reconcilecontroller.New(f.caller(), git, clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), "resolving the revision of golden-rust")
}

func TestWithoutAPromotionEngineEveryPassingSubstageAdvances(t *testing.T) {
	f := newFakeEngines(t)

	p := pipeline(config.Stage{Name: "build", Substages: []config.Substage{substage("default", []string{"build"})}})

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.True(t, report.Advanced())
	require.Contains(t, report.Stages[0].Reason, "passed every substage")
}

func TestWithoutAPromotionEngineAFailureStillBlocks(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["build/default"] = citypes.RunOutput{Status: citypes.StatusFailed}

	p := pipeline(config.Stage{Name: "build", Substages: []config.Substage{substage("default", []string{"build"})}})

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.False(t, report.Advanced())
	require.Contains(t, report.Stages[0].Reason, "not finished")
}

func TestAnUnknownPromotionEngineIsReported(t *testing.T) {
	f := newFakeEngines(t)

	p := pipeline(config.Stage{
		Name: "build", Promotion: "ghost",
		Substages: []config.Substage{substage("default", []string{"build"})},
	})

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work", plain)
	require.ErrorIs(t, err, reconcilecontroller.ErrEngine)
}

func TestAnUnknownGateEngineIsReported(t *testing.T) {
	f := newFakeEngines(t)

	p := pipeline(stage("prod", substage("eu-a", []string{"build"}, "ghost")))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work", plain)
	require.ErrorIs(t, err, reconcilecontroller.ErrEngine)
}

func TestTheRunEngineIsGivenTheCheckoutsAndTargets(t *testing.T) {
	f := newFakeEngines(t)

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.NoError(t, err)

	payload, ok := f.store["run/"+revisionOf(t, f)+"/build/default"]
	require.True(t, ok)

	var run citypes.Run
	require.NoError(t, json.Unmarshal([]byte(payload), &run))
	require.Equal(t, "here", run.Engine)
	require.Equal(t, "build", run.Stage)
	require.Equal(t, "default", run.Substage)
}

func TestAStateFailureIsNamed(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriState, "get"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), `state engine "st"`)
}

func TestARunEngineFailureIsNamed(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriCompute, "run"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), `stage "build" substage "default"`)
}

func TestCorruptStateIsReportedNotIgnored(t *testing.T) {
	f := newFakeEngines(t)
	f.store["owned/resources"] = "not json"

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.Error(t, err)
	require.Contains(t, err.Error(), "reading the ownership record")
}

func TestACorruptRunRecordIsReported(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(stage("build", substage("default", []string{"build"})))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)

	f.store["run/"+revisionOf(t, f)+"/build/default"] = "not json"

	_, err = reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work", plain)
	require.Error(t, err)
	require.Contains(t, err.Error(), "reading run")
}

func revisionOf(t *testing.T, f *fakeEngines) string {
	t.Helper()

	for key := range f.store {
		if len(key) > len("revision/") && key[:len("revision/")] == "revision/" {
			return key[len("revision/"):]
		}
	}

	t.Fatal("no revision was recorded")

	return ""
}

func TestANilClockFallsBackToTheRealOne(t *testing.T) {
	f := newFakeEngines(t)

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), nil).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.NoError(t, err)
	require.False(t, report.Revision.CreatedAt.IsZero())
}

func TestAnUnknownTargetAliasIsSkippedRatherThanSentAsAnEmptyTarget(t *testing.T) {
	f := newFakeEngines(t)

	p := pipeline(stage("build", substage("default", []string{"build", "ghost"})))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.Equal(t, 1, f.counted(call{uriCompute, "run"}))
}

func TestAFailureWritingOwnershipIsReported(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriState, "put"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.ErrorIs(t, err, errBoom)
}

func TestAGateEngineFailureIsReported(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriGate, "evaluate"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("prod", substage("eu-a", []string{"build"}, "approve"))), "/work", plain)
	require.ErrorIs(t, err, errBoom)
}

func TestAPromotionEngineFailureIsReported(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriPromotion, "evaluate"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), `stage "build"`)
}

func TestAPipelineWithNoReposStillResolvesARevision(t *testing.T) {
	f := newFakeEngines(t)

	p := pipeline(stage("build", substage("default", []string{"self"})))
	p.Repos = nil

	report, err := reconcilecontroller.New(f.caller(), noRepoGit(t), clock()).
		Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.NotEmpty(t, report.Revision.ID)
	require.Empty(t, report.Revision.Repos)
}

func TestTheRunOutputIsRecordedSoAFailureCanBeRead(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["build/default"] = citypes.RunOutput{
		Status: citypes.StatusFailed, Message: "exited 1", Output: "two tests failed\n",
	}

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.NoError(t, err)
	require.Equal(t, "two tests failed\n", report.Stages[0].Runs[0].Output)
}

func TestALongOutputIsTruncatedFromTheFront(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["build/default"] = citypes.RunOutput{
		Status: citypes.StatusFailed,
		Output: strings.Repeat("x", 20000) + "THE INTERESTING PART",
	}

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.NoError(t, err)

	got := report.Stages[0].Runs[0].Output
	require.Less(t, len(got), 20000)
	require.Contains(t, got, "THE INTERESTING PART")
	require.Contains(t, got, "earlier output dropped")
}

func TestAnUncommittedChangeIsANewRevisionAndReruns(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(stage("build", substage("default", []string{"build"})))

	clean := noRepoGit(t)
	clean.EXPECT().HeadSHA(mock.Anything, mock.Anything).Return("abc", nil).Maybe()
	clean.EXPECT().WorktreeHash(mock.Anything, mock.Anything).Return("", nil).Maybe()

	first, err := reconcilecontroller.New(f.caller(), clean, clock()).Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.Empty(t, first.Revision.Dirty)
	require.NotContains(t, first.Revision.ID, "-dirty")
	require.Equal(t, 1, f.counted(call{uriCompute, "run"}))

	dirty := noRepoGit(t)
	dirty.EXPECT().HeadSHA(mock.Anything, mock.Anything).Return("abc", nil).Maybe()
	dirty.EXPECT().WorktreeHash(mock.Anything, mock.Anything).Return("wt1", nil).Maybe()

	second, err := reconcilecontroller.New(f.caller(), dirty, clock()).Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.Equal(t, []string{"golden-rust"}, second.Revision.Dirty)
	require.Contains(t, second.Revision.ID, "-dirty")
	require.NotEqual(t, first.Revision.ID, second.Revision.ID)
	require.Equal(t, 2, f.counted(call{uriCompute, "run"}),
		"an uncommitted edit must rerun, or the pipeline reports green on code that does not compile")
}

func TestEditingAgainIsAnotherRevision(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(stage("build", substage("default", []string{"build"})))

	ids := map[string]bool{}

	for _, worktree := range []string{"wt1", "wt2", "wt3"} {
		git := noRepoGit(t)
		git.EXPECT().HeadSHA(mock.Anything, mock.Anything).Return("abc", nil).Maybe()
		git.EXPECT().WorktreeHash(mock.Anything, mock.Anything).Return(worktree, nil).Maybe()

		report, err := reconcilecontroller.New(f.caller(), git, clock()).Apply(context.Background(), p, "/work", plain)
		require.NoError(t, err)

		ids[report.Revision.ID] = true
	}

	require.Len(t, ids, 3, "each edit is its own revision")
	require.Equal(t, 3, f.counted(call{uriCompute, "run"}))
}

func TestAWorktreeFailureNamesTheRepo(t *testing.T) {
	f := newFakeEngines(t)

	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HeadSHA(mock.Anything, mock.Anything).Return("abc", nil).Once()
	git.EXPECT().WorktreeHash(mock.Anything, mock.Anything).Return("", errBoom).Once()

	_, err := reconcilecontroller.New(f.caller(), git, clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), "resolving the revision of golden-rust")
}

func TestSubstagesRunAtTheSameTime(t *testing.T) {
	f := newFakeEngines(t)

	subs := make([]config.Substage, 0, 4)
	for _, name := range []string{"rust", "go", "python", "typescript"} {
		subs = append(subs, substage(name, []string{"build"}))
	}

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).
		Apply(context.Background(), pipeline(stage("build", subs...)), "/work", plain)
	require.NoError(t, err)
	require.Len(t, report.Stages[0].Runs, 4)
	require.Equal(t, 4, f.peak,
		"substages are documented as running at the same time, so all four must be in flight together")
}

func TestTheReportKeepsSubstageOrderDespiteConcurrency(t *testing.T) {
	f := newFakeEngines(t)

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).
		Apply(context.Background(), pipeline(stage("build",
			substage("first", []string{"build"}),
			substage("second", []string{"build"}),
			substage("third", []string{"build"}),
		)), "/work", plain)
	require.NoError(t, err)
	require.Equal(t, []string{"first", "second", "third"}, []string{
		report.Stages[0].Runs[0].Substage,
		report.Stages[0].Runs[1].Substage,
		report.Stages[0].Runs[2].Substage,
	})
}

func TestOneFailingSubstageStillReportsTheOthers(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["build/go"] = citypes.RunOutput{Status: citypes.StatusFailed, Message: "vet found a bug"}

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).
		Apply(context.Background(), pipeline(stage("build",
			substage("rust", []string{"build"}),
			substage("go", []string{"build"}),
		)), "/work", plain)
	require.NoError(t, err)
	require.False(t, report.Advanced())
	require.Equal(t, citypes.StatusPassed, report.Stages[0].Runs[0].Status)
	require.Equal(t, citypes.StatusFailed, report.Stages[0].Runs[1].Status)
}

// A revision in state is a claim that this tuple of commits was proven. It used
// to be written before anything ran, so a build that then failed still handed a
// revision to whatever reads state next.
func TestAFailedBuildMintsNothing(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["build/default"] = citypes.RunOutput{Status: citypes.StatusFailed}

	c := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock())

	report, err := c.Apply(
		context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))),
		"/work",
		plain,
	)
	require.NoError(t, err)

	require.NotEmpty(t, report.Revision.ID, "the id is still resolved, a run record is keyed by it")
	require.False(t, report.Minted)
	require.NotContains(t, f.store, "revision/"+report.Revision.ID,
		"a broken build must not hand a revision to whatever reads state next")
}

func TestAStageThatDoesNotDeclareMintWritesNoRevision(t *testing.T) {
	f := newFakeEngines(t)
	c := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock())

	report, err := c.Apply(
		context.Background(),
		pipeline(mintlessStage("build", substage("default", []string{"build"}))),
		"/work",
		plain,
	)
	require.NoError(t, err)
	require.True(t, report.Advanced())
	require.False(t, report.Minted)
	require.NotContains(t, f.store, "revision/"+report.Revision.ID)
}

func TestAStageAfterTheMintingOneStillSeesTheRevision(t *testing.T) {
	f := newFakeEngines(t)
	c := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock())

	p := pipeline(
		stage("build", substage("default", []string{"build"})),
		mintlessStage("staging", substage("default", []string{"build"})),
	)

	report, err := c.Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.True(t, report.Minted)
	require.Len(t, report.Stages, 2)
	require.Contains(t, f.store, "revision/"+report.Revision.ID,
		"the build stage mints and staging runs against what it proved")
}

func TestAStageThatDeclaresAReleasePublishesWhatItProved(t *testing.T) {
	f := newFakeEngines(t)

	git := gitAt(t, "abc123", "v0.1.9")

	c := reconcilecontroller.New(f.caller(), git, clock())

	p := pipeline(releasingStage("build", substage("default", []string{"build"})))

	report, err := c.Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)

	require.Len(t, report.Released, 1)
	require.True(t, report.Released[0].Published)
	require.Equal(t, "https://example.com/releases/v0.1.10", report.Released[0].URL)

	require.Len(t, f.published, 1)
	require.Equal(t, report.Revision.ID, f.published[0].Revision)
	require.Equal(t, "v0.1.10", f.published[0].Version)
	require.Equal(t, "abc123", f.published[0].Repos["golden-rust"])
	require.Equal(t, "/work", f.published[0].Spec["root"],
		"the engine is told where the repos are")
}

func TestAReleaseSetKeepsTheOtherReposOutOfThePublish(t *testing.T) {
	f := newFakeEngines(t)
	c := reconcilecontroller.New(f.caller(), gitAt(t, "abc123", "v0.1.9"), clock())

	s := releasingStage("build", substage("default", []string{"build"}))
	s.ReleaseRepos = []string{"golden-rust"}

	p := pipeline(s)
	p.Repos = append(p.Repos, config.Repo{Name: "toolchain", URL: "git@example.com:toolchain.git"})

	report, err := c.Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)

	require.Equal(t, "abc123", report.Revision.Repos["toolchain"],
		"the revision keeps pinning what the release does not own")

	require.Len(t, f.published, 1)
	require.Equal(t, map[string]string{"golden-rust": "abc123"}, f.published[0].Repos,
		"the engine tags what it is handed, so it is handed only the release set")
}

func TestAFailedStagePublishesNothing(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["build/default"] = citypes.RunOutput{Status: citypes.StatusFailed}

	c := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock())

	p := pipeline(releasingStage("build", substage("default", []string{"build"})))

	report, err := c.Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)

	require.Empty(t, report.Released)
	require.Empty(t, f.published, "a build that did not pass must publish nothing")
}

func TestAStageWithNoReleasePublishesNothing(t *testing.T) {
	f := newFakeEngines(t)
	c := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock())

	report, err := c.Apply(
		context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))),
		"/work",
		plain,
	)
	require.NoError(t, err)
	require.Empty(t, report.Released)
	require.Empty(t, f.published)
}

// The default strategy moves the patch. A minor or a major is a claim about
// what changed, and the default reads no diff, so it moves the only number
// nobody has an opinion about.
func TestTheDefaultStrategyMovesThePatch(t *testing.T) {
	f := newFakeEngines(t)

	git := gitAt(t, "abc123", "v0.2.4")

	c := reconcilecontroller.New(f.caller(), git, clock())

	report, err := c.Apply(context.Background(),
		pipeline(releasingStage("build", substage("default", []string{"build"}))), "/work", plain)
	require.NoError(t, err)
	require.Len(t, f.published, 1)
	require.Equal(t, "v0.2.5", f.published[0].Version)
	require.True(t, report.Minted)
}

func TestTheMinorStrategyMovesTheMinor(t *testing.T) {
	f := newFakeEngines(t)

	git := gitAt(t, "abc123", "v0.2.4")

	c := reconcilecontroller.New(f.caller(), git, clock())

	p := pipeline(releasingStage("build", substage("default", []string{"build"})))
	p.Versioning.Strategy = config.StrategyMinor

	_, err := c.Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.Equal(t, "v0.3.0", f.published[0].Version)
}

// The semantic strategy reads EVERY member's subjects, because a factory
// releases its members together: a breaking change in any one of them is
// breaking for the number they all carry.
func TestTheSemanticStrategyReadsEveryMember(t *testing.T) {
	f := newFakeEngines(t)

	git := gitAt(t, "abc123", "v0.2.4")
	git.EXPECT().SubjectsSince(mock.Anything, "/work/golden-rust", "v0.2.4").
		Return([]string{"fix: a small thing"}, nil).Once()
	git.EXPECT().SubjectsSince(mock.Anything, "/work/golden-go", "v0.2.4").
		Return([]string{"feat: a new door"}, nil).Once()

	c := reconcilecontroller.New(f.caller(), git, clock())

	p := pipeline(releasingStage("build", substage("default", []string{"build"})))
	p.Repos = append(p.Repos, config.Repo{Name: "golden-go", URL: "https://example.com/golden-go"})
	p.Versioning.Strategy = config.StrategySemantic
	p.Versioning.Semantic = config.Semantic{
		Minor:  []string{"feat:"},
		Patch:  []string{"fix:"},
		Ignore: []string{"docs:"},
	}

	_, err := c.Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.Equal(t, "v0.3.0", f.published[0].Version,
		"the highest claim any member makes decides the one number all of them carry")
}

// A cap holds a factory that is not ready for v1. A bump that would cross it
// drops one level, so the factory keeps releasing rather than stopping.
func TestACapClampsTheBumpInsteadOfStoppingTheRelease(t *testing.T) {
	f := newFakeEngines(t)

	git := gitAt(t, "abc123", "v0.49.0")
	git.EXPECT().SubjectsSince(mock.Anything, mock.Anything, "v0.49.0").
		Return([]string{"feat!: everything moved"}, nil).Maybe()

	c := reconcilecontroller.New(f.caller(), git, clock())

	p := pipeline(releasingStage("build", substage("default", []string{"build"})))
	p.Versioning.Strategy = config.StrategySemantic
	p.Versioning.Cap = "v0"
	p.Versioning.Semantic = config.Semantic{Major: []string{"!:"}, Minor: []string{"feat:"}}

	_, err := c.Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.Equal(t, "v0.50.0", f.published[0].Version,
		"a major under a v0 cap drops to a minor rather than refusing to release")
}

// The prefix reaches the engine so the engine can name the tag, and only the
// tag: the version stays the version.
func TestTheTagPrefixReachesTheEngine(t *testing.T) {
	f := newFakeEngines(t)

	git := gitAt(t, "abc123", "v0.49.0")
	git.EXPECT().LatestTag(mock.Anything, "/work", "forge").Return("v0.49.0", nil).Maybe()

	c := reconcilecontroller.New(f.caller(), git, clock())

	p := pipeline(releasingStage("build", substage("default", []string{"build"})))
	p.Versioning.TagPrefix = "forge"

	_, err := c.Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.Equal(t, "v0.49.1", f.published[0].Version)
	require.Equal(t, "forge", f.published[0].TagPrefix)
}

func TestAWorkspaceThatNeverReleasedStartsAtTheFirstVersion(t *testing.T) {
	f := newFakeEngines(t)
	c := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock())

	_, err := c.Apply(context.Background(),
		pipeline(releasingStage("build", substage("default", []string{"build"}))), "/work", plain)
	require.NoError(t, err)
	require.Equal(t, "v0.1.0", f.published[0].Version)
}

// TestOnlyABootstrapTellsTheManagerItMayWriteCredentials pins the flag that
// keeps a pipeline run from holding the rights to rewrite the secrets it runs
// under.
//
// The core hands over everything, bootstrapOnly included: dropping a resource
// here would drop it from the ownership record too, and that record is what
// stops another manager claiming it. What changes is the flag, and the
// manager reads it.
func TestOnlyABootstrapTellsTheManagerItMayWriteCredentials(t *testing.T) {
	secret := citypes.Resource{
		Kind:          "actions-secret",
		Name:          "owner/repo/FORGE_CI_GITHUB_TOKEN",
		BootstrapOnly: true,
	}
	file := citypes.Resource{Kind: "file-content", Name: ".github/workflows/pipeline.yaml"}

	t.Run("an apply says no", func(t *testing.T) {
		f := newFakeEngines(t)
		f.declared = []citypes.Resource{file, secret}

		_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock()).Apply(
			context.Background(),
			pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
		require.NoError(t, err)

		require.Contains(t, f.realized, secret.ID(),
			"the resource still reaches the manager, so ownership survives")
		require.False(t, f.bootstrapped,
			"but the manager is told this is not the ceremony that writes credentials")
	})

	t.Run("a bootstrap says yes", func(t *testing.T) {
		f := newFakeEngines(t)
		f.declared = []citypes.Resource{file, secret}

		_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock()).Bootstrap(
			context.Background(),
			pipeline(stage("build", substage("default", []string{"build"}))), "/work", plain)
		require.NoError(t, err)

		require.True(t, f.bootstrapped,
			"the bootstrap is the one ceremony responsible for credentials")
	})
}

// The serialized duplicate of a push wave finds every run recorded green and
// executes nothing. The report must say so - a reused apply that reads like
// a build that did work hides what actually happened.
func TestAnApplyThatReusedEveryRunReportsNothingNew(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(stage("build", substage("default", []string{"build"})))

	first, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).
		Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.False(t, first.NothingNew, "the first apply executed the run")
	require.Zero(t, first.Stages[0].Reused)

	second, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).
		Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.True(t, second.NothingNew, "every run was answered from the recorded state")
	require.Equal(t, 1, second.Stages[0].Reused)
}
