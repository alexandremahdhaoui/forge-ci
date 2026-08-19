package promotioncontroller

import (
	"errors"
	"fmt"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var ErrThreshold = errors.New("spec.threshold must be a percentage between 0 and 100")

type Controller struct{}

func New() Controller {
	return Controller{}
}

func (Controller) Declare(map[string]any) (citypes.DeclareOutput, error) {
	return citypes.DeclareOutput{Resources: []citypes.Resource{}}, nil
}

func (Controller) Evaluate(in citypes.PromotionInput) (citypes.PromotionOutput, error) {
	if len(in.Runs) == 0 {
		return citypes.PromotionOutput{
			Advance: false,
			Reason:  fmt.Sprintf("stage %q has no runs yet", in.Stage),
		}, nil
	}

	threshold, err := thresholdOf(in.Spec)
	if err != nil {
		return citypes.PromotionOutput{}, err
	}

	passed := 0

	for _, run := range in.Runs {
		if run.Status != citypes.StatusPassed {
			continue
		}

		if blocked := firstBlockingGate(run); blocked != "" {
			continue
		}

		passed++
	}

	percent := float64(passed) / float64(len(in.Runs)) * 100

	if percent+1e-9 < threshold {
		return citypes.PromotionOutput{
			Advance: false,
			Reason: fmt.Sprintf("stage %q passed %d of %d substages, which is %.0f percent, below the %.0f percent needed",
				in.Stage, passed, len(in.Runs), percent, threshold),
		}, nil
	}

	return citypes.PromotionOutput{
		Advance: true,
		Reason: fmt.Sprintf("stage %q passed %d of %d substages",
			in.Stage, passed, len(in.Runs)),
	}, nil
}

func firstBlockingGate(run citypes.Run) string {
	for _, gate := range run.Gates {
		if gate.Status != citypes.StatusPassed {
			return gate.Alias
		}
	}

	return ""
}

func thresholdOf(spec map[string]any) (float64, error) {
	raw, ok := spec["threshold"]
	if !ok {
		return 100, nil
	}

	switch v := raw.(type) {
	case float64:
		if v < 0 || v > 100 {
			return 0, ErrThreshold
		}

		return v, nil
	case int:
		return thresholdOf(map[string]any{"threshold": float64(v)})
	default:
		return 0, fmt.Errorf("reading spec.threshold: %w", ErrThreshold)
	}
}
