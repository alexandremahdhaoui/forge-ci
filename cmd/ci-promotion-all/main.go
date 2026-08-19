package main

import (
	"context"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/promotioncontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/cienginekit"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var Version = "dev"

type declareInput struct {
	Spec map[string]any `json:"spec,omitempty"`
}

func main() {
	ctrl := promotioncontroller.New()

	cienginekit.Engine{
		Name:    "ci-promotion-all",
		Version: Version,
		Tools: []cienginekit.Tool{
			cienginekit.NewTool("declare", "Report the resources this promotion needs.",
				func(_ context.Context, in declareInput) (citypes.DeclareOutput, error) {
					return ctrl.Declare(in.Spec)
				}),
			cienginekit.NewTool("evaluate", "Advance when enough substages passed. spec.threshold sets how many.",
				func(_ context.Context, in citypes.PromotionInput) (citypes.PromotionOutput, error) {
					return ctrl.Evaluate(in)
				}),
		},
	}.Run()
}
