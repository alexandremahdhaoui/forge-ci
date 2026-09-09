package managercontroller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	tfjson "github.com/hashicorp/terraform-json"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const KindRootModule = "root-module"

type Terraform interface {
	Init(ctx context.Context, dir string) error
	Plan(ctx context.Context, dir string) (reportsChanges bool, document *tfjson.Plan, err error)
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
			"initializing the root module at %s: this manager carries no terraform yet", dir)
	}

	if err := r.terraform.Init(r.ctx, dir); err != nil {
		return Action{}, fmt.Errorf("initializing the root module at %s: %w", dir, err)
	}

	reportsChanges, document, err := r.terraform.Plan(r.ctx, dir)
	if err != nil {
		return Action{}, fmt.Errorf("planning the root module at %s: %w", dir, err)
	}

	changes, err := plannedWork(reportsChanges, document)
	if err != nil {
		return Action{}, fmt.Errorf("planning the root module at %s: %w", dir, err)
	}

	if changes == 0 && !opts.Force {
		return Kept("kept the root module at " + dir), nil
	}

	if opts.DryRun {
		return Kept(opts.would(countedAs("apply", changes, dir))), nil
	}

	if err := r.terraform.Apply(r.ctx, dir); err != nil {
		return Action{}, fmt.Errorf("applying the root module at %s: %w", dir, err)
	}

	return Did(countedAs("applied", changes, dir)), nil
}

func countedAs(verb string, changes int, dir string) string {
	return fmt.Sprintf(
		"%s %d planned changes to the root module at %s, counted one per planned change where terraform's own summary counts a replace as two",
		verb, changes, dir)
}

func plannedWork(reportsChanges bool, document *tfjson.Plan) (int, error) {
	if document == nil {
		return 0, errors.New("terraform answered no plan document")
	}

	if document.Complete != nil && !*document.Complete {
		return 0, fmt.Errorf(
			"terraform answered an incomplete plan holding %d deferred changes, %s",
			len(document.DeferredChanges), deferredReasons(document))
	}

	if unknown := unknownActions(document); len(unknown) > 0 {
		return 0, fmt.Errorf(
			"terraform answered actions this manager does not know, %s. it knows %s, and it refuses a plan it cannot read rather than guess whether that plan holds work",
			strings.Join(unknown, ", "), knownActionNames())
	}

	changes := plannedChanges(document)

	if reportsChanges != (changes > 0) {
		return 0, fmt.Errorf(
			"terraform reported %s and the plan document counts %d, naming %s. this manager refuses a plan its two answers do not agree on rather than guess which one is right",
			reportedAs(reportsChanges), changes, actionsSeen(document))
	}

	return changes, nil
}

func deferredReasons(document *tfjson.Plan) string {
	seen := map[string]bool{}

	for _, deferred := range document.DeferredChanges {
		if deferred == nil {
			continue
		}

		if deferred.Reason == "" {
			seen["an unnamed reason"] = true

			continue
		}

		seen[deferred.Reason] = true
	}

	if len(seen) == 0 {
		return "naming no reason"
	}

	return "naming " + strings.Join(sortedNames(seen), " and ")
}

var knownActions = []tfjson.Action{
	tfjson.ActionNoop,
	tfjson.ActionCreate,
	tfjson.ActionRead,
	tfjson.ActionUpdate,
	tfjson.ActionDelete,
	tfjson.ActionForget,
}

func knownActionNames() string {
	names := make([]string, 0, len(knownActions))
	for _, action := range knownActions {
		names = append(names, string(action))
	}

	return strings.Join(names, ", ")
}

func isKnownAction(action tfjson.Action) bool {
	for _, known := range knownActions {
		if known == action {
			return true
		}
	}

	return false
}

func unknownActions(document *tfjson.Plan) []string {
	seen := map[string]bool{}

	for _, resource := range document.ResourceChanges {
		if resource == nil || resource.Change == nil {
			continue
		}

		if named := unknownActionsOf(resource.Address, resource.Change); named != "" {
			seen[named] = true
		}
	}

	for address, output := range document.OutputChanges {
		if output == nil {
			continue
		}

		if named := unknownActionsOf("output "+address, output); named != "" {
			seen[named] = true
		}
	}

	return sortedNames(seen)
}

func unknownActionsOf(address string, change *tfjson.Change) string {
	if address == "" {
		address = "a change terraform gave no address"
	}

	if len(change.Actions) == 0 {
		return address + " holds an empty action list"
	}

	unknown := make([]string, 0, len(change.Actions))

	for _, action := range change.Actions {
		if !isKnownAction(action) {
			unknown = append(unknown, strconv.Quote(string(action)))
		}
	}

	if len(unknown) == 0 {
		return ""
	}

	return address + " holds " + strings.Join(unknown, " and ")
}

func sortedNames(seen map[string]bool) []string {
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

func reportedAs(reportsChanges bool) string {
	if reportsChanges {
		return "changes"
	}

	return "no changes"
}

func actionsSeen(document *tfjson.Plan) string {
	seen := map[string]bool{}

	for _, resource := range document.ResourceChanges {
		if resource != nil && resource.Change != nil {
			seen[actionsOf(resource.Change)] = true
		}
	}

	for _, output := range document.OutputChanges {
		if output != nil {
			seen[actionsOf(output)] = true
		}
	}

	if len(seen) == 0 {
		return "no change at all"
	}

	return strings.Join(sortedNames(seen), " and ")
}

func actionsOf(change *tfjson.Change) string {
	parts := make([]string, 0, len(change.Actions))
	for _, action := range change.Actions {
		parts = append(parts, string(action))
	}

	if change.Importing != nil {
		parts = append(parts, "import")
	}

	if len(parts) == 0 {
		return "nothing"
	}

	return strings.Join(parts, "+")
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

	return !change.Actions.NoOp()
}
