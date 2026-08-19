package main

import (
	"context"
	"encoding/json"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/forgeadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/computecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

// NewHandlers wires the local compute into the generated tool surface.
//
// A generated wire type is not an internal type, so the mapping happens here.
// It is explicit field by field, except for the forge section: forge owns
// TestReport and Artifact, their schema lives in forge, and a copy here would
// drift from what forge emits. That section travels as the JSON it already is.
func NewHandlers() Handlers {
	ctrl := computecontroller.New(execadapter.New(), forgeadapter.New(fsadapter.New()), nil)

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

			result := &RunOutput{
				Status:  Status(out.Status),
				Message: out.Message,
				Output:  out.Output,
			}

			forgeSection, err := asObject(out.Forge)
			if err != nil {
				return nil, err
			}

			result.Forge = forgeSection

			return result, nil
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

// asObject turns forge's own records into the JSON object the wire carries.
// Marshalling is the mapping here on purpose: the target shape is whatever
// forge emits, and describing it again is what we are avoiding.
func asObject(in *citypes.ForgeResult) (ForgeResult, error) {
	if in == nil {
		return nil, nil
	}

	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}

	var out ForgeResult

	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}

	return out, nil
}

func fromResources(in []citypes.Resource) []Resource {
	out := make([]Resource, 0, len(in))

	for _, r := range in {
		out = append(out, Resource{Kind: r.Kind, Name: r.Name, Spec: r.Spec})
	}

	return out
}
