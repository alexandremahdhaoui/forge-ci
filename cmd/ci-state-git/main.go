package main

import (
	"context"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/statecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/cienginekit"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var Version = "dev"

type declareInput struct {
	Spec map[string]any `json:"spec,omitempty"`
}

func main() {
	ctrl := statecontroller.New(fsadapter.New(), gitadapter.New(execadapter.New()))

	cienginekit.Engine{
		Name:    "ci-state-git",
		Version: Version,
		Tools: []cienginekit.Tool{
			cienginekit.NewTool("declare", "Report the directories the state repo needs.",
				func(_ context.Context, in declareInput) (citypes.DeclareOutput, error) {
					return ctrl.Declare(in.Spec)
				}),
			cienginekit.NewTool("get", "Read one record.",
				func(ctx context.Context, in citypes.StateGetInput) (citypes.StateGetOutput, error) {
					return ctrl.Get(ctx, in)
				}),
			cienginekit.NewTool("put", "Write one record and commit it.",
				func(ctx context.Context, in citypes.StatePutInput) (citypes.StateGetOutput, error) {
					return ctrl.Put(ctx, in)
				}),
			cienginekit.NewTool("list", "List the keys under one kind.",
				func(ctx context.Context, in citypes.StateGetInput) (citypes.StateListOutput, error) {
					return ctrl.List(ctx, in)
				}),
		},
	}.Run()
}
