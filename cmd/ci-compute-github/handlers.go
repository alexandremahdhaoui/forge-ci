package main

import (
	"context"
	"os"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/workflowcontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

// NewHandlers wires the github compute into the generated tool surface.
//
// The API client is built per run from the parsed spec: base URL and
// token variable come from the pipeline, and the token itself is read
// from the environment - it never crosses the wire.
func NewHandlers() Handlers {
	ctrl := workflowcontroller.New(func(spec workflowcontroller.Spec) githubadapter.API {
		return githubadapter.New(nil, spec.APIBaseURL, os.Getenv(spec.TokenEnv))
	}, nil, nil)

	return Handlers{
		Declare: func(_ context.Context, in DeclareInput) (*DeclareOutput, error) {
			out, err := ctrl.Declare(in.Spec)
			if err != nil {
				return nil, err
			}

			return &DeclareOutput{Resources: fromResources(out.Resources)}, nil
		},
		Run: func(ctx context.Context, in RunInput) (*RunOutput, error) {
			out, err := ctrl.Run(ctx, toRunInput(in))
			if err != nil {
				return nil, err
			}

			return &RunOutput{
				Status:  Status(out.Status),
				Message: out.Message,
				Output:  out.Output,
			}, nil
		},
	}
}

func toRunInput(in RunInput) citypes.RunInput {
	targets := make([]citypes.Target, 0, len(in.Targets))
	for _, t := range in.Targets {
		targets = append(targets, citypes.Target{
			Alias: t.Alias, Forge: t.Forge, ForgeCI: t.ForgeCI, In: t.In,
		})
	}

	repos := make([]citypes.RepoCheckout, 0, len(in.Repos))
	for _, r := range in.Repos {
		repos = append(repos, citypes.RepoCheckout{Name: r.Name, Path: r.Path, SHA: r.Sha})
	}

	return citypes.RunInput{
		Revision: in.Revision,
		Stage:    in.Stage,
		Substage: in.Substage,
		Targets:  targets,
		Params:   in.Params,
		Repos:    repos,
		Root:     in.Root,
		Spec:     in.Spec,
	}
}

func fromResources(in []citypes.Resource) []Resource {
	out := make([]Resource, 0, len(in))

	for _, r := range in {
		out = append(out, Resource{Kind: r.Kind, Name: r.Name, Spec: r.Spec})
	}

	return out
}
