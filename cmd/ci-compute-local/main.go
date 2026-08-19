package main

import (
	"context"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/forgeadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/computecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/cienginekit"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var Version = "dev"

type declareInput struct {
	Spec map[string]any `json:"spec,omitempty"`
}

func main() {
	ctrl := computecontroller.New(execadapter.New(), forgeadapter.New(fsadapter.New()), nil)

	cienginekit.Engine{
		Name:    "ci-compute-local",
		Version: Version,
		Tools: []cienginekit.Tool{
			cienginekit.NewTool("declare", "Report the resources this compute target needs.",
				func(_ context.Context, in declareInput) (citypes.DeclareOutput, error) {
					return ctrl.Declare(in.Spec)
				}),
			cienginekit.NewTool("run", "Run a substage's targets on this machine and harvest the forge artifact store.",
				func(ctx context.Context, in citypes.RunInput) (citypes.RunOutput, error) {
					return ctrl.Run(ctx, in)
				}),
		},
	}.Run()
}
