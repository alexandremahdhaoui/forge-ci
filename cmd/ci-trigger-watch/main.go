package main

import (
	"context"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/triggercontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/cienginekit"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var Version = "dev"

type input struct {
	Spec map[string]any `json:"spec,omitempty"`
}

func main() {
	ctrl := triggercontroller.New(gitadapter.New(execadapter.New()))

	cienginekit.Engine{
		Name:    "ci-trigger-watch",
		Version: Version,
		Tools: []cienginekit.Tool{
			cienginekit.NewTool("declare", "Report the resources this trigger needs.",
				func(_ context.Context, in input) (citypes.DeclareOutput, error) {
					return ctrl.Declare(in.Spec)
				}),
			cienginekit.NewTool("poll", "Fingerprint the watched repos and say whether they moved.",
				func(ctx context.Context, in input) (citypes.TriggerOutput, error) {
					return ctrl.Poll(ctx, in.Spec)
				}),
		},
	}.Run()
}
