package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/forgeadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactdircontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/computecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

func NewHandlers() Handlers {
	files := fsadapter.New()
	runner := execadapter.New()
	ctrl, built := computecontroller.New(runner, forgeadapter.New(files), files, gitadapter.New(runner), time.Now)
	artifactDir := artifactdircontroller.New(files)

	return Handlers{
		Declare: func(_ context.Context, in DeclareInput) (*DeclareOutput, error) {
			if built != nil {
				return nil, built
			}

			out, err := ctrl.Declare(in.Spec)
			if err != nil {
				return nil, err
			}

			return &DeclareOutput{Resources: fromResources(out.Resources)}, nil
		},
		Run: func(ctx context.Context, in RunInput) (*RunOutput, error) {
			if built != nil {
				return nil, built
			}

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

			out, err := artifactDir.Put(citypes.ArtifactPutInput{
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

			out, err := artifactDir.Get(citypes.ArtifactGetInput{
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
