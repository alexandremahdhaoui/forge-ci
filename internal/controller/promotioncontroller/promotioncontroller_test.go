package promotioncontroller_test

import (
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/promotioncontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/stretchr/testify/require"
)

func run(status citypes.Status, gates ...citypes.GateResult) citypes.Run {
	return citypes.Run{Status: status, Gates: gates}
}

func TestEveryPassingSubstageAdvances(t *testing.T) {
	out, err := promotioncontroller.New().Evaluate(citypes.PromotionInput{
		Stage: "build",
		Runs:  []citypes.Run{run(citypes.StatusPassed), run(citypes.StatusPassed)},
	})
	require.NoError(t, err)
	require.True(t, out.Advance)
	require.Contains(t, out.Reason, `stage "build" passed 2 of 2`)
}

func TestOneFailureBlocksByDefault(t *testing.T) {
	out, err := promotioncontroller.New().Evaluate(citypes.PromotionInput{
		Stage: "prod",
		Runs:  []citypes.Run{run(citypes.StatusPassed), run(citypes.StatusFailed)},
	})
	require.NoError(t, err)
	require.False(t, out.Advance)
	require.Contains(t, out.Reason, "50 percent, below the 100 percent needed")
}

func TestAThresholdToleratesAPartialFailure(t *testing.T) {
	out, err := promotioncontroller.New().Evaluate(citypes.PromotionInput{
		Stage: "prod",
		Runs: []citypes.Run{
			run(citypes.StatusPassed), run(citypes.StatusPassed),
			run(citypes.StatusPassed), run(citypes.StatusPassed),
			run(citypes.StatusPassed), run(citypes.StatusPassed),
			run(citypes.StatusPassed), run(citypes.StatusPassed),
			run(citypes.StatusPassed), run(citypes.StatusFailed),
		},
		Spec: map[string]any{"threshold": float64(90)},
	})
	require.NoError(t, err)
	require.True(t, out.Advance)
}

func TestAThresholdStillBlocksBelowIt(t *testing.T) {
	out, err := promotioncontroller.New().Evaluate(citypes.PromotionInput{
		Stage: "prod",
		Runs:  []citypes.Run{run(citypes.StatusPassed), run(citypes.StatusFailed)},
		Spec:  map[string]any{"threshold": 75},
	})
	require.NoError(t, err)
	require.False(t, out.Advance)
}

func TestAPendingGateBlocksAPassingSubstage(t *testing.T) {
	out, err := promotioncontroller.New().Evaluate(citypes.PromotionInput{
		Stage: "prod",
		Runs: []citypes.Run{
			run(citypes.StatusPassed, citypes.GateResult{Alias: "approve", Status: citypes.StatusPending}),
		},
	})
	require.NoError(t, err)
	require.False(t, out.Advance)
}

func TestAPassingGateLetsItThrough(t *testing.T) {
	out, err := promotioncontroller.New().Evaluate(citypes.PromotionInput{
		Stage: "prod",
		Runs: []citypes.Run{
			run(citypes.StatusPassed, citypes.GateResult{Alias: "approve", Status: citypes.StatusPassed}),
		},
	})
	require.NoError(t, err)
	require.True(t, out.Advance)
}

func TestNoRunsMeansNothingToPromote(t *testing.T) {
	out, err := promotioncontroller.New().Evaluate(citypes.PromotionInput{Stage: "build"})
	require.NoError(t, err)
	require.False(t, out.Advance)
	require.Contains(t, out.Reason, "no runs yet")
}

func TestABadThresholdIsRejected(t *testing.T) {
	for _, spec := range []map[string]any{
		{"threshold": float64(-1)},
		{"threshold": float64(101)},
		{"threshold": "most"},
	} {
		_, err := promotioncontroller.New().Evaluate(citypes.PromotionInput{
			Stage: "prod", Runs: []citypes.Run{run(citypes.StatusPassed)}, Spec: spec,
		})
		require.ErrorIs(t, err, promotioncontroller.ErrThreshold)
	}
}

func TestPromotionDeclaresNoResources(t *testing.T) {
	out, err := promotioncontroller.New().Declare(nil)
	require.NoError(t, err)
	require.Empty(t, out.Resources)
}
