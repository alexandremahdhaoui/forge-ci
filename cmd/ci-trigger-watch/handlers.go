package main

import (
	"context"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/triggercontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

// NewHandlers wires the watch trigger into the generated tool surface.
//
// A generated wire type is not an internal type. The mapping is explicit
// rather than a JSON round trip, so a schema that changes shape is a compile
// error here instead of a field that silently stops arriving.
func NewHandlers() Handlers {
	ctrl := triggercontroller.New(gitadapter.New(execadapter.New()))

	return Handlers{
		Declare: func(_ context.Context, in DeclareInput) (*DeclareOutput, error) {
			out, err := ctrl.Declare(in.Spec)
			if err != nil {
				return nil, err
			}

			return &DeclareOutput{Resources: fromResources(out.Resources)}, nil
		},
		Poll: func(ctx context.Context, in DeclareInput) (*TriggerOutput, error) {
			out, err := ctrl.Poll(ctx, in.Spec)
			if err != nil {
				return nil, err
			}

			return &TriggerOutput{
				Changed:     out.Changed,
				Reason:      out.Reason,
				Fingerprint: out.Fingerprint,
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
