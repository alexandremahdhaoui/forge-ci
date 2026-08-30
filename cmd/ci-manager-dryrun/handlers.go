package main

import (
	"context"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

// NewHandlers wires the dry run manager into the generated tool surface.
//
// A generated wire type is not an internal type. The mapping below is explicit
// rather than a JSON round trip, so a schema that changes shape is a compile
// error here instead of a field that silently stops arriving.
func NewHandlers() Handlers {
	ctrl := managercontroller.New(managercontroller.NewDryRunRealizer(), fsadapter.New())

	return Handlers{
		Reconcile: func(_ context.Context, in ReconcileInput) (*ReconcileOutput, error) {
			out, err := ctrl.Reconcile(toReconcileInput(in))
			if err != nil {
				return nil, err
			}

			return fromReconcileOutput(out), nil
		},
	}
}

func toReconcileInput(in ReconcileInput) citypes.ReconcileInput {
	resources := make([]citypes.Resource, 0, len(in.Resources))
	for _, r := range in.Resources {
		resources = append(resources, citypes.Resource{
			Kind: r.Kind, Name: r.Name, BootstrapOnly: r.BootstrapOnly, Spec: r.Spec,
		})
	}

	owned := make([]citypes.Ownership, 0, len(in.Owned))
	for _, o := range in.Owned {
		owned = append(owned, citypes.Ownership{Resource: o.Resource, Manager: o.Manager})
	}

	return citypes.ReconcileInput{
		Manager:   in.Manager,
		Resources: resources,
		Owned:     owned,
		Bootstrap: in.Bootstrap,
		Spec:      in.Spec,
	}
}

func fromReconcileOutput(out citypes.ReconcileOutput) *ReconcileOutput {
	owned := make([]Ownership, 0, len(out.Owned))
	for _, o := range out.Owned {
		owned = append(owned, Ownership{Resource: o.Resource, Manager: o.Manager})
	}

	actions := out.Actions
	if actions == nil {
		actions = []string{}
	}

	return &ReconcileOutput{Owned: owned, Actions: actions, Changed: out.Changed}
}
