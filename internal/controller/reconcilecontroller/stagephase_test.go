package reconcilecontroller_test

import (
	"context"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/reconcilecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/stretchr/testify/require"
)

// One apply cut into jobs must reach the same release the whole apply
// reaches. These tests cut it the two ways a compute engine may render it.

// One job per stage: each stage runs on its own, after the one before it
// left a green record, and the release finds every run recorded.
func TestOneJobPerStageReachesTheRelease(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(
		mintlessStage("check", substage("default", []string{"build"})),
		releasingStage("build", substage("default", []string{"build"})),
	)

	apply := func(opts reconcilecontroller.Options) (reconcilecontroller.Report, error) {
		return reconcilecontroller.New(f.caller(), gitAt(t, "abc123", "v0.1.9"), clock()).
			Apply(context.Background(), p, "/work", opts)
	}

	_, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseEvaluate})
	require.NoError(t, err)

	// The second stage before the first: refused, by name.
	_, err = apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "build"})
	require.ErrorIs(t, err, reconcilecontroller.ErrStageOutOfOrder)
	require.Contains(t, err.Error(), `stage "check" before "build"`)

	check, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "check"})
	require.NoError(t, err)
	require.Len(t, check.Stages, 1)
	require.Equal(t, "check", check.Stages[0].Name)
	require.True(t, check.Stages[0].Advance)
	require.False(t, check.Minted, "check does not mint")

	build, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "build"})
	require.NoError(t, err)
	require.Len(t, build.Stages, 1)
	require.True(t, build.Minted, "the minting stage mints in its own job")
	require.Empty(t, build.Released, "a stage job releases nothing")
	require.Empty(t, f.published)

	release, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseRelease})
	require.NoError(t, err)
	require.Len(t, release.Released, 1)
	require.Len(t, f.published, 1)
	require.Equal(t, "v0.1.10", f.published[0].Version)

	_, err = apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "deploy"})
	require.ErrorIs(t, err, reconcilecontroller.ErrUnknownStage)
}

// One job per substage: each substage runs alone and decides nothing for
// the stage; the promotion job reads every substage's record, asks the
// promotion, and mints. Asked before a substage ran, it refuses.
func TestOneJobPerSubstageWithAPromotionJob(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(releasingStage("build",
		substage("default", []string{"build"}), substage("dist", []string{"build"})))

	apply := func(opts reconcilecontroller.Options) (reconcilecontroller.Report, error) {
		return reconcilecontroller.New(f.caller(), gitAt(t, "abc123", "v0.1.9"), clock()).
			Apply(context.Background(), p, "/work", opts)
	}

	_, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseEvaluate})
	require.NoError(t, err)

	one, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "build", Substage: "default"})
	require.NoError(t, err)
	require.Len(t, one.Stages[0].Runs, 1)
	require.Equal(t, "default", one.Stages[0].Runs[0].Substage)
	require.True(t, one.Advanced(), "a passed substage is a green job")
	require.False(t, one.Minted, "a substage job never mints")

	_, err = apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "build", Promote: true})
	require.ErrorIs(t, err, reconcilecontroller.ErrStageIncomplete)
	require.Contains(t, err.Error(), `substage "dist"`)

	_, err = apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "build", Substage: "dist"})
	require.NoError(t, err)

	promoted, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "build", Promote: true})
	require.NoError(t, err)
	require.Len(t, promoted.Stages[0].Runs, 2, "the promotion reads both records")
	require.Equal(t, 2, promoted.Stages[0].Reused)
	require.True(t, promoted.Stages[0].Advance)
	require.True(t, promoted.Minted)
	require.Equal(t, 2, f.counted(call{uriCompute, "run"}), "the promotion job runs nothing")

	release, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseRelease})
	require.NoError(t, err)
	require.Len(t, release.Released, 1)
	require.Len(t, f.published, 1)

	_, err = apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "build", Substage: "nope"})
	require.ErrorIs(t, err, reconcilecontroller.ErrUnknownStage)
}

// A substage that fails is a red job, and the promotion over it does not
// advance: the stage after it then refuses to run.
func TestAFailedSubstageJobBlocksTheStageAfterIt(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["build/default"] = citypes.RunOutput{Status: citypes.StatusFailed, Message: "boom"}

	p := pipeline(
		stage("build", substage("default", []string{"build"})),
		stage("publish", substage("default", []string{"build"})),
	)

	apply := func(opts reconcilecontroller.Options) (reconcilecontroller.Report, error) {
		return reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock()).
			Apply(context.Background(), p, "/work", opts)
	}

	_, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseEvaluate})
	require.NoError(t, err)

	red, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "build", Substage: "default"})
	require.NoError(t, err, "a failing build is a report, never an error")
	require.False(t, red.Advanced())
	require.Contains(t, red.Stages[0].Reason, "did not pass")

	promoted, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "build", Promote: true})
	require.NoError(t, err)
	require.False(t, promoted.Stages[0].Advance)

	_, err = apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "publish"})
	require.ErrorIs(t, err, reconcilecontroller.ErrStageOutOfOrder)
}
