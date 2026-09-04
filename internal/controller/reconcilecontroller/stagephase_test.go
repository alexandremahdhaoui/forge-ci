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
		stage("check", substage("default", []string{"build"})),
		stage("build", substage("default", []string{"build"})),
		releaseStage(),
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
	require.False(t, check.Minted, "the evaluate job minted; a stage job never does")

	build, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "build"})
	require.NoError(t, err)
	require.Len(t, build.Stages, 1)
	require.False(t, build.Minted, "no stage mints; the run had one identity before any of them ran")
	require.Empty(t, build.Released, "this stage builds; the one after it publishes")
	require.Empty(t, f.published)

	// The stage that publishes is a job like the two before it. There is no
	// release phase to run after them.
	release, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "release"})
	require.NoError(t, err)
	require.Len(t, release.Released, 1)
	require.Len(t, f.published, 1)
	require.Equal(t, "v0.1.10", f.published[0].Version)

	_, err = apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "deploy"})
	require.ErrorIs(t, err, reconcilecontroller.ErrUnknownStage)
}

// One job per substage: each substage runs alone and decides nothing for
// the stage. The stage after asks the promotion over every substage's
// record itself, and asked before one of them ran, it refuses by name.
func TestOneJobPerSubstageAndTheNextStageAsksThePromotion(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(
		stage("build", substage("default", []string{"build"}), substage("dist", []string{"build"})),
		releaseStage(),
	)

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

	// Half the stage in front has run: the stage after arrives early and
	// says which substage it is waiting on.
	_, err = apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "release"})
	require.ErrorIs(t, err, reconcilecontroller.ErrStageOutOfOrder)
	require.ErrorIs(t, err, reconcilecontroller.ErrStageIncomplete)
	require.Contains(t, err.Error(), `substage "dist"`)

	_, err = apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "build", Substage: "dist"})
	require.NoError(t, err)

	// Both records exist: the stage after asks the promotion over them and
	// runs. No job in between wrote anything down.
	release, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "release"})
	require.NoError(t, err)
	require.Len(t, release.Released, 1)
	require.Equal(t, 2, f.counted(call{uriCompute, "run"}), "asking the promotion runs nothing")

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

	// The stage after asks the promotion itself, over the record the red
	// substage left. Nothing between the two stages had to spend a runner
	// to say no.
	_, err = apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "publish"})
	require.ErrorIs(t, err, reconcilecontroller.ErrStageOutOfOrder)
	require.Contains(t, err.Error(), `stage "build" before "publish"`)
}
