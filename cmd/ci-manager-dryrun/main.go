package main

import (
	"context"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/cienginekit"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var Version = "dev"

func main() {
	ctrl := managercontroller.New(managercontroller.NewDryRunRealizer(), fsadapter.New())

	cienginekit.Engine{
		Name:    "ci-manager-dryrun",
		Version: Version,
		Tools: []cienginekit.Tool{
			cienginekit.NewTool("reconcile",
				"Report what would be created without changing anything.",
				func(_ context.Context, in citypes.ReconcileInput) (citypes.ReconcileOutput, error) {
					return ctrl.Reconcile(in)
				}),
		},
	}.Run()
}
