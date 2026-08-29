package main

import (
	"context"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/gatecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

// NewHandlers wires the manual gate into the generated tool surface.
//
// A generated wire type is not an internal type. The mapping is explicit
// rather than a JSON round trip, so a schema that changes shape is a compile
// error here instead of a field that silently stops arriving.
//
// The forge section of a run is not mapped. forge owns TestReport and Artifact
// and a gate never reads them, so carrying them through would copy someone
// else's types for nothing.
func NewHandlers() Handlers {
	ctrl := gatecontroller.New(fsadapter.New(), "ci-gate-manual")

	return Handlers{
		Declare: func(_ context.Context, in DeclareInput) (*DeclareOutput, error) {
			out, err := ctrl.Declare(in.Spec)
			if err != nil {
				return nil, err
			}

			return &DeclareOutput{Resources: fromResources(out.Resources)}, nil
		},
		Evaluate: func(_ context.Context, in GateInput) (*GateResult, error) {
			out, err := ctrl.Evaluate(citypes.GateInput{Run: toRun(in.Run), Spec: in.Spec})
			if err != nil {
				return nil, err
			}

			return &GateResult{
				Alias:   out.Alias,
				Status:  Status(out.Status),
				Message: out.Message,
			}, nil
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
