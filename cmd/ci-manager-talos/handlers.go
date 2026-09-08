package main

import (
	"context"
	"fmt"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/talosadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

func NewHandlers() Handlers {
	fs := fsadapter.New()

	return Handlers{
		Reconcile: func(ctx context.Context, in ReconcileInput) (*ReconcileOutput, error) {
			node, err := talosadapter.New(applyMode(in.Spec))
			if err != nil {
				return nil, fmt.Errorf("building the node client of manager %s: %w", in.Manager, err)
			}

			ctrl := managercontroller.New(
				managercontroller.NewTalosRealizer(ctx, node, talosconfigEnv(in.Spec)), fs)

			out, err := ctrl.Reconcile(toReconcileInput(in))
			if err != nil {
				return nil, err
			}

			return fromReconcileOutput(out), nil
		},
	}
}

func talosconfigEnv(spec map[string]interface{}) string {
	name, _ := spec["talosconfigEnv"].(string)
	if name == "" {
		name = "TALOSCONFIG"
	}

	return name
}

func applyMode(spec map[string]interface{}) string {
	mode, _ := spec["applyMode"].(string)

	return mode
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
		Manager:      in.Manager,
		Resources:    resources,
		Owned:        owned,
		Bootstrap:    in.Bootstrap,
		Spec:         in.Spec,
		DryRun:       in.DryRun,
		Force:        in.Force,
		CommitPrefix: in.CommitPrefix,
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

	return &ReconcileOutput{Owned: owned, Actions: actions, Changed: out.Changed, Published: out.Published}
}
