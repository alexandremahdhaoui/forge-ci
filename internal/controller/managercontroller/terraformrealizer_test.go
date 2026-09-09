package managercontroller_test

import (
	"errors"
	"testing"

	tfjson "github.com/hashicorp/terraform-json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/managercontrollermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	theModuleDir = "terraform/home-record"

	reportsChanges = true
	reportsNone    = false
)

func terraformRealizer(t *testing.T) (managercontroller.TerraformRealizer, *managercontrollermock.MockTerraform) {
	t.Helper()

	terraform := managercontrollermock.NewMockTerraform(t)

	return managercontroller.NewTerraformRealizer(t.Context(), terraform), terraform
}

func rootModule() citypes.Resource {
	return citypes.Resource{
		Kind: managercontroller.KindRootModule,
		Name: "home-record",
		Spec: map[string]any{"dir": theModuleDir},
	}
}

func planOf(actions ...tfjson.Actions) *tfjson.Plan {
	plan := &tfjson.Plan{}
	for _, action := range actions {
		plan.ResourceChanges = append(plan.ResourceChanges, &tfjson.ResourceChange{
			Address: "aws_route53_record.home",
			Change:  &tfjson.Change{Actions: action},
		})
	}

	return plan
}

func planOfChanges(changes ...*tfjson.Change) *tfjson.Plan {
	plan := &tfjson.Plan{}
	for _, change := range changes {
		plan.ResourceChanges = append(plan.ResourceChanges, &tfjson.ResourceChange{
			Address: "aws_route53_record.home",
			Change:  change,
		})
	}

	return plan
}

func TestTheTerraformRealizerNamesItselfTerraform(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "terraform", managercontroller.NewTerraformRealizer(t.Context(), nil).Kind())
}

func TestTheTerraformRealizerRefusesAKindItDoesNotKnowByName(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewTerraformRealizer(t.Context(), nil)

	_, err := r.Realize(citypes.Resource{Kind: "state-file", Name: "home"}, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot realize kind "state-file"`)
	assert.Contains(t, err.Error(), managercontroller.KindRootModule)
}

func TestTheTerraformRealizerRefusesARootModuleThatNamesNoDirectory(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewTerraformRealizer(t.Context(), nil)

	_, err := r.Realize(citypes.Resource{Kind: managercontroller.KindRootModule, Name: "home"}, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.dir is required")
}

func TestTheTerraformRealizerRefusesADirectoryThatIsNotAString(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewTerraformRealizer(t.Context(), nil)

	_, err := r.Realize(citypes.Resource{
		Kind: managercontroller.KindRootModule,
		Name: "home",
		Spec: map[string]any{"dir": 7},
	}, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading spec.dir")
	assert.Contains(t, err.Error(), "a string is required")
}

func TestTheTerraformRealizerRefusesToWorkWithNoTerraformBehindIt(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewTerraformRealizer(t.Context(), nil)

	_, err := r.Realize(rootModule(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "planning the root module at "+theModuleDir)
	assert.Contains(t, err.Error(), "carries no terraform yet")
}

func TestTheTerraformRealizerKeepsARootModuleWhosePlanReportsNoChangeAtAll(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsNone, &tfjson.Plan{}, nil)

	action, err := r.Realize(rootModule(), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept the root module at "+theModuleDir, action.Text)
	assert.False(t, action.Changed)
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestTheTerraformRealizerKeepsARootModuleWhosePlanHoldsOnlyNoOpChanges(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsNone,
		planOf(tfjson.Actions{tfjson.ActionNoop}, tfjson.Actions{tfjson.ActionNoop}), nil)

	action, err := r.Realize(rootModule(), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept the root module at "+theModuleDir, action.Text)
	assert.False(t, action.Changed)
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestTheTerraformRealizerAppliesARootModuleWhosePlanHoldsARealChange(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsChanges,
		planOf(tfjson.Actions{tfjson.ActionNoop}, tfjson.Actions{tfjson.ActionUpdate}), nil)
	terraform.EXPECT().Apply(mock.Anything, theModuleDir).Return(nil)

	action, err := r.Realize(rootModule(), plain)
	require.NoError(t, err)
	assert.Equal(t, "applied 1 planned changes to the root module at "+theModuleDir, action.Text)
	assert.True(t, action.Changed)
}

func TestTheTerraformRealizerAppliesAResourceTerraformWillReplace(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsChanges,
		planOf(tfjson.Actions{tfjson.ActionDelete, tfjson.ActionCreate}), nil)
	terraform.EXPECT().Apply(mock.Anything, theModuleDir).Return(nil)

	action, err := r.Realize(rootModule(), plain)
	require.NoError(t, err)
	assert.Equal(t, "applied 1 planned changes to the root module at "+theModuleDir, action.Text)
	assert.True(t, action.Changed)
}

func TestTheTerraformRealizerAppliesAResourceTerraformWillDestroy(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsChanges,
		planOf(tfjson.Actions{tfjson.ActionDelete}), nil)
	terraform.EXPECT().Apply(mock.Anything, theModuleDir).Return(nil)

	action, err := r.Realize(rootModule(), plain)
	require.NoError(t, err)
	assert.Equal(t, "applied 1 planned changes to the root module at "+theModuleDir, action.Text)
	assert.True(t, action.Changed)
}

func TestTheTerraformRealizerAppliesAResourceTerraformWillForget(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsChanges,
		planOf(tfjson.Actions{tfjson.ActionForget}), nil)
	terraform.EXPECT().Apply(mock.Anything, theModuleDir).Return(nil)

	action, err := r.Realize(rootModule(), plain)
	require.NoError(t, err)
	assert.Equal(t, "applied 1 planned changes to the root module at "+theModuleDir, action.Text)
	assert.True(t, action.Changed)
}

func TestTheTerraformRealizerAppliesAnImportOfAResourceThatAlreadyMatchesTheModule(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsChanges, planOfChanges(&tfjson.Change{
		Actions:   tfjson.Actions{tfjson.ActionNoop},
		Importing: &tfjson.Importing{ID: "Z1234/home.songe.example/A"},
	}), nil)
	terraform.EXPECT().Apply(mock.Anything, theModuleDir).Return(nil)

	action, err := r.Realize(rootModule(), plain)
	require.NoError(t, err)
	assert.Equal(t, "applied 1 planned changes to the root module at "+theModuleDir, action.Text)
	assert.True(t, action.Changed)
}

func TestTheTerraformRealizerKeepsARootModuleWhosePlanOnlyReadsADataSource(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsNone,
		planOf(tfjson.Actions{tfjson.ActionRead}, tfjson.Actions{tfjson.ActionNoop}), nil)

	action, err := r.Realize(rootModule(), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept the root module at "+theModuleDir, action.Text)
	assert.False(t, action.Changed)
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestTheTerraformRealizerRefusesAPlanWhoseExitCodeSaysThereIsWorkAndWhoseDocumentHoldsNone(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsChanges,
		planOf(tfjson.Actions{tfjson.ActionNoop}, tfjson.Actions{tfjson.ActionRead}), nil)

	_, err := r.Realize(rootModule(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "planning the root module at "+theModuleDir)
	assert.Contains(t, err.Error(), "terraform reported changes and the plan document counts 0")
	assert.Contains(t, err.Error(), "naming no-op and read")
	assert.Contains(t, err.Error(), "refuses a plan its two answers do not agree on")
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestTheTerraformRealizerRefusesAPlanWhoseDocumentHoldsWorkAndWhoseExitCodeSaysThereIsNone(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsNone,
		planOf(tfjson.Actions{tfjson.ActionCreate}), nil)

	_, err := r.Realize(rootModule(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "planning the root module at "+theModuleDir)
	assert.Contains(t, err.Error(), "terraform reported no changes and the plan document counts 1")
	assert.Contains(t, err.Error(), "naming create")
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestARefusedPlanNamesTheImportItSawAlongsideTheAction(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsNone, planOfChanges(&tfjson.Change{
		Actions:   tfjson.Actions{tfjson.ActionNoop},
		Importing: &tfjson.Importing{ID: "Z1234/home.songe.example/A"},
	}), nil)

	_, err := r.Realize(rootModule(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "naming no-op+import")
}

func TestARefusedPlanHoldingNoChangeAtAllSaysSo(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsChanges, &tfjson.Plan{}, nil)

	_, err := r.Realize(rootModule(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "naming no change at all")
}

func TestTheTerraformRealizerRefusesAPlanTerraformCouldNotComplete(t *testing.T) {
	r, terraform := terraformRealizer(t)
	incomplete := false
	plan := planOf(tfjson.Actions{tfjson.ActionNoop})
	plan.Complete = &incomplete
	plan.DeferredChanges = []*tfjson.DeferredResourceChange{
		{Reason: "provider_config_unknown"},
		{Reason: "resource_config_unknown"},
	}
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsChanges, plan, nil)

	_, err := r.Realize(rootModule(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "planning the root module at "+theModuleDir)
	assert.Contains(t, err.Error(), "incomplete plan holding 2 deferred changes")
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestAnIncompletePlanIsRefusedBeforeTheTwoAnswersAreCompared(t *testing.T) {
	r, terraform := terraformRealizer(t)
	incomplete := false
	plan := planOf(tfjson.Actions{tfjson.ActionCreate})
	plan.Complete = &incomplete
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsNone, plan, nil)

	_, err := r.Realize(rootModule(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "incomplete plan holding 0 deferred changes")
	assert.NotContains(t, err.Error(), "do not agree")
}

func TestTheTerraformRealizerReadsAPlanTerraformCompleted(t *testing.T) {
	r, terraform := terraformRealizer(t)
	complete := true
	plan := planOf(tfjson.Actions{tfjson.ActionCreate})
	plan.Complete = &complete
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsChanges, plan, nil)
	terraform.EXPECT().Apply(mock.Anything, theModuleDir).Return(nil)

	action, err := r.Realize(rootModule(), plain)
	require.NoError(t, err)
	assert.True(t, action.Changed)
}

func TestTheTerraformRealizerCountsAnOutputChangeAsARealChange(t *testing.T) {
	r, terraform := terraformRealizer(t)
	plan := &tfjson.Plan{OutputChanges: map[string]*tfjson.Change{
		"address": {Actions: tfjson.Actions{tfjson.ActionUpdate}},
		"zone":    {Actions: tfjson.Actions{tfjson.ActionNoop}},
	}}
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsChanges, plan, nil)
	terraform.EXPECT().Apply(mock.Anything, theModuleDir).Return(nil)

	action, err := r.Realize(rootModule(), plain)
	require.NoError(t, err)
	assert.Equal(t, "applied 1 planned changes to the root module at "+theModuleDir, action.Text)
	assert.True(t, action.Changed)
}

func TestTheTerraformRealizerReportsTheDirectoryOfAnInitThatFailed(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(errors.New("no backend credentials"))

	_, err := r.Realize(rootModule(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initializing the root module at "+theModuleDir)
	assert.Contains(t, err.Error(), "no backend credentials")
	terraform.AssertNotCalled(t, "Plan", mock.Anything, mock.Anything)
}

func TestTheTerraformRealizerReportsTheDirectoryOfAPlanThatFailed(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).
		Return(reportsNone, nil, errors.New("the zone does not exist"))

	_, err := r.Realize(rootModule(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "planning the root module at "+theModuleDir)
	assert.Contains(t, err.Error(), "the zone does not exist")
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestTheTerraformRealizerRefusesAPlanTerraformNeverWrote(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsNone, nil, nil)

	_, err := r.Realize(rootModule(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "planning the root module at "+theModuleDir)
	assert.Contains(t, err.Error(), "terraform answered no plan document")
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestTheTerraformRealizerReportsTheDirectoryOfAnApplyThatFailed(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsChanges,
		planOf(tfjson.Actions{tfjson.ActionCreate}), nil)
	terraform.EXPECT().Apply(mock.Anything, theModuleDir).Return(errors.New("the record is taken"))

	_, err := r.Realize(rootModule(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "applying the root module at "+theModuleDir)
	assert.Contains(t, err.Error(), "the record is taken")
}

func TestADryRunPlansTheRootModuleAndAppliesNothing(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsChanges,
		planOf(tfjson.Actions{tfjson.ActionCreate}), nil)

	action, err := r.Realize(rootModule(), managercontroller.Options{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t,
		"would apply 1 planned changes to the root module at "+theModuleDir, action.Text)
	assert.False(t, action.Changed)
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestADryRunOfARootModuleWithNothingToChangeAnswersKept(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsNone,
		planOf(tfjson.Actions{tfjson.ActionNoop}), nil)

	action, err := r.Realize(rootModule(), managercontroller.Options{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, "kept the root module at "+theModuleDir, action.Text)
	assert.False(t, action.Changed)
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestADryRunRefusesAPlanTheTwoAnswersDoNotAgreeOn(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsChanges,
		planOf(tfjson.Actions{tfjson.ActionNoop}), nil)

	_, err := r.Realize(rootModule(), managercontroller.Options{DryRun: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not agree on")
}

func TestForceAppliesARootModuleWhosePlanHoldsNothingToChange(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsNone,
		planOf(tfjson.Actions{tfjson.ActionNoop}), nil)
	terraform.EXPECT().Apply(mock.Anything, theModuleDir).Return(nil)

	action, err := r.Realize(rootModule(), managercontroller.Options{Force: true})
	require.NoError(t, err)
	assert.Equal(t, "applied 0 planned changes to the root module at "+theModuleDir, action.Text)
	assert.True(t, action.Changed)
}

func TestForceStillRefusesAPlanTheTwoAnswersDoNotAgreeOn(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsChanges,
		planOf(tfjson.Actions{tfjson.ActionNoop}), nil)

	_, err := r.Realize(rootModule(), managercontroller.Options{Force: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not agree on")
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestForceInADryRunStillAppliesNothing(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(reportsNone,
		planOf(tfjson.Actions{tfjson.ActionNoop}), nil)

	action, err := r.Realize(rootModule(), managercontroller.Options{DryRun: true, Force: true})
	require.NoError(t, err)
	assert.Equal(t,
		"would apply 0 planned changes to the root module at "+theModuleDir, action.Text)
	assert.False(t, action.Changed)
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}
