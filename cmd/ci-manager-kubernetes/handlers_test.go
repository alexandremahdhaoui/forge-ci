package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const refusalOfAClientNeverBuiltByNew = "this helm client was never built by New"

func TestOnlyAReconcileDeclaringAReleaseCarriesAHelmClientThatCanReachHelm(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		declaration string
		resources   []Resource
		refuses     bool
	}{
		{
			declaration: "only secrets",
			resources:   []Resource{{Kind: "secret", Name: "flux-deploy-key"}},
			refuses:     true,
		},
		{
			declaration: "only a release",
			resources: []Resource{
				{Kind: managercontroller.KindHelmRelease, Name: "flux"},
			},
			refuses: false,
		},
		{
			declaration: "a secret and a release",
			resources: []Resource{
				{Kind: "secret", Name: "flux-deploy-key"},
				{Kind: managercontroller.KindHelmRelease, Name: "flux"},
			},
			refuses: false,
		},
	} {
		t.Run(c.declaration, func(t *testing.T) {
			t.Parallel()

			releases, err := releaseClient(c.resources)
			require.NoError(t, err)

			var port managercontroller.Helm = releases

			err = port.InstallRelease(context.Background(), citypes.HelmRelease{
				Namespace: "flux-system",
				Name:      "flux",
				Chart:     "flux2",
				Version:   "2.0.0",
			})
			require.Error(t, err)

			if c.refuses {
				assert.ErrorContains(t, err, refusalOfAClientNeverBuiltByNew,
					"a declaration holding no release carries a client New never built")

				return
			}

			assert.NotContains(t, err.Error(), refusalOfAClientNeverBuiltByNew,
				"a declaration holding a release carries a client New built")
			assert.ErrorContains(t, err,
				"locating chart flux2 2.0.0 for release flux-system/flux",
				"the client opened helm storage and went on to look the chart up")
		})
	}
}

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
