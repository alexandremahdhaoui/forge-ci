package main

import (
	"context"
	"fmt"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/releaseadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactcontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/cienginekit"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var Version = "dev"

type declareInput struct {
	Spec map[string]any `json:"spec,omitempty"`
}

func main() {
	ctrl := artifactcontroller.New()
	publisher := releaseadapter.New(execadapter.New())

	cienginekit.Engine{
		Name:    "ci-artifact-release",
		Version: Version,
		Tools: []cienginekit.Tool{
			cienginekit.NewTool("declare", "Report the resources this release needs. It needs none.",
				func(_ context.Context, in declareInput) (citypes.DeclareOutput, error) {
					return ctrl.Declare(in.Spec)
				}),
			cienginekit.NewTool("publish", "Tag every repo of the revision and create the release.",
				func(ctx context.Context, in citypes.ArtifactInput) (citypes.ArtifactOutput, error) {
					return publish(ctx, ctrl, publisher, in)
				}),
		},
	}.Run()
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
