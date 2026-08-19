package reconcilecontroller_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/reconcilecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/gitadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

func gitAt(t *testing.T, sha string) *gitadaptermock.MockGit {
	t.Helper()

	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HeadSHA(mock.Anything, mock.Anything).Return(sha, nil).Maybe()

	return git
}

func TestEveryStageRunsInOrderAndAdvances(t *testing.T) {
	f := newFakeEngines(t)
	c := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock())

	report, err := c.Apply(context.Background(), pipeline(
		stage("build", substage("default", []string{"build"})),
		stage("self", substage("default", []string{"self"})),
	), "/work")
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

	first, err := c.Apply(context.Background(), pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.NoError(t, err)
	require.NotEmpty(t, first.Revision.ID)
	require.Equal(t, "abc123", first.Revision.Repos["golden-rust"])
	require.Contains(t, f.store, "revision/"+first.Revision.ID)

	second, err := c.Apply(context.Background(), pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.NoError(t, err)
	require.Equal(t, first.Revision.ID, second.Revision.ID)
}

func TestANewCommitIsANewRevision(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(stage("build", substage("default", []string{"build"})))

	before, err := reconcilecontroller.New(f.caller(), gitAt(t, "aaa"), clock()).
		Apply(context.Background(), p, "/work")
	require.NoError(t, err)

	after, err := reconcilecontroller.New(f.caller(), gitAt(t, "bbb"), clock()).
		Apply(context.Background(), p, "/work")
	require.NoError(t, err)
	require.NotEqual(t, before.Revision.ID, after.Revision.ID)
}

func TestAPassedSubstageIsNotRunAgain(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(stage("build", substage("default", []string{"build"})))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).
		Apply(context.Background(), p, "/work")
	require.NoError(t, err)
	require.Equal(t, 1, f.counted(call{uriCompute, "run"}))

	_, err = reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).
		Apply(context.Background(), p, "/work")
	require.NoError(t, err)
	require.Equal(t, 1, f.counted(call{uriCompute, "run"}))
}

func TestAFailedSubstageStopsTheRestOfThePipeline(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["build/default"] = citypes.RunOutput{Status: citypes.StatusFailed, Message: "two tests failed"}

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), pipeline(
		stage("build", substage("default", []string{"build"})),
		stage("self", substage("default", []string{"self"})),
	), "/work")
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

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
	require.NoError(t, err)

	delete(f.runOutputs, "build/default")

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
	require.NoError(t, err)
	require.True(t, report.Advanced())
	require.Equal(t, 2, f.counted(call{uriCompute, "run"}))
}

func TestGatesRunAfterTheSubstageAndAreRecorded(t *testing.T) {
	f := newFakeEngines(t)

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("prod", substage("eu-a", []string{"build"}, "approve"))), "/work")
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
		), "/work")
	require.NoError(t, err)
	require.False(t, report.Advanced())
	require.Len(t, report.Stages, 1)
	require.Contains(t, report.Stages[0].Reason, "gate")
}

func TestAPendingGateIsRetriedOnTheNextApply(t *testing.T) {
	f := newFakeEngines(t)
	f.gateStatus = citypes.StatusPending

	p := pipeline(stage("prod", substage("eu-a", []string{"build"}, "approve")))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
	require.NoError(t, err)

	f.gateStatus = citypes.StatusPassed

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
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
		)), "/work")
	require.NoError(t, err)
	require.Len(t, report.Stages[0].Runs, 2)
	require.Equal(t, 2, f.counted(call{uriCompute, "run"}))
}

func TestOwnershipIsRecordedAndFedBackToTheManager(t *testing.T) {
	f := newFakeEngines(t)

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
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
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), `manager "local"`)
}

func TestADeclareFailureNamesTheEngine(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriCompute, "declare"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), `engine "here"`)
}

func TestAnUnknownComputeEngineIsReported(t *testing.T) {
	f := newFakeEngines(t)

	p := pipeline(stage("build", config.Substage{
		Name: "default", Engine: "missing", Manager: "local", Targets: []string{"build"},
	}))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
	require.ErrorIs(t, err, reconcilecontroller.ErrEngine)
	require.Contains(t, err.Error(), `stage "build" substage "default"`)
}

func TestAWrongPortIsReported(t *testing.T) {
	f := newFakeEngines(t)

	p := pipeline(stage("build", config.Substage{
		Name: "default", Engine: "st", Manager: "local", Targets: []string{"build"},
	}))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
	require.Error(t, err)
	require.Contains(t, err.Error(), "is a state engine, want compute")
}

func TestAnUnknownManagerIsReported(t *testing.T) {
	f := newFakeEngines(t)

	p := pipeline(stage("build", substage("default", []string{"build"})))
	p.Engines[0].Manager = "ghost"

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
	require.ErrorIs(t, err, reconcilecontroller.ErrEngine)
	require.Contains(t, err.Error(), `manager "ghost"`)
}

func TestAGitFailureNamesTheRepo(t *testing.T) {
	f := newFakeEngines(t)
	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HeadSHA(mock.Anything, mock.Anything).Return("", errBoom).Once()

	_, err := reconcilecontroller.New(f.caller(), git, clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), "resolving the revision of golden-rust")
}

func TestWithoutAPromotionEngineEveryPassingSubstageAdvances(t *testing.T) {
	f := newFakeEngines(t)

	p := pipeline(config.Stage{Name: "build", Substages: []config.Substage{substage("default", []string{"build"})}})

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
	require.NoError(t, err)
	require.True(t, report.Advanced())
	require.Contains(t, report.Stages[0].Reason, "passed every substage")
}

func TestWithoutAPromotionEngineAFailureStillBlocks(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["build/default"] = citypes.RunOutput{Status: citypes.StatusFailed}

	p := pipeline(config.Stage{Name: "build", Substages: []config.Substage{substage("default", []string{"build"})}})

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
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

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
	require.ErrorIs(t, err, reconcilecontroller.ErrEngine)
}

func TestAnUnknownGateEngineIsReported(t *testing.T) {
	f := newFakeEngines(t)

	p := pipeline(stage("prod", substage("eu-a", []string{"build"}, "ghost")))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
	require.ErrorIs(t, err, reconcilecontroller.ErrEngine)
}

func TestTheRunEngineIsGivenTheCheckoutsAndTargets(t *testing.T) {
	f := newFakeEngines(t)

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
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
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), `state engine "st"`)
}

func TestARunEngineFailureIsNamed(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriCompute, "run"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), `stage "build" substage "default"`)
}

func TestCorruptStateIsReportedNotIgnored(t *testing.T) {
	f := newFakeEngines(t)
	f.store["owned/resources"] = "not json"

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.Error(t, err)
	require.Contains(t, err.Error(), "reading the ownership record")
}

func TestACorruptRunRecordIsReported(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(stage("build", substage("default", []string{"build"})))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
	require.NoError(t, err)

	f.store["run/"+revisionOf(t, f)+"/build/default"] = "not json"

	_, err = reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
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
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.NoError(t, err)
	require.False(t, report.Revision.CreatedAt.IsZero())
}

func TestAnUnknownTargetAliasIsSkippedRatherThanSentAsAnEmptyTarget(t *testing.T) {
	f := newFakeEngines(t)

	p := pipeline(stage("build", substage("default", []string{"build", "ghost"})))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
	require.NoError(t, err)
	require.Equal(t, 1, f.counted(call{uriCompute, "run"}))
}

func TestAFailureWritingOwnershipIsReported(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriState, "put"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.ErrorIs(t, err, errBoom)
}

func TestAGateEngineFailureIsReported(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriGate, "evaluate"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("prod", substage("eu-a", []string{"build"}, "approve"))), "/work")
	require.ErrorIs(t, err, errBoom)
}

func TestAPromotionEngineFailureIsReported(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriPromotion, "evaluate"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), `stage "build"`)
}

func TestAPipelineWithNoReposStillResolvesARevision(t *testing.T) {
	f := newFakeEngines(t)

	p := pipeline(stage("build", substage("default", []string{"self"})))
	p.Repos = nil

	report, err := reconcilecontroller.New(f.caller(), gitadaptermock.NewMockGit(t), clock()).
		Apply(context.Background(), p, "/work")
	require.NoError(t, err)
	require.NotEmpty(t, report.Revision.ID)
	require.Empty(t, report.Revision.Repos)
}
