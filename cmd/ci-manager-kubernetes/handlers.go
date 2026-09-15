package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/helmadapter"
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

			storage, err := declaredStorage(in.Spec)
			if err != nil {
				return nil, fmt.Errorf("reading the spec of manager %s: %w", in.Manager, err)
			}

			input := toReconcileInput(in)

			releases, err := releaseClient(input, storage)
			if err != nil {
				return nil, fmt.Errorf("building the helm client of manager %s: %w", in.Manager, err)
			}

			realizer, err := managercontroller.NewKubernetesRealizer(ctx, cluster, releases)
			if err != nil {
				return nil, fmt.Errorf("building the realizer of manager %s: %w", in.Manager, err)
			}

			out, err := managercontroller.New(realizer, fs).Reconcile(input)
			if err != nil {
				return nil, err
			}

			return fromReconcileOutput(out), nil
		},
	}
}

func declaredStorage(spec map[string]any) (string, error) {
	storage, err := citypes.SpecString(spec, "storage")
	if err != nil {
		return "", err
	}

	if storage == "" {
		return helmadapter.StorageSecrets, nil
	}

	if !slices.Contains(helmadapter.Storages, storage) {
		return "", fmt.Errorf(
			"reading spec.storage: it names %q, and helm keeps its release records in %s",
			storage, strings.Join(helmadapter.Storages, " or "))
	}

	return storage, nil
}

func releaseClient(in citypes.ReconcileInput, storage string) (helmadapter.Releases, error) {
	for _, r := range in.Resources {
		if managercontroller.Realizes(in, r) && r.Kind == managercontroller.KindHelmRelease {
			return helmadapter.New(storage)
		}
	}

	return helmadapter.Releases{}, nil
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
