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

const theModuleDir = "terraform/home-record"

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
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(&tfjson.Plan{}, nil)

	action, err := r.Realize(rootModule(), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept the root module at "+theModuleDir, action.Text)
	assert.False(t, action.Changed)
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestTheTerraformRealizerKeepsARootModuleWhosePlanHoldsOnlyNoOpChanges(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).
		Return(planOf(tfjson.Actions{tfjson.ActionNoop}, tfjson.Actions{tfjson.ActionNoop}), nil)

	action, err := r.Realize(rootModule(), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept the root module at "+theModuleDir, action.Text)
	assert.False(t, action.Changed)
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestTheTerraformRealizerAppliesARootModuleWhosePlanHoldsARealChange(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).
		Return(planOf(tfjson.Actions{tfjson.ActionNoop}, tfjson.Actions{tfjson.ActionUpdate}), nil)
	terraform.EXPECT().Apply(mock.Anything, theModuleDir).Return(nil)

	action, err := r.Realize(rootModule(), plain)
	require.NoError(t, err)
	assert.Equal(t, "applied 1 planned changes to the root module at "+theModuleDir, action.Text)
	assert.True(t, action.Changed)
}

func TestTheTerraformRealizerCountsAnOutputChangeAsARealChange(t *testing.T) {
	r, terraform := terraformRealizer(t)
	plan := &tfjson.Plan{OutputChanges: map[string]*tfjson.Change{
		"address": {Actions: tfjson.Actions{tfjson.ActionUpdate}},
		"zone":    {Actions: tfjson.Actions{tfjson.ActionNoop}},
	}}
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(plan, nil)
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
		Return(nil, errors.New("the zone does not exist"))

	_, err := r.Realize(rootModule(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "planning the root module at "+theModuleDir)
	assert.Contains(t, err.Error(), "the zone does not exist")
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestTheTerraformRealizerRefusesAPlanTerraformNeverWrote(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).Return(nil, nil)

	_, err := r.Realize(rootModule(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "planning the root module at "+theModuleDir)
	assert.Contains(t, err.Error(), "terraform answered no plan")
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestTheTerraformRealizerReportsTheDirectoryOfAnApplyThatFailed(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).
		Return(planOf(tfjson.Actions{tfjson.ActionCreate}), nil)
	terraform.EXPECT().Apply(mock.Anything, theModuleDir).Return(errors.New("the record is taken"))

	_, err := r.Realize(rootModule(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "applying the root module at "+theModuleDir)
	assert.Contains(t, err.Error(), "the record is taken")
}

func TestADryRunPlansTheRootModuleAndAppliesNothing(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).
		Return(planOf(tfjson.Actions{tfjson.ActionCreate}), nil)

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
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).
		Return(planOf(tfjson.Actions{tfjson.ActionNoop}), nil)

	action, err := r.Realize(rootModule(), managercontroller.Options{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, "kept the root module at "+theModuleDir, action.Text)
	assert.False(t, action.Changed)
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}

func TestForceAppliesARootModuleWhosePlanHoldsNothingToChange(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).
		Return(planOf(tfjson.Actions{tfjson.ActionNoop}), nil)
	terraform.EXPECT().Apply(mock.Anything, theModuleDir).Return(nil)

	action, err := r.Realize(rootModule(), managercontroller.Options{Force: true})
	require.NoError(t, err)
	assert.Equal(t, "applied 0 planned changes to the root module at "+theModuleDir, action.Text)
	assert.True(t, action.Changed)
}

func TestForceInADryRunStillAppliesNothing(t *testing.T) {
	r, terraform := terraformRealizer(t)
	terraform.EXPECT().Init(mock.Anything, theModuleDir).Return(nil)
	terraform.EXPECT().Plan(mock.Anything, theModuleDir).
		Return(planOf(tfjson.Actions{tfjson.ActionNoop}), nil)

	action, err := r.Realize(rootModule(), managercontroller.Options{DryRun: true, Force: true})
	require.NoError(t, err)
	assert.Equal(t,
		"would apply 0 planned changes to the root module at "+theModuleDir, action.Text)
	assert.False(t, action.Changed)
	terraform.AssertNotCalled(t, "Apply", mock.Anything, mock.Anything)
}
