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
	fs := fsadapter.New()
	ctrl := managercontroller.New(managercontroller.NewLocalRealizer(fs), fs)

	cienginekit.Engine{
		Name:    "ci-manager-local",
		Version: Version,
		Tools: []cienginekit.Tool{
			cienginekit.NewTool("reconcile",
				"Make declared resources exist on this machine and record what was created.",
				func(_ context.Context, in citypes.ReconcileInput) (citypes.ReconcileOutput, error) {
					return ctrl.Reconcile(in)
				}),
		},
	}.Run()
}
