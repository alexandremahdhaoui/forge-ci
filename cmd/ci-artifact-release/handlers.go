package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/releaseadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactcontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

// NewHandlers wires the release into the generated tool surface.
//
// A generated wire type is not an internal type, so the mapping happens here.
// It is explicit field by field, except for the artifacts: forge owns that
// record, its schema lives in forge, and a copy here would drift from what
// forge emits. Those travel as the JSON they already are.
func NewHandlers() Handlers {
	ctrl := artifactcontroller.New()
	publisher := releaseadapter.New(execadapter.New())

	return Handlers{
		Declare: func(_ context.Context, in DeclareInput) (*DeclareOutput, error) {
			out, err := ctrl.Declare(in.Spec)
			if err != nil {
				return nil, err
			}

			return &DeclareOutput{Resources: fromResources(out.Resources)}, nil
		},
		Publish: func(ctx context.Context, in ArtifactInput) (*ArtifactOutput, error) {
			artifacts, err := toArtifacts(in.Artifacts)
			if err != nil {
				return nil, err
			}

			out, err := publish(ctx, ctrl, publisher, citypes.ArtifactInput{
				Revision:  in.Revision,
				Version:   in.Version,
				Repos:     in.Repos,
				Artifacts: artifacts,
				Spec:      in.Spec,
			})
			if err != nil {
				return nil, err
			}

			return &ArtifactOutput{
				Published: out.Published,
				Url:       out.URL,
				Reason:    out.Reason,
				Tagged:    out.Tagged,
			}, nil
		},
	}
}

// toArtifacts reads forge's own records off the wire. Unmarshalling is the
// mapping here on purpose: the shape is whatever forge emits, and describing
// it again is what we are avoiding.
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

func fromResources(in []citypes.Resource) []Resource {
	out := make([]Resource, 0, len(in))

	for _, r := range in {
		out = append(out, Resource{Kind: r.Kind, Name: r.Name, Spec: r.Spec})
	}

	return out
}

// publish carries out what the controller decided. The decision is not made
// here, so what gets released is testable without a network.
func publish(
	ctx context.Context,
	ctrl *artifactcontroller.Controller,
	publisher releaseadapter.Publisher,
	in citypes.ArtifactInput,
) (citypes.ArtifactOutput, error) {
	plan, err := ctrl.Plan(in)
	if err != nil {
		return citypes.ArtifactOutput{Reason: err.Error()}, err
	}

	root, _ := in.Spec["root"].(string)
	if root == "" {
		root = "."
	}

	out := citypes.ArtifactOutput{Tagged: []string{}}

	for _, tag := range plan.Tags {
		dir := root + "/" + tag.Repo

		if err := publisher.Tag(ctx, dir, plan.Version, tag.SHA); err != nil {
			return out, fmt.Errorf("releasing %s: %w", plan.Version, err)
		}

		out.Tagged = append(out.Tagged, tag.Repo)
	}

	url, err := publisher.Release(ctx, root, plan.Version, plan.Uploads)
	if err != nil {
		return out, fmt.Errorf("releasing %s: %w", plan.Version, err)
	}

	out.Published = true
	out.URL = url

	return out, nil
}
