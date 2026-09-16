package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/helmadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const refusalOfAClientNeverBuiltByNew = "this helm client was never built by New"

const theKubeconfigDocument = `apiVersion: v1
kind: Config
clusters:
  - name: here
    cluster:
      server: https://127.0.0.1:1
contexts:
  - name: here
    context:
      cluster: here
current-context: here
`

func theKubeconfigPath(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(path, []byte(theKubeconfigDocument), 0o600))

	return path
}

func declaring(spec map[string]any) ReconcileInput {
	return ReconcileInput{Manager: "kubernetes", Spec: spec}
}

func TestAManagerDeclaringNoKubeconfigIsRefusedByNameBeforeAnyClientThatReachesAClusterIsBuilt(t *testing.T) {
	_, err := NewHandlers().Reconcile(context.Background(), declaring(nil))
	require.Error(t, err)
	assert.Equal(t,
		"reading the spec of manager kubernetes: reading spec.kubeconfig: it declares no source, "+
			"and the cluster credential comes from path or talos or kind", err.Error())
}

func TestTheKubeconfigTheManagerSpecDeclaresIsTheOneItReadsAndNoAmbientFileTakesItsPlace(t *testing.T) {
	decoy := theKubeconfigPath(t)
	t.Setenv("KUBECONFIG", decoy)

	declared := filepath.Join(t.TempDir(), "nothing-is-here")

	_, err := NewHandlers().Reconcile(context.Background(),
		declaring(map[string]any{"kubeconfig": map[string]any{"path": declared}}))
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"reading the cluster credential of manager kubernetes from path "+declared)
	assert.NotContains(t, err.Error(), decoy,
		"the manager read the file the declaration names, never the one the environment names")
}

func TestAKubeconfigTheManagerSpecDeclaresReachesTheClusterClientAndItsHostIsTheDeclaredServer(t *testing.T) {
	out, err := NewHandlers().Reconcile(context.Background(),
		declaring(map[string]any{"kubeconfig": map[string]any{"path": theKubeconfigPath(t)}}))
	require.NoError(t, err)
	assert.Equal(t, []string{}, out.Actions)
}

func TestOnlyAReconcileThatWillRealizeAReleaseCarriesAHelmClientThatCanReachHelm(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		declaration string
		in          citypes.ReconcileInput
		refuses     bool
	}{
		{
			declaration: "only secrets",
			in: citypes.ReconcileInput{
				Resources: []citypes.Resource{{Kind: "secret", Name: "flux-deploy-key"}},
			},
			refuses: true,
		},
		{
			declaration: "only a release",
			in: citypes.ReconcileInput{
				Resources: []citypes.Resource{
					{Kind: managercontroller.KindHelmRelease, Name: "flux"},
				},
			},
			refuses: false,
		},
		{
			declaration: "a secret and a release",
			in: citypes.ReconcileInput{
				Resources: []citypes.Resource{
					{Kind: "secret", Name: "flux-deploy-key"},
					{Kind: managercontroller.KindHelmRelease, Name: "flux"},
				},
			},
			refuses: false,
		},
		{
			declaration: "a bootstrapOnly release on a routine run",
			in: citypes.ReconcileInput{
				Resources: []citypes.Resource{
					{Kind: managercontroller.KindHelmRelease, Name: "flux", BootstrapOnly: true},
				},
			},
			refuses: true,
		},
		{
			declaration: "a bootstrapOnly release on a bootstrap",
			in: citypes.ReconcileInput{
				Bootstrap: true,
				Resources: []citypes.Resource{
					{Kind: managercontroller.KindHelmRelease, Name: "flux", BootstrapOnly: true},
				},
			},
			refuses: false,
		},
		{
			declaration: "a bootstrapOnly release beside one an apply realizes",
			in: citypes.ReconcileInput{
				Resources: []citypes.Resource{
					{Kind: managercontroller.KindHelmRelease, Name: "cilium", BootstrapOnly: true},
					{Kind: managercontroller.KindHelmRelease, Name: "flux"},
				},
			},
			refuses: false,
		},
	} {
		t.Run(c.declaration, func(t *testing.T) {
			t.Parallel()

			releases, err := releaseClient(c.in, helmadapter.StorageMemory, theKubeconfigPath(t))
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

func TestAManagerSpecThatDeclaresNoStorageKeepsHelmOnItsRecordSecrets(t *testing.T) {
	t.Parallel()

	storage, err := declaredStorage(nil)
	require.NoError(t, err)
	assert.Equal(t, helmadapter.StorageSecrets, storage)
}

func TestEveryStorageHelmKeepsRecordsInIsAccepted(t *testing.T) {
	t.Parallel()

	for _, declared := range []string{helmadapter.StorageSecrets, helmadapter.StorageMemory} {
		storage, err := declaredStorage(map[string]any{"storage": declared})
		require.NoError(t, err)
		assert.Equal(t, declared, storage)
	}
}

func TestAStorageTheManagerDoesNotKnowIsRefusedByNameAndNamesBothItAccepts(t *testing.T) {
	t.Parallel()

	_, err := declaredStorage(map[string]any{"storage": "papyrus"})
	require.Error(t, err)
	assert.Equal(t,
		`reading spec.storage: it names "papyrus", `+
			"and helm keeps its release records in secrets or memory", err.Error())
}

func TestAStorageKeyThatIsNotAStringIsRefusedByName(t *testing.T) {
	t.Parallel()

	_, err := declaredStorage(map[string]any{"storage": 7})
	require.Error(t, err)
	assert.ErrorContains(t, err, "reading spec.storage: a string is required, the spec holds a int")
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
				"keys":      []interface{}{"identity", "known_hosts"},
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
			"keys":      []interface{}{"identity", "known_hosts"},
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
