package reconcilecontroller_test

import (
	"context"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/reconcilecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/stretchr/testify/require"
)

func withTrigger(p config.Pipeline) config.Pipeline {
	p.Engines = append(p.Engines, config.Engine{
		Alias: "on-change", Type: config.PortTrigger, Engine: uriTrigger, Manager: "local",
		Spec: map[string]any{"watch": []any{"/repo"}},
	})
	p.Triggers = []string{"on-change"}

	return p
}

func TestBootstrapReconcilesAndRecordsTheRevisionWithoutRunning(t *testing.T) {
	f := newFakeEngines(t)

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Bootstrap(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.NoError(t, err)
	require.NotEmpty(t, report.Revision.ID)
	require.Equal(t, []string{"reconciled"}, report.Actions)
	require.Empty(t, report.Stages)
	require.Equal(t, 0, f.counted(call{uriCompute, "run"}))
	require.Contains(t, f.store, "revision/"+report.Revision.ID)
}

func TestBootstrapReportsAManagerFailure(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriManager, "reconcile"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Bootstrap(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.ErrorIs(t, err, errBoom)
}

func TestBootstrapReportsAGitFailure(t *testing.T) {
	f := newFakeEngines(t)
	git := gitAt(t, "")
	git.ExpectedCalls = nil
	git.EXPECT().HeadSHA(mockAny(), mockAny()).Return("", errBoom).Once()

	_, err := reconcilecontroller.New(f.caller(), git, clock()).Bootstrap(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.ErrorIs(t, err, errBoom)
}

func TestStatusShowsPendingBeforeAnythingRan(t *testing.T) {
	f := newFakeEngines(t)

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Status(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.NoError(t, err)
	require.Len(t, report.Stages, 1)
	require.Equal(t, citypes.StatusPending, report.Stages[0].Runs[0].Status)
	require.False(t, report.Stages[0].Advance)
	require.Equal(t, "not finished", report.Stages[0].Reason)
	require.Equal(t, 0, f.counted(call{uriCompute, "run"}))
}

func TestStatusShowsWhatApplyRecorded(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(stage("build", substage("default", []string{"build"})))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
	require.NoError(t, err)

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Status(context.Background(), p, "/work")
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPassed, report.Stages[0].Runs[0].Status)
	require.True(t, report.Stages[0].Advance)
	require.Equal(t, "every substage passed", report.Stages[0].Reason)
}

func TestStatusCountsAPendingGateAsNotFinished(t *testing.T) {
	f := newFakeEngines(t)
	f.gateStatus = citypes.StatusPending

	p := pipeline(stage("prod", substage("eu-a", []string{"build"}, "approve")))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Apply(context.Background(), p, "/work")
	require.NoError(t, err)

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Status(context.Background(), p, "/work")
	require.NoError(t, err)
	require.False(t, report.Stages[0].Advance)
}

func TestStatusReportsAGitFailure(t *testing.T) {
	f := newFakeEngines(t)
	git := gitAt(t, "")
	git.ExpectedCalls = nil
	git.EXPECT().HeadSHA(mockAny(), mockAny()).Return("", errBoom).Once()

	_, err := reconcilecontroller.New(f.caller(), git, clock()).Status(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.ErrorIs(t, err, errBoom)
}

func TestStatusReportsAStateFailure(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriState, "get"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Status(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "/work")
	require.ErrorIs(t, err, errBoom)
}

func TestPollIsChangedTheFirstTimeAndQuietAfter(t *testing.T) {
	f := newFakeEngines(t)
	p := withTrigger(pipeline(stage("build", substage("default", []string{"build"}))))

	c := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock())

	first, err := c.Poll(context.Background(), p, "")
	require.NoError(t, err)
	require.True(t, first.Changed)
	require.Contains(t, first.Reason, "on-change:")

	second, err := c.Poll(context.Background(), p, "")
	require.NoError(t, err)
	require.False(t, second.Changed)
}

func TestPollWithNoTriggersIsQuiet(t *testing.T) {
	f := newFakeEngines(t)

	out, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Poll(context.Background(),
		pipeline(stage("build", substage("default", []string{"build"}))), "")
	require.NoError(t, err)
	require.False(t, out.Changed)
}

func TestPollReportsAnUnknownTrigger(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(stage("build", substage("default", []string{"build"})))
	p.Triggers = []string{"ghost"}

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Poll(context.Background(), p, "")
	require.ErrorIs(t, err, reconcilecontroller.ErrEngine)
}

func TestPollReportsAnEngineFailure(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriTrigger, "poll"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Poll(context.Background(),
		withTrigger(pipeline(stage("build", substage("default", []string{"build"})))), "")
	require.ErrorIs(t, err, errBoom)
}

func TestPollReportsAStateFailure(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriState, "get"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Poll(context.Background(),
		withTrigger(pipeline(stage("build", substage("default", []string{"build"})))), "")
	require.ErrorIs(t, err, errBoom)
}

func TestPollReportsAFailureWritingTheFingerprint(t *testing.T) {
	f := newFakeEngines(t)
	f.failOn[call{uriState, "put"}] = errBoom

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc"), clock()).Poll(context.Background(),
		withTrigger(pipeline(stage("build", substage("default", []string{"build"})))), "")
	require.ErrorIs(t, err, errBoom)
}
