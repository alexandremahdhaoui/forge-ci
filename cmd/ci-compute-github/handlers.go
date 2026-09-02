package main

import (
	"context"
	"encoding/json"
	"os"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/computecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/workflowcontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
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

	// put and get are the same controller the local engine runs: the files
	// live under the root either way, and the rendered workflow is what
	// carries that directory between jobs.
	files := computecontroller.New(nil, nil, nil)

	return Handlers{
		Declare: func(_ context.Context, in DeclareInput) (*DeclareOutput, error) {
			out, err := ctrl.Declare(in.Spec, in.Root)
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
		Put: func(_ context.Context, in ArtifactPutInput) (*ArtifactPutOutput, error) {
			artifacts, err := toArtifacts(in.Artifacts)
			if err != nil {
				return nil, err
			}

			out, err := files.Put(fsadapter.New(), citypes.ArtifactPutInput{
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

			out, err := files.Get(fsadapter.New(), citypes.ArtifactGetInput{
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
			Alias: t.Alias, Forge: t.Forge, ForgeCI: t.ForgeCI, In: t.In,
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
