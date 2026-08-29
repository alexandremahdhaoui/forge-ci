package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/containeradapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/containercontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

// NewHandlers wires the container release into the generated tool surface.
//
// A generated wire type is not an internal type, so the mapping happens here,
// explicit field by field. The artifacts are the exception: forge owns that
// record and its schema lives in forge, so a copy here would drift from what
// forge emits.
func NewHandlers() Handlers {
	ctrl := containercontroller.New()

	return Handlers{
		Declare: func(_ context.Context, in DeclareInput) (*DeclareOutput, error) {
			out, err := ctrl.Declare(in.Spec)
			if err != nil {
				return nil, err
			}

			return &DeclareOutput{Resources: fromResources(out.Resources)}, nil
		},
		Publish: func(_ context.Context, in ArtifactInput) (*ArtifactOutput, error) {
			artifacts, err := toArtifacts(in.Artifacts)
			if err != nil {
				return nil, err
			}

			out, err := publish(ctrl, registryFor(in.Spec), citypes.ArtifactInput{
				Revision:  in.Revision,
				Version:   in.Version,
				TagPrefix: in.TagPrefix,
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

// registryFor reads the credential the spec names. GITHUB_TOKEN by default,
// which GitHub Actions injects, so there is no secret to create, seal or
// rotate.
func registryFor(spec map[string]any) containeradapter.Registry {
	tokenEnv, _ := spec["tokenEnv"].(string)
	if tokenEnv == "" {
		tokenEnv = "GITHUB_TOKEN"
	}

	return &containeradapter.Remote{Token: os.Getenv(tokenEnv)}
}

// publish carries out what the controller decided. The decision is not made
// here, so what gets published is testable without a registry.
func publish(
	ctrl *containercontroller.Controller,
	registry containeradapter.Registry,
	in citypes.ArtifactInput,
) (citypes.ArtifactOutput, error) {
	plan, err := ctrl.Plan(in)
	if err != nil {
		return citypes.ArtifactOutput{Reason: err.Error()}, err
	}

	out := citypes.ArtifactOutput{Tagged: []string{}}

	for _, path := range plan.Layouts {
		log.Printf("publishing %s as %v", path, plan.Tags)

		if err := registry.Push(path, plan.Tags, plan.Labels); err != nil {
			return out, fmt.Errorf("publishing %s: %w", plan.Image, err)
		}
	}

	out.Published = true
	out.URL = plan.Tags[0]
	out.Tagged = plan.Tags

	return out, nil
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
