package main

import (
	"context"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/promotioncontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

// NewHandlers wires the promotion into the generated tool surface.
//
// A generated wire type is not an internal type. The mapping is explicit
// rather than a JSON round trip, so a schema that changes shape is a compile
// error here instead of a field that silently stops arriving.
//
// The forge section of a run is not mapped. forge owns TestReport and Artifact
// and a promotion never reads them, so carrying them through would copy
// someone else's types for nothing.
func NewHandlers() Handlers {
	ctrl := promotioncontroller.New()

	return Handlers{
		Declare: func(_ context.Context, in DeclareInput) (*DeclareOutput, error) {
			out, err := ctrl.Declare(in.Spec)
			if err != nil {
				return nil, err
			}

			return &DeclareOutput{Resources: fromResources(out.Resources)}, nil
		},
		Evaluate: func(_ context.Context, in PromotionInput) (*PromotionOutput, error) {
			runs := make([]citypes.Run, 0, len(in.Runs))
			for _, r := range in.Runs {
				runs = append(runs, toRun(r))
			}

			out, err := ctrl.Evaluate(citypes.PromotionInput{
				Stage: in.Stage, Runs: runs, Spec: in.Spec,
			})
			if err != nil {
				return nil, err
			}

			return &PromotionOutput{Advance: out.Advance, Reason: out.Reason}, nil
		},
	}
}

func fromResources(in []citypes.Resource) []Resource {
	out := make([]Resource, 0, len(in))

	for _, r := range in {
		out = append(out, Resource{
			Kind: r.Kind, Name: r.Name, BootstrapOnly: r.BootstrapOnly, Spec: r.Spec,
		})
	}

	return out
}

// toRun maps a run off the wire. startedAt is a string there, because that is
// what a date-time is in JSON, and time.Time here.
func toRun(in Run) citypes.Run {
	startedAt, err := time.Parse(time.RFC3339, in.StartedAt)
	if err != nil {
		startedAt = time.Time{}
	}

	gates := make([]citypes.GateResult, 0, len(in.Gates))
	for _, g := range in.Gates {
		gates = append(gates, citypes.GateResult{
			Alias: g.Alias, Status: citypes.Status(g.Status), Message: g.Message,
		})
	}

	return citypes.Run{
		Revision:  in.Revision,
		Stage:     in.Stage,
		Substage:  in.Substage,
		Engine:    in.Engine,
		Status:    citypes.Status(in.Status),
		StartedAt: startedAt,
		Duration:  in.Duration,
		Message:   in.Message,
		Output:    in.Output,
		Gates:     gates,
	}
}
