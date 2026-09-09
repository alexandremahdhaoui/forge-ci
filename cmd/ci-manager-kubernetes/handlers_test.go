package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

func TestEveryFieldOfAReconcileInputCrossesIntoTheController(t *testing.T) {
	t.Parallel()

	in := ReconcileInput{
		Manager: "kubernetes",
		Resources: []Resource{{
			Kind:          "secret",
			Name:          "flux-deploy-key",
			BootstrapOnly: true,
			Spec: map[string]interface{}{
				"namespace": "flux-system",
				"name":      "flux-deploy-key",
				"data":      map[string]interface{}{"identity": "FLUX_DEPLOY_KEY"},
			},
		}},
		Owned:        []Ownership{{Resource: "secret/flux-deploy-key", Manager: "kubernetes"}},
		Bootstrap:    true,
		Spec:         map[string]interface{}{"statePath": "state.json"},
		DryRun:       true,
		Force:        true,
		CommitPrefix: "ci:",
	}

	out := toReconcileInput(in)
	assert.Equal(t, "kubernetes", out.Manager)
	assert.Equal(t, []citypes.Resource{{
		Kind:          "secret",
		Name:          "flux-deploy-key",
		BootstrapOnly: true,
		Spec: map[string]interface{}{
			"namespace": "flux-system",
			"name":      "flux-deploy-key",
			"data":      map[string]interface{}{"identity": "FLUX_DEPLOY_KEY"},
		},
	}}, out.Resources)
	assert.Equal(t,
		[]citypes.Ownership{{Resource: "secret/flux-deploy-key", Manager: "kubernetes"}}, out.Owned)
	assert.True(t, out.Bootstrap)
	assert.Equal(t, map[string]interface{}{"statePath": "state.json"}, out.Spec)
	assert.True(t, out.DryRun)
	assert.True(t, out.Force)
	assert.Equal(t, "ci:", out.CommitPrefix)
}

func TestEveryFieldOfAReconcileOutputCrossesBackToTheWire(t *testing.T) {
	t.Parallel()

	out := fromReconcileOutput(citypes.ReconcileOutput{
		Owned:     []citypes.Ownership{{Resource: "secret/flux-deploy-key", Manager: "kubernetes"}},
		Actions:   []string{"kept secret flux-system/flux-deploy-key"},
		Changed:   true,
		Published: true,
	})

	assert.Equal(t,
		[]Ownership{{Resource: "secret/flux-deploy-key", Manager: "kubernetes"}}, out.Owned)
	assert.Equal(t, []string{"kept secret flux-system/flux-deploy-key"}, out.Actions)
	assert.True(t, out.Changed)
	assert.True(t, out.Published)
}

func TestAReconcileOutputThatNamesNoActionCrossesAsAnEmptyList(t *testing.T) {
	t.Parallel()

	out := fromReconcileOutput(citypes.ReconcileOutput{})
	assert.Equal(t, []string{}, out.Actions)
	assert.Equal(t, []Ownership{}, out.Owned)
}
