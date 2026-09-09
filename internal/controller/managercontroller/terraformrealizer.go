package managercontroller

import (
	"context"
	"errors"
	"fmt"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const KindRootModule = "root-module"

type Terraform interface {
	Init(ctx context.Context, dir string) error
	Plan(ctx context.Context, dir string) (*tfjson.Plan, error)
	Apply(ctx context.Context, dir string) error
}

type TerraformRealizer struct {
	ctx       context.Context
	terraform Terraform
}

var _ Realizer = TerraformRealizer{}

func NewTerraformRealizer(ctx context.Context, terraform Terraform) TerraformRealizer {
	return TerraformRealizer{ctx: ctx, terraform: terraform}
}

func (TerraformRealizer) Kind() string {
	return "terraform"
}

func (r TerraformRealizer) Realize(res citypes.Resource, opts Options) (Action, error) {
	switch res.Kind {
	case KindRootModule:
		return r.realizeRootModule(res, opts)
	default:
		return Action{}, fmt.Errorf(
			"the terraform manager cannot realize kind %q, it knows %s", res.Kind, KindRootModule)
	}
}

func (r TerraformRealizer) realizeRootModule(res citypes.Resource, opts Options) (Action, error) {
	dir, err := citypes.SpecString(res.Spec, "dir")
	if err != nil {
		return Action{}, err
	}

	if dir == "" {
		return Action{}, errors.New("spec.dir is required")
	}

	if r.terraform == nil {
		return Action{}, fmt.Errorf(
			"planning the root module at %s: this manager carries no terraform yet", dir)
	}

	if err := r.terraform.Init(r.ctx, dir); err != nil {
		return Action{}, fmt.Errorf("initializing the root module at %s: %w", dir, err)
	}

	plan, err := r.terraform.Plan(r.ctx, dir)
	if err != nil {
		return Action{}, fmt.Errorf("planning the root module at %s: %w", dir, err)
	}

	if plan == nil {
		return Action{}, fmt.Errorf("planning the root module at %s: terraform answered no plan", dir)
	}

	if plan.Complete != nil && !*plan.Complete {
		return Action{}, fmt.Errorf(
			"planning the root module at %s: terraform answered an incomplete plan holding %d deferred changes",
			dir, len(plan.DeferredChanges))
	}

	changes := plannedChanges(plan)

	if changes == 0 && !opts.Force {
		return Kept("kept the root module at " + dir), nil
	}

	text := fmt.Sprintf("apply %d planned changes to the root module at %s", changes, dir)

	if opts.DryRun {
		return Kept(opts.would(text)), nil
	}

	if err := r.terraform.Apply(r.ctx, dir); err != nil {
		return Action{}, fmt.Errorf("applying the root module at %s: %w", dir, err)
	}

	return Did(fmt.Sprintf("applied %d planned changes to the root module at %s", changes, dir)), nil
}

func plannedChanges(plan *tfjson.Plan) int {
	count := 0

	for _, resource := range plan.ResourceChanges {
		if resource != nil && moves(resource.Change) {
			count++
		}
	}

	for _, output := range plan.OutputChanges {
		if moves(output) {
			count++
		}
	}

	return count
}

func moves(change *tfjson.Change) bool {
	if change == nil {
		return false
	}

	if change.Importing != nil {
		return true
	}

	return !change.Actions.NoOp() && !change.Actions.Read()
}
