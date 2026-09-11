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
)

func nodeSpec(name, address string) map[string]any {
	return map[string]any{"name": name, "address": address}
}

func theCluster() talossecretsadapter.Cluster {
	return talossecretsadapter.Cluster{
		Name: "t0", Endpoint: "https://10.0.0.1:6443", KubernetesVersion: "1.37.0",
	}
}

func fullSpec(nodes ...any) map[string]any {
	return map[string]any{
		"nodes":             nodes,
		"clusterName":       "t0",
		"endpoint":          "https://10.0.0.1:6443",
		"kubernetesVersion": "1.37.0",
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

func TestANodeDeclaresAResourceWhoseConfigIsTheRenderedBytesAndNotTheFileBytes(t *testing.T) {
	t.Parallel()

	out, err := machineconfigcontroller.New(filesOnDisk(t), aRenderer(t)).Declare(citypes.DeclareInput{
		Root: "/w",
		Spec: fullSpec(nodeSpec("t0-controlplane", "192.168.1.10")),
	})

	require.NoError(t, err)
	require.Len(t, out.Resources, 1)
	assert.Equal(t, citypes.Resource{
		Kind: "machine-config",
		Name: "t0-controlplane",
		Spec: map[string]any{"node": "192.168.1.10", "config": renderedConfig},
	}, out.Resources[0])
	assert.NotEqual(t, storedBundle, out.Resources[0].Spec["config"])
	assert.NotEqual(t, patchText, out.Resources[0].Spec["config"])
}

func TestTheRendererReceivesTheLoadedBundleTheDeclaredClusterAndThePatchFileText(t *testing.T) {
	t.Parallel()

	renderer := machineconfigcontrollermock.NewMockRenderer(t)
	renderer.EXPECT().Load(citypes.Secret(storedBundle)).Return(citypes.Secret(loadedBundle), nil).Once()
	renderer.EXPECT().
		MachineConfig(citypes.Secret(loadedBundle), theCluster(), patchText).
		Return(citypes.Secret(renderedConfig), nil).Once()

	_, err := machineconfigcontroller.New(filesOnDisk(t), renderer).Declare(citypes.DeclareInput{
		Root: "/w",
		Spec: fullSpec(nodeSpec("t0-controlplane", "192.168.1.10")),
	})

	require.NoError(t, err)
}

func TestEveryDeclaredNodeCarriesTheSameRenderedConfigInTheOrderDeclared(t *testing.T) {
	t.Parallel()

	out, err := machineconfigcontroller.New(filesOnDisk(t), aRenderer(t)).Declare(citypes.DeclareInput{
		Root: "/w",
		Spec: fullSpec(nodeSpec("one", "10.0.0.1"), nodeSpec("two", "10.0.0.2")),
	})

	require.NoError(t, err)
	require.Len(t, out.Resources, 2)
	assert.Equal(t, "one", out.Resources[0].Name)
	assert.Equal(t, renderedConfig, out.Resources[0].Spec["config"])
	assert.Equal(t, "two", out.Resources[1].Name)
	assert.Equal(t, "10.0.0.2", out.Resources[1].Spec["node"])
	assert.Equal(t, renderedConfig, out.Resources[1].Spec["config"])
}

func TestAMissingBundleFileIsRefusedNamingTheClusterAndTheFile(t *testing.T) {
	t.Parallel()

	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().
		ReadFile(filepath.Join("/w", "secrets", "bundle.yaml")).
		Return(nil, errors.New("no such file"))

	_, err := machineconfigcontroller.New(fs, machineconfigcontrollermock.NewMockRenderer(t)).
		Declare(citypes.DeclareInput{Root: "/w", Spec: fullSpec(nodeSpec("one", "10.0.0.1"))})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the secret bundle of cluster t0 from secrets/bundle.yaml")
	assert.Contains(t, err.Error(), "no such file")
}

func TestAMissingPatchFileIsRefusedNamingTheClusterAndTheFile(t *testing.T) {
	t.Parallel()

	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().
		ReadFile(filepath.Join("/w", "secrets", "bundle.yaml")).
		Return([]byte(storedBundle), nil)
	fs.EXPECT().
		ReadFile(filepath.Join("/w", "talos", "controlplane.patch.yaml")).
		Return(nil, errors.New("no such file"))

	_, err := machineconfigcontroller.New(fs, aRenderer(t)).
		Declare(citypes.DeclareInput{Root: "/w", Spec: fullSpec(nodeSpec("one", "10.0.0.1"))})

	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"reading the patch document of cluster t0 from talos/controlplane.patch.yaml")
	assert.Contains(t, err.Error(), "no such file")
}

func TestEachAbsentClusterKeyIsRefusedByNameBeforeAnyFileIsRead(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"clusterName", "endpoint", "kubernetesVersion", "bundleFile", "patchFile"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			spec := fullSpec(nodeSpec("one", "10.0.0.1"))
			delete(spec, key)

			_, err := machineconfigcontroller.New(
				fsadaptermock.NewMockFS(t), machineconfigcontrollermock.NewMockRenderer(t),
			).Declare(citypes.DeclareInput{Root: "/w", Spec: spec})

			require.Error(t, err)
			assert.Equal(t, "this engine needs spec."+key+" and the spec does not name it", err.Error())
		})
	}
}

func TestAClusterKeyThatIsNotAStringIsRefusedByName(t *testing.T) {
	t.Parallel()

	spec := fullSpec(nodeSpec("one", "10.0.0.1"))
	spec["endpoint"] = 6443

	_, err := machineconfigcontroller.New(
		fsadaptermock.NewMockFS(t), machineconfigcontrollermock.NewMockRenderer(t),
	).Declare(citypes.DeclareInput{Root: "/w", Spec: spec})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.endpoint")
}

func TestABundleTheRendererRefusesIsAnErrorNamingTheCluster(t *testing.T) {
	t.Parallel()

	renderer := machineconfigcontrollermock.NewMockRenderer(t)
	renderer.EXPECT().Load(citypes.Secret(storedBundle)).
		Return("", errors.New("reading the stored talos secret bundle: it is not a secret bundle document"))

	_, err := machineconfigcontroller.New(filesOnDisk(t), renderer).
		Declare(citypes.DeclareInput{Root: "/w", Spec: fullSpec(nodeSpec("one", "10.0.0.1"))})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading the secret bundle of cluster t0")
}

func TestNoRefusalEverCarriesBundlePatchOrConfigBytes(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

			fs, renderer := failure(t)

			_, err := machineconfigcontroller.New(fs, renderer).
				Declare(citypes.DeclareInput{Root: "/w", Spec: fullSpec(nodeSpec("one", "10.0.0.1"))})

			require.Error(t, err)

			for _, secret := range []string{storedBundle, loadedBundle, patchText, renderedConfig} {
				assert.False(t, strings.Contains(err.Error(), strings.TrimSpace(secret)),
					"the refusal carries bytes it must never print")
			}
		})
	}
}

func TestASpecWithNoNodesIsRefusedByName(t *testing.T) {
	t.Parallel()

	for name, spec := range map[string]map[string]any{
		"no nodes key":   {},
		"an empty list":  {"nodes": []any{}},
		"not a list":     {"nodes": "controlplane"},
		"a nil declared": {"nodes": nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := machineconfigcontroller.New(
				fsadaptermock.NewMockFS(t), machineconfigcontrollermock.NewMockRenderer(t),
			).Declare(citypes.DeclareInput{Spec: spec})

			require.ErrorIs(t, err, machineconfigcontroller.ErrNodes)
		})
	}
}

func TestANodeMissingItsNameOrItsAddressIsRefusedByName(t *testing.T) {
	t.Parallel()

	for want, entry := range map[string]any{
		"name is required":    nodeSpec("", "10.0.0.1"),
		"address is required": nodeSpec("one", ""),
	} {
		t.Run(want, func(t *testing.T) {
			t.Parallel()

			_, err := machineconfigcontroller.New(
				fsadaptermock.NewMockFS(t), machineconfigcontrollermock.NewMockRenderer(t),
			).Declare(citypes.DeclareInput{Spec: fullSpec(entry)})

			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
		})
	}
}

func TestANodeThatIsNotAnObjectIsRefusedByItsPosition(t *testing.T) {
	t.Parallel()

	_, err := machineconfigcontroller.New(
		fsadaptermock.NewMockFS(t), machineconfigcontrollermock.NewMockRenderer(t),
	).Declare(citypes.DeclareInput{Spec: fullSpec("controlplane")})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.nodes[0]")
}

func TestANodeFieldThatIsNotAStringIsRefusedByName(t *testing.T) {
	t.Parallel()

	for field, entry := range map[string]any{
		"spec.nodes[0]": map[string]any{"name": 1, "address": "10.0.0.1"},
		"address":       map[string]any{"name": "one", "address": 10},
	} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()

			_, err := machineconfigcontroller.New(
				fsadaptermock.NewMockFS(t), machineconfigcontrollermock.NewMockRenderer(t),
			).Declare(citypes.DeclareInput{Spec: fullSpec(entry)})

			require.Error(t, err)
			assert.Contains(t, err.Error(), field)
		})
	}
}

func TestPublishRefusesByNameBecauseThisEngineOnlyDeclares(t *testing.T) {
	t.Parallel()

	err := machineconfigcontroller.New(
		fsadaptermock.NewMockFS(t), machineconfigcontrollermock.NewMockRenderer(t),
	).Publish()

	require.ErrorIs(t, err, machineconfigcontroller.ErrNeverPublishes)
	assert.Equal(t,
		"this engine declares machine configs and never publishes: no substage may name it",
		err.Error())
}
