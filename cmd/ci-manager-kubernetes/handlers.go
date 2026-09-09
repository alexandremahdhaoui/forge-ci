package main

import (
	"context"
	"fmt"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/kubernetesadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

func NewHandlers() Handlers {
	fs := fsadapter.New()

	return Handlers{
		Reconcile: func(ctx context.Context, in ReconcileInput) (*ReconcileOutput, error) {
			cluster, err := kubernetesadapter.New()
			if err != nil {
				return nil, fmt.Errorf("building the cluster client of manager %s: %w", in.Manager, err)
			}

			ctrl := managercontroller.New(
				managercontroller.NewKubernetesRealizer(ctx, cluster), fs)

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
