package main

import (
	"context"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/gatecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/cienginekit"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var Version = "dev"

type declareInput struct {
	Spec map[string]any `json:"spec,omitempty"`
}

func main() {
	ctrl := gatecontroller.New(fsadapter.New(), "ci-gate-manual")

	cienginekit.Engine{
		Name:    "ci-gate-manual",
		Version: Version,
		Tools: []cienginekit.Tool{
			cienginekit.NewTool("declare", "Report the resources this gate needs.",
				func(_ context.Context, in declareInput) (citypes.DeclareOutput, error) {
					return ctrl.Declare(in.Spec)
				}),
			cienginekit.NewTool("evaluate", "Pass when the approval file exists, otherwise wait.",
				func(_ context.Context, in citypes.GateInput) (citypes.GateResult, error) {
					return ctrl.Evaluate(in)
				}),
		},
	}.Run()
}
