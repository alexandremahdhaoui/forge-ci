package machineconfigcontroller_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/talossecretsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/machineconfigcontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/fsadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/machineconfigcontrollermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	storedBundle   = "bundle: the stored cluster CA private key\n"
	loadedBundle   = "bundle: the loaded cluster CA private key\n"
	patchText      = "machine:\n  install:\n    disk: /dev/sda\n"
	renderedConfig = "version: v1alpha1\nmachine:\n  ca:\n    key: the rendered private key\n"

	theVariable = "A_TALOSCONFIG_THIS_TEST_OWNS"
	thePath     = "/home/a-person/talosconfig"
	theNode     = "box.lan"
)

func theContext() citypes.TalosContext {
	return citypes.TalosContext{Endpoints: []string{"box.lan:50000", "spare.lan"}, Node: theNode}
}

func theCluster() talossecretsadapter.Cluster {
	return talossecretsadapter.Cluster{
		Name: "t0", Endpoint: "https://box.lan:6443", KubernetesVersion: "1.37.0",
	}
}

func fullSpec() map[string]any {
	return map[string]any{
		"clusterName":       "t0",
		"kubernetesVersion": "1.37.0",
		"talosconfigEnv":    theVariable,
		"bundleFile":        "secrets/bundle.yaml",
		"patchFile":         "talos/controlplane.patch.yaml",
	}
}

func filesOnDisk(t *testing.T) *fsadaptermock.MockFS {
	t.Helper()

	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().
		ReadFile(filepath.Join("/w", "secrets", "bundle.yaml")).
		Return([]byte(storedBundle), nil).Maybe()
	fs.EXPECT().
		ReadFile(filepath.Join("/w", "talos", "controlplane.patch.yaml")).
		Return([]byte(patchText), nil).Maybe()

	return fs
}

func aRenderer(t *testing.T) *machineconfigcontrollermock.MockRenderer {
	t.Helper()

	renderer := machineconfigcontrollermock.NewMockRenderer(t)
	renderer.EXPECT().Load(citypes.Secret(storedBundle)).Return(citypes.Secret(loadedBundle), nil).Maybe()
	renderer.EXPECT().
		MachineConfig(citypes.Secret(loadedBundle), theCluster(), patchText).
		Return(citypes.Secret(renderedConfig), nil).Maybe()

	return renderer
}

func aTalosconfig(t *testing.T) *machineconfigcontrollermock.MockTalosconfig {
	t.Helper()

	talosconfig := machineconfigcontrollermock.NewMockTalosconfig(t)
	talosconfig.EXPECT().Context(citypes.Secret(thePath)).Return(theContext(), nil).Maybe()

	return talosconfig
}

func exported(t *testing.T) {
	t.Helper()
	t.Setenv(theVariable, thePath)
}

func TestAClusterDeclaresItsMachineConfigThenItsBootstrapBothNamedByTheClusterBothCarryingTheVariableAndNeitherNamesANode(t *testing.T) {
	exported(t)

	out, err := machineconfigcontroller.New(filesOnDisk(t), aRenderer(t), aTalosconfig(t)).
		Declare(citypes.DeclareInput{Root: "/w", Spec: fullSpec()})

	require.NoError(t, err)
	require.Len(t, out.Resources, 2)
	assert.Equal(t, citypes.Resource{
		Kind: "machine-config",
		Name: "t0",
		Spec: map[string]any{"config": renderedConfig, "talosconfigEnv": theVariable},
	}, out.Resources[0])
	assert.Equal(t, citypes.Resource{
		Kind: "cluster-bootstrap",
		Name: "t0",
		Spec: map[string]any{"talosconfigEnv": theVariable},
	}, out.Resources[1])
	assert.NotEqual(t, storedBundle, out.Resources[0].Spec["config"])
	assert.NotEqual(t, patchText, out.Resources[0].Spec["config"])
}

func TestTheRendererReceivesTheLoadedBundleTheClusterEndpointBuiltFromTheFirstContextEndpointAndThePatchText(t *testing.T) {
	exported(t)

	renderer := machineconfigcontrollermock.NewMockRenderer(t)
	renderer.EXPECT().Load(citypes.Secret(storedBundle)).Return(citypes.Secret(loadedBundle), nil).Once()
	renderer.EXPECT().
		MachineConfig(citypes.Secret(loadedBundle), theCluster(), patchText).
		Return(citypes.Secret(renderedConfig), nil).Once()

	_, err := machineconfigcontroller.New(filesOnDisk(t), renderer, aTalosconfig(t)).
		Declare(citypes.DeclareInput{Root: "/w", Spec: fullSpec()})

	require.NoError(t, err)
}

func TestAContextEndpointWithoutAPortStillBecomesTheClusterEndpointOnPort6443(t *testing.T) {
	exported(t)

	talosconfig := machineconfigcontrollermock.NewMockTalosconfig(t)
	talosconfig.EXPECT().Context(citypes.Secret(thePath)).
		Return(citypes.TalosContext{Endpoints: []string{"box.lan"}, Node: theNode}, nil)

	_, err := machineconfigcontroller.New(filesOnDisk(t), aRenderer(t), talosconfig).
		Declare(citypes.DeclareInput{Root: "/w", Spec: fullSpec()})

	require.NoError(t, err)
}

func TestAVariableHoldingNothingIsRefusedNamingTheClusterAndTheVariableBeforeAnyFileIsRead(t *testing.T) {
	t.Setenv(theVariable, "")

	_, err := machineconfigcontroller.New(
		fsadaptermock.NewMockFS(t),
		machineconfigcontrollermock.NewMockRenderer(t),
		machineconfigcontrollermock.NewMockTalosconfig(t),
	).Declare(citypes.DeclareInput{Root: "/w", Spec: fullSpec()})

	require.Error(t, err)
	assert.Equal(t,
		"reading the client configuration of cluster t0: nothing is set in "+theVariable+
			", and spec.talosconfigEnv names it. export it before declaring", err.Error())
}

func TestATalosconfigTheAdapterRefusesIsAnErrorNamingTheClusterAndNeverThePath(t *testing.T) {
	exported(t)

	talosconfig := machineconfigcontrollermock.NewMockTalosconfig(t)
	talosconfig.EXPECT().Context(citypes.Secret(thePath)).
		Return(citypes.TalosContext{}, errors.New("context \"t0\" names 2 nodes"))

	_, err := machineconfigcontroller.New(
		fsadaptermock.NewMockFS(t), machineconfigcontrollermock.NewMockRenderer(t), talosconfig,
	).Declare(citypes.DeclareInput{Root: "/w", Spec: fullSpec()})

	require.Error(t, err)
	assert.Equal(t,
		"reading the client configuration of cluster t0: context \"t0\" names 2 nodes", err.Error())
	assert.NotContains(t, err.Error(), thePath)
}

func TestAMissingBundleFileIsRefusedNamingTheClusterAndTheFile(t *testing.T) {
	exported(t)

	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().
		ReadFile(filepath.Join("/w", "secrets", "bundle.yaml")).
		Return(nil, errors.New("no such file"))

	_, err := machineconfigcontroller.New(fs, machineconfigcontrollermock.NewMockRenderer(t), aTalosconfig(t)).
		Declare(citypes.DeclareInput{Root: "/w", Spec: fullSpec()})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the secret bundle of cluster t0 from secrets/bundle.yaml")
	assert.Contains(t, err.Error(), "no such file")
}

func TestAMissingPatchFileIsRefusedNamingTheClusterAndTheFile(t *testing.T) {
	exported(t)

	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().
		ReadFile(filepath.Join("/w", "secrets", "bundle.yaml")).
		Return([]byte(storedBundle), nil)
	fs.EXPECT().
		ReadFile(filepath.Join("/w", "talos", "controlplane.patch.yaml")).
		Return(nil, errors.New("no such file"))

	_, err := machineconfigcontroller.New(fs, aRenderer(t), aTalosconfig(t)).
		Declare(citypes.DeclareInput{Root: "/w", Spec: fullSpec()})

	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"reading the patch document of cluster t0 from talos/controlplane.patch.yaml")
	assert.Contains(t, err.Error(), "no such file")
}

func TestEachAbsentSpecKeyIsRefusedByNameBeforeAnyFileIsRead(t *testing.T) {
	exported(t)

	for _, key := range []string{"clusterName", "kubernetesVersion", "talosconfigEnv", "bundleFile", "patchFile"} {
		t.Run(key, func(t *testing.T) {
			spec := fullSpec()
			delete(spec, key)

			_, err := machineconfigcontroller.New(
				fsadaptermock.NewMockFS(t),
				machineconfigcontrollermock.NewMockRenderer(t),
				aTalosconfig(t),
			).Declare(citypes.DeclareInput{Root: "/w", Spec: spec})

			require.Error(t, err)
			assert.Equal(t, "this engine needs spec."+key+" and the spec does not name it", err.Error())
		})
	}
}

func TestTheRetiredEndpointKeyIsRefusedByNameBeforeAnythingIsRead(t *testing.T) {
	exported(t)

	spec := fullSpec()
	spec["endpoint"] = "https://box.lan:6443"

	_, err := machineconfigcontroller.New(
		fsadaptermock.NewMockFS(t),
		machineconfigcontrollermock.NewMockRenderer(t),
		machineconfigcontrollermock.NewMockTalosconfig(t),
	).Declare(citypes.DeclareInput{Root: "/w", Spec: spec})

	require.Error(t, err)
	assert.Equal(t,
		"this engine refuses spec.endpoint: the node and the endpoint come from the talosconfig "+
			"the variable spec.talosconfigEnv names", err.Error())
}

func TestTheRetiredNodesKeyIsRefusedByNameBeforeAnythingIsRead(t *testing.T) {
	exported(t)

	spec := fullSpec()
	spec["nodes"] = []any{map[string]any{"name": "one", "address": "box.lan"}}

	_, err := machineconfigcontroller.New(
		fsadaptermock.NewMockFS(t),
		machineconfigcontrollermock.NewMockRenderer(t),
		machineconfigcontrollermock.NewMockTalosconfig(t),
	).Declare(citypes.DeclareInput{Root: "/w", Spec: spec})

	require.Error(t, err)
	assert.Equal(t,
		"this engine refuses spec.nodes: the node and the endpoint come from the talosconfig "+
			"the variable spec.talosconfigEnv names", err.Error())
}

func TestASpecKeyThatIsNotAStringIsRefusedByName(t *testing.T) {
	exported(t)

	spec := fullSpec()
	spec["kubernetesVersion"] = 1.37

	_, err := machineconfigcontroller.New(
		fsadaptermock.NewMockFS(t),
		machineconfigcontrollermock.NewMockRenderer(t),
		machineconfigcontrollermock.NewMockTalosconfig(t),
	).Declare(citypes.DeclareInput{Root: "/w", Spec: spec})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.kubernetesVersion")
}

func TestABundleTheRendererRefusesIsAnErrorNamingTheCluster(t *testing.T) {
	exported(t)

	renderer := machineconfigcontrollermock.NewMockRenderer(t)
	renderer.EXPECT().Load(citypes.Secret(storedBundle)).
		Return("", errors.New("reading the stored talos secret bundle: it is not a secret bundle document"))

	_, err := machineconfigcontroller.New(filesOnDisk(t), renderer, aTalosconfig(t)).
		Declare(citypes.DeclareInput{Root: "/w", Spec: fullSpec()})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading the secret bundle of cluster t0")
}

func TestNoRefusalEverCarriesBundlePatchConfigOrTalosconfigBytes(t *testing.T) {
	exported(t)

	failures := map[string]func(*testing.T) (*fsadaptermock.MockFS, *machineconfigcontrollermock.MockRenderer){
		"the bundle does not load": func(t *testing.T) (
			*fsadaptermock.MockFS, *machineconfigcontrollermock.MockRenderer,
		) {
			renderer := machineconfigcontrollermock.NewMockRenderer(t)
			renderer.EXPECT().Load(citypes.Secret(storedBundle)).Return("", errors.New("not a bundle"))

			return filesOnDisk(t), renderer
		},
		"the config does not render": func(t *testing.T) (
			*fsadaptermock.MockFS, *machineconfigcontrollermock.MockRenderer,
		) {
			renderer := machineconfigcontrollermock.NewMockRenderer(t)
			renderer.EXPECT().Load(citypes.Secret(storedBundle)).Return(citypes.Secret(loadedBundle), nil)
			renderer.EXPECT().
				MachineConfig(citypes.Secret(loadedBundle), theCluster(), patchText).
				Return(citypes.Secret(renderedConfig), errors.New("patching failed"))

			return filesOnDisk(t), renderer
		},
	}

	for name, failure := range failures {
		t.Run(name, func(t *testing.T) {
			fs, renderer := failure(t)

			_, err := machineconfigcontroller.New(fs, renderer, aTalosconfig(t)).
				Declare(citypes.DeclareInput{Root: "/w", Spec: fullSpec()})

			require.Error(t, err)

			for _, secret := range []string{storedBundle, loadedBundle, patchText, renderedConfig, thePath} {
				assert.False(t, strings.Contains(err.Error(), strings.TrimSpace(secret)),
					"the refusal carries bytes it must never print")
			}
		})
	}
}

func TestPublishRefusesByNameBecauseThisEngineOnlyDeclares(t *testing.T) {
	t.Parallel()

	err := machineconfigcontroller.New(
		fsadaptermock.NewMockFS(t),
		machineconfigcontrollermock.NewMockRenderer(t),
		machineconfigcontrollermock.NewMockTalosconfig(t),
	).Publish()

	require.ErrorIs(t, err, machineconfigcontroller.ErrNeverPublishes)
	assert.Equal(t,
		"this engine declares machine configs and never publishes: no substage may name it",
		err.Error())
}
