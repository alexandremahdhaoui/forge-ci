package main

import (
	"context"
	"encoding/json"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/forgeadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/computecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
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
		Put: func(_ context.Context, in ArtifactPutInput) (*ArtifactPutOutput, error) {
			artifacts, err := toArtifacts(in.Artifacts)
			if err != nil {
				return nil, err
			}

			out, err := ctrl.Put(fsadapter.New(), citypes.ArtifactPutInput{
				Revision: in.Revision, Artifacts: artifacts, Root: in.Root, Spec: in.Spec,
			})
			if err != nil {
				return nil, err
			}

			wire, err := fromArtifacts(out.Artifacts)
			if err != nil {
				return nil, err
			}

			return &ArtifactPutOutput{Artifacts: wire}, nil
		},
		Get: func(_ context.Context, in ArtifactGetInput) (*ArtifactGetOutput, error) {
			artifacts, err := toArtifacts(in.Artifacts)
			if err != nil {
				return nil, err
			}

			out, err := ctrl.Get(fsadapter.New(), citypes.ArtifactGetInput{
				Revision: in.Revision, Artifacts: artifacts, Root: in.Root, Spec: in.Spec,
			})
			if err != nil {
				return nil, err
			}

			wire, err := fromArtifacts(out.Artifacts)
			if err != nil {
				return nil, err
			}

			return &ArtifactGetOutput{Artifacts: wire}, nil
		},
	}
}

func toRunInput(in RunInput) citypes.RunInput {
	targets := make([]citypes.Target, 0, len(in.Targets))
	for _, t := range in.Targets {
		targets = append(targets, citypes.Target{
			Alias: t.Alias, Binary: t.Binary, Args: t.Args, In: t.In,
		})
	}

	repos := make([]citypes.RepoCheckout, 0, len(in.Repos))
	for _, r := range in.Repos {
		repos = append(repos, citypes.RepoCheckout{Name: r.Name, Path: r.Path, SHA: r.Sha, Needs: r.Needs})
	}

	return citypes.RunInput{
		Revision: in.Revision,
		Version:  in.Version,
		Stage:    in.Stage,
		Substage: in.Substage,
		Sync:     in.Sync,
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
		out = append(out, Resource{
			Kind: r.Kind, Name: r.Name, BootstrapOnly: r.BootstrapOnly, Spec: r.Spec,
		})
	}

	return out
}

// toArtifacts reads forge's own records off the wire and fromArtifacts puts
// them back. Marshalling is the mapping on purpose: the shape is whatever
// forge emits, and describing it again is what we are avoiding.
func toArtifacts(in []ForgeArtifact) ([]forge.Artifact, error) {
	out := make([]forge.Artifact, 0, len(in))

	for _, a := range in {
		raw, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}

		var artifact forge.Artifact

		if err := json.Unmarshal(raw, &artifact); err != nil {
			return nil, err
		}

		out = append(out, artifact)
	}

	return out, nil
}

func fromArtifacts(in []forge.Artifact) ([]ForgeArtifact, error) {
	out := make([]ForgeArtifact, 0, len(in))

	for _, a := range in {
		raw, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}

		var wire ForgeArtifact

		if err := json.Unmarshal(raw, &wire); err != nil {
			return nil, err
		}

		out = append(out, wire)
	}

	return out, nil
}
