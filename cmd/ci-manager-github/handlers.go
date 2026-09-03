package main

import (
	"context"
	"os"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

// NewHandlers wires the github manager into the generated tool surface.
//
// The realizer is built per call: the API base and the token variable come
// from the manager spec, and the token itself is read from the environment
// at realize time - it never crosses the wire and never lands in state.
func NewHandlers() Handlers {
	fs := fsadapter.New()

	return Handlers{
		Reconcile: func(ctx context.Context, in ReconcileInput) (*ReconcileOutput, error) {
			api := githubadapter.New(nil, baseURL(in.Spec), os.Getenv(tokenEnv(in.Spec)))
			root, _ := in.Spec["root"].(string)
			git := gitadapter.New(execadapter.New())
			ctrl := managercontroller.New(
				managercontroller.NewGitHubRealizer(ctx, fs, api, git, root), fs)

			out, err := ctrl.Reconcile(toReconcileInput(in))
			if err != nil {
				return nil, err
			}

			return fromReconcileOutput(out), nil
		},
	}
}

func baseURL(spec map[string]interface{}) string {
	base, _ := spec["apiBaseURL"].(string)

	return base
}

func tokenEnv(spec map[string]interface{}) string {
	name, _ := spec["tokenEnv"].(string)
	if name == "" {
		name = "GITHUB_TOKEN"
	}

	return name
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
