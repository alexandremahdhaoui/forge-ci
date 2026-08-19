package main

import (
	"context"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/statecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

// NewHandlers wires the state controller into the generated tool surface.
//
// The generated types come from forge-revision-spec, the contract forge-ci and
// forge-factory both speak. They are the wire, so they are mapped here rather
// than reaching the controller.
func NewHandlers() Handlers {
	ctrl := statecontroller.New(fsadapter.New(), gitadapter.New(execadapter.New()))

	return Handlers{
		Declare: func(_ context.Context, in DeclareInput) (*DeclareOutput, error) {
			out, err := ctrl.Declare(in.Spec)
			if err != nil {
				return nil, err
			}

			return &DeclareOutput{Resources: fromResources(out.Resources)}, nil
		},
		Get: func(ctx context.Context, in StateGetInput) (*StateGetOutput, error) {
			out, err := ctrl.Get(ctx, citypes.StateGetInput{Kind: in.Kind, Key: in.Key, Spec: in.Spec})
			if err != nil {
				return nil, err
			}

			return &StateGetOutput{Found: out.Found, Payload: out.Payload}, nil
		},
		Put: func(ctx context.Context, in StatePutInput) (*StateGetOutput, error) {
			out, err := ctrl.Put(ctx, citypes.StatePutInput{
				Kind: in.Kind, Key: in.Key, Payload: in.Payload, Spec: in.Spec,
			})
			if err != nil {
				return nil, err
			}

			return &StateGetOutput{Found: out.Found, Payload: out.Payload}, nil
		},
		List: func(ctx context.Context, in StateGetInput) (*StateListOutput, error) {
			out, err := ctrl.List(ctx, citypes.StateGetInput{Kind: in.Kind, Key: in.Key, Spec: in.Spec})
			if err != nil {
				return nil, err
			}

			return &StateListOutput{Keys: out.Keys}, nil
		},
	}
}

func fromResources(in []citypes.Resource) []Resource {
	out := make([]Resource, 0, len(in))

	for _, r := range in {
		out = append(out, Resource{Kind: r.Kind, Name: r.Name, Spec: r.Spec})
	}

	return out
}
