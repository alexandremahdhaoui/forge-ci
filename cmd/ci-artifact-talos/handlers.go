package main

import (
	"context"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/machineconfigcontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

func NewHandlers() Handlers {
	return newHandlers(machineconfigcontroller.New(fsadapter.New()))
}

func newHandlers(ctrl *machineconfigcontroller.Controller) Handlers {
	return Handlers{
		Declare: func(_ context.Context, in DeclareInput) (*DeclareOutput, error) {
			out, err := ctrl.Declare(citypes.DeclareInput{Spec: in.Spec, Root: in.Root})
			if err != nil {
				return nil, err
			}

			return &DeclareOutput{Resources: fromResources(out.Resources)}, nil
		},
		Publish: func(_ context.Context, _ ArtifactInput) (*ArtifactOutput, error) {
			return nil, ctrl.Publish()
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
