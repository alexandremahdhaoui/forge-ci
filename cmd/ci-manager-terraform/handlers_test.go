package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

func TestEveryFieldOfAReconcileInputCrossesIntoTheController(t *testing.T) {
	t.Parallel()

	in := ReconcileInput{
		Manager: "terraform",
		Resources: []Resource{{
			Kind:          "root-module",
			Name:          "home-record",
			BootstrapOnly: true,
			Spec:          map[string]interface{}{"dir": "terraform/home-record"},
		}},
		Owned:        []Ownership{{Resource: "root-module/home-record", Manager: "terraform"}},
		Bootstrap:    true,
		Spec:         map[string]interface{}{"statePath": "state.json"},
		DryRun:       true,
		Force:        true,
		CommitPrefix: "ci:",
	}

	out := toReconcileInput(in)
	assert.Equal(t, "terraform", out.Manager)
	assert.Equal(t, []citypes.Resource{{
		Kind:          "root-module",
		Name:          "home-record",
		BootstrapOnly: true,
		Spec:          map[string]interface{}{"dir": "terraform/home-record"},
	}}, out.Resources)
	assert.Equal(t,
		[]citypes.Ownership{{Resource: "root-module/home-record", Manager: "terraform"}}, out.Owned)
	assert.True(t, out.Bootstrap)
	assert.Equal(t, map[string]interface{}{"statePath": "state.json"}, out.Spec)
	assert.True(t, out.DryRun)
	assert.True(t, out.Force)
	assert.Equal(t, "ci:", out.CommitPrefix)
}

func TestEveryFieldOfAReconcileOutputCrossesBackToTheWire(t *testing.T) {
	t.Parallel()

	out := fromReconcileOutput(citypes.ReconcileOutput{
		Owned:     []citypes.Ownership{{Resource: "root-module/home-record", Manager: "terraform"}},
		Actions:   []string{"kept the root module at terraform/home-record"},
		Changed:   true,
		Published: true,
	})

	assert.Equal(t, []Ownership{{Resource: "root-module/home-record", Manager: "terraform"}}, out.Owned)
	assert.Equal(t, []string{"kept the root module at terraform/home-record"}, out.Actions)
	assert.True(t, out.Changed)
	assert.True(t, out.Published)
}

func TestAReconcileOutputThatNamesNoActionCrossesAsAnEmptyList(t *testing.T) {
	t.Parallel()

	out := fromReconcileOutput(citypes.ReconcileOutput{})
	assert.Equal(t, []string{}, out.Actions)
	assert.Equal(t, []Ownership{}, out.Owned)
}
