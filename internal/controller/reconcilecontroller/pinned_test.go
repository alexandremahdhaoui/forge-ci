package reconcilecontroller_test

import (
	"context"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/reconcilecontroller"
	"github.com/stretchr/testify/require"
)

// The case that killed golden run 27 and forge-self run 29.
//
// A stage publishes into one of the pipeline's own repos, so by the time the
// next job clones the workspace that repo has moved and the revision it
// derives is not the one the run is proving. Handed the revision the evaluate
// phase decided, the later phase answers for the run rather than for the
// clone, and finds every record the run wrote.
func TestALaterPhaseRunsOnTheRevisionItWasGiven(t *testing.T) {
	f := newFakeEngines(t)
	p := releasingPipeline()

	evaluate, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc123", "v0.1.9"), clock()).
		Apply(context.Background(), p, "/work", reconcilecontroller.Options{
			Phase: reconcilecontroller.PhaseEvaluate,
		})
	require.NoError(t, err)
	require.NotEmpty(t, evaluate.Revision.ID)
	require.True(t, evaluate.Minted)

	// The repo the publish stage writes has moved: this clone hashes to
	// something else entirely.
	moved := reconcilecontroller.New(f.caller(), gitAt(t, "def456", "v0.1.9"), clock())

	_, err = moved.Apply(context.Background(), p, "/work", reconcilecontroller.Options{
		Phase: reconcilecontroller.PhaseStages,
	})
	require.ErrorIs(t, err, reconcilecontroller.ErrNoEvaluation,
		"without the revision, the phase asks for a record this run never wrote")

	stages, err := moved.Apply(context.Background(), p, "/work", reconcilecontroller.Options{
		Phase: reconcilecontroller.PhaseStages, Revision: evaluate.Revision.ID,
	})
	require.NoError(t, err)
	require.Equal(t, evaluate.Revision.ID, stages.Revision.ID)
	require.Equal(t, evaluate.Version, stages.Version)
	require.False(t, stages.Minted, "the evaluate phase minted, before this one ran")
	require.Len(t, stages.Released, 1)
	require.Len(t, f.published, 1)
	require.Equal(t, evaluate.Version, f.published[0].Version)
}

// A store that answers with a record for another revision is machinery
// broken, not a build gone red, and the run says so rather than carrying on
// under a number it was not asked for.
func TestARecordForAnotherRevisionRefuses(t *testing.T) {
	f := newFakeEngines(t)
	p := releasingPipeline()
	c := reconcilecontroller.New(f.caller(), gitAt(t, "abc123", "v0.1.9"), clock())

	evaluate, err := c.Apply(context.Background(), p, "/work", reconcilecontroller.Options{
		Phase: reconcilecontroller.PhaseEvaluate,
	})
	require.NoError(t, err)

	// The record this key holds was written for the revision above, so
	// asking for it under another name is a store that disagrees with
	// itself.
	f.rekeyEvaluation(t, evaluate.Revision.ID, "0000deadbeef")

	_, err = c.Apply(context.Background(), p, "/work", reconcilecontroller.Options{
		Phase: reconcilecontroller.PhaseStages, Revision: "0000deadbeef",
	})
	require.ErrorIs(t, err, reconcilecontroller.ErrRevisionMismatch)
}

// A decision written by a forge-ci from before the revision travelled has
// none recorded. The phase resolves its own, which is what that version did,
// so an apply already in flight when the toolchain moves finishes the way it
// started rather than dying on the upgrade.
func TestADecisionWithNoRevisionStillResolvesLocally(t *testing.T) {
	f := newFakeEngines(t)
	p := releasingPipeline()
	c := reconcilecontroller.New(f.caller(), gitAt(t, "abc123", "v0.1.9"), clock())

	evaluate, err := c.Apply(context.Background(), p, "/work", reconcilecontroller.Options{
		Phase: reconcilecontroller.PhaseEvaluate,
	})
	require.NoError(t, err)

	f.stripEvaluationRevision(t, evaluate.Revision.ID)

	stages, err := c.Apply(context.Background(), p, "/work", reconcilecontroller.Options{
		Phase: reconcilecontroller.PhaseStages, Revision: evaluate.Revision.ID,
	})
	require.NoError(t, err)
	require.Equal(t, evaluate.Revision.ID, stages.Revision.ID)
}
