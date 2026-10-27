package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configdiff"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/config/machine"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	stockyaml "go.yaml.in/yaml/v4"
	"sigs.k8s.io/yaml"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/talosadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/talossecretsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/machineconfigcontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/machineconfigcontrollermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	patchDocument = `machine:
  install:
    disk: /dev/the-disk-the-patch-names
`
	talosconfigDocument = `context: t0
contexts:
  t0:
    endpoints:
      - node-a.example.invalid
    nodes:
      - node-a.example.invalid
`
	theVariable         = "A_TALOSCONFIG_THIS_TEST_OWNS"
	theRecoveryVariable = "A_RECOVERY_DIRECTORY_THIS_TEST_OWNS"
)

func writeThrowawayCluster(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "patch.yaml"), []byte(patchDocument), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "talosconfig"), []byte(talosconfigDocument), 0o600))

	t.Setenv(theVariable, filepath.Join(root, "talosconfig"))

	return root
}

func declaredSpec() Spec {
	return Spec{
		"clusterName":       "t0",
		"kubernetesVersion": constants.DefaultKubernetesVersion,
		"talosconfigEnv":    theVariable,
		"recoveryDirEnv":    theRecoveryVariable,
		"patchFile":         "patch.yaml",
	}
}

type theNode struct {
	*machineconfigcontrollermock.MockTalosconfig
	registry *machineconfigcontrollermock.MockRegistry
}

func aNode(t *testing.T) theNode {
	t.Helper()

	node := theNode{machineconfigcontrollermock.NewMockTalosconfig(t), machineconfigcontrollermock.NewMockRegistry(t)}
	node.EXPECT().Context(mock.Anything).RunAndReturn(talosadapter.NewTalosconfig().Context).Maybe()

	return node
}

func (n theNode) inMaintenance() theNode {
	n.EXPECT().InMaintenance(mock.Anything, mock.Anything).Return(true, nil)

	return n
}

func (n theNode) holding(bundle citypes.Secret) theNode {
	n.EXPECT().InMaintenance(mock.Anything, mock.Anything).Return(false, nil)
	n.EXPECT().Bootstrapped(mock.Anything, mock.Anything).Return(true, nil)
	n.EXPECT().StoredBundle(mock.Anything, mock.Anything).Return(bundle, true, nil)

	return n
}

func handlersReaching(node theNode) Handlers {
	return newHandlers(machineconfigcontroller.New(fsadapter.New(), talossecretsadapter.New(), node, node.registry))
}

func TestTheDeclareToolOnANodeInMaintenanceAnswersARenderedControlPlaneConfigThenTheBootstrapThenTheBundle(t *testing.T) {
	root := writeThrowawayCluster(t)

	out, err := handlersReaching(aNode(t).inMaintenance()).
		Declare(context.Background(), DeclareInput{Root: root, Spec: declaredSpec()})

	require.NoError(t, err)
	require.Len(t, out.Resources, 3)
	assert.Equal(t, "machine-config", out.Resources[0].Kind)
	assert.Equal(t, "t0", out.Resources[0].Name)
	assert.NotContains(t, out.Resources[0].Spec, "node")
	assert.Equal(t, theVariable, out.Resources[0].Spec["talosconfigEnv"])
	assert.Equal(t, theRecoveryVariable, out.Resources[0].Spec["recoveryDirEnv"])
	assert.Equal(t, "cluster-bootstrap", out.Resources[1].Kind)
	assert.Equal(t, "t0", out.Resources[1].Name)
	assert.Equal(t, Spec{"talosconfigEnv": theVariable}, out.Resources[1].Spec)
	assert.Equal(t, "secret-bundle", out.Resources[2].Kind)
	assert.Equal(t, "t0", out.Resources[2].Name)
	assert.Equal(t, out.Resources[0].Spec["bundle"], out.Resources[2].Spec["bundle"])

	rendered, ok := out.Resources[0].Spec["config"].(citypes.Secret)
	require.True(t, ok)
	assert.NotEqual(t, patchDocument, string(rendered))
	assert.Contains(t, string(rendered), "/dev/the-disk-the-patch-names")
	assert.Contains(t, string(rendered), "https://node-a.example.invalid:6443")

	loaded, err := configloader.NewFromBytes([]byte(rendered))
	require.NoError(t, err)
	assert.Equal(t, machine.TypeControlPlane, loaded.Machine().Type())
}

func TestALaterRunRendersFromTheBundleTheFirstRunStoredAConfigTheVendorFindsNoPatchAgainst(t *testing.T) {
	root := writeThrowawayCluster(t)

	first, err := handlersReaching(aNode(t).inMaintenance()).
		Declare(context.Background(), DeclareInput{Root: root, Spec: declaredSpec()})
	require.NoError(t, err)

	stored, ok := first.Resources[2].Spec["bundle"].(citypes.Secret)
	require.True(t, ok)

	later, err := handlersReaching(aNode(t).holding(stored)).
		Declare(context.Background(), DeclareInput{Root: root, Spec: declaredSpec()})
	require.NoError(t, err)

	again, err := handlersReaching(aNode(t).holding(stored)).
		Declare(context.Background(), DeclareInput{Root: root, Spec: declaredSpec()})
	require.NoError(t, err)

	applied := loadedConfig(t, first.Resources[0].Spec["config"])
	rendered := loadedConfig(t, later.Resources[0].Spec["config"])

	patches, err := configdiff.Patch(applied, rendered)
	require.NoError(t, err)
	assert.Empty(t, patches, "a later run renders the config the first run applied")
	assert.Equal(t, later.Resources[0].Spec["config"], again.Resources[0].Spec["config"])
	assert.Equal(t, stored, later.Resources[2].Spec["bundle"])
}

func TestTheBundleAFirstRunDeclaresLoadsThroughStockTalosctlWithEveryFieldItWrote(t *testing.T) {
	root := writeThrowawayCluster(t)

	first, err := handlersReaching(aNode(t).inMaintenance()).
		Declare(context.Background(), DeclareInput{Root: root, Spec: declaredSpec()})
	require.NoError(t, err)

	declared, ok := first.Resources[2].Spec["bundle"].(citypes.Secret)
	require.True(t, ok)

	path := filepath.Join(t.TempDir(), "secrets.yaml")
	require.NoError(t, os.WriteFile(path, []byte(declared), 0o600))

	loaded, err := secrets.LoadBundle(path)
	require.NoError(t, err)
	require.NoError(t, loaded.Validate(config.TalosVersionCurrent), "stock talosctl reads a whole bundle")

	reencoded, err := stockyaml.Marshal(loaded)
	require.NoError(t, err)
	assert.Equal(t, string(declared), string(reencoded), "stock talosctl reads every field the engine wrote")
}

func TestALaterRunOverABundleStoredInTheUpperCaseFormIsRefusedByNameAndDeclaresNothing(t *testing.T) {
	root := writeThrowawayCluster(t)

	minted, err := secrets.NewBundle(secrets.NewClock(), config.TalosVersionCurrent)
	require.NoError(t, err)

	upperCase, err := yaml.Marshal(minted)
	require.NoError(t, err)
	require.Contains(t, string(upperCase), "Certs:")

	out, err := handlersReaching(aNode(t).holding(citypes.Secret(upperCase))).
		Declare(context.Background(), DeclareInput{Root: root, Spec: declaredSpec()})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading the secret bundle of cluster t0: validating the stored talos secret bundle")
	assert.Contains(t, err.Error(), "certs is required")
	assert.Nil(t, out)
}

func loadedConfig(t *testing.T, held any) config.Provider {
	t.Helper()

	text, ok := held.(citypes.Secret)
	require.True(t, ok)

	loaded, err := configloader.NewFromBytes([]byte(text))
	require.NoError(t, err)

	return loaded
}

func TestTheDeclareToolAnswersTheControllersRefusalAndNoResources(t *testing.T) {
	t.Parallel()

	out, err := NewHandlers().Declare(context.Background(), DeclareInput{Spec: Spec{}})

	require.Error(t, err)
	assert.Equal(t, "this engine needs spec.clusterName and the spec does not name it", err.Error())
	assert.Nil(t, out)
}

func TestTheDeclareToolRefusesTheRetiredBundleFileByNameAndDeclaresNothing(t *testing.T) {
	t.Parallel()

	spec := declaredSpec()
	spec["bundleFile"] = "talos/secrets.yaml"

	out, err := NewHandlers().Declare(context.Background(), DeclareInput{Spec: spec})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "this engine refuses spec.bundleFile")
	assert.Nil(t, out)
}

func TestTheDeclareToolRefusesARealTalosconfigWhoseSecondEndpointCarriesASchemeBeforeReachingTheNode(t *testing.T) {
	root := writeThrowawayCluster(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "talosconfig"), []byte(`context: t0
contexts:
  t0:
    endpoints:
      - talos.example
      - https://talos.example:50000
    nodes:
      - talos.example
`), 0o600))

	out, err := NewHandlers().Declare(context.Background(), DeclareInput{Root: root, Spec: declaredSpec()})

	require.Error(t, err)
	assert.Nil(t, out)
	assert.Equal(t,
		`reading the client configuration of cluster t0: reading the client configuration: endpoint 2 of context "t0" `+
			"carries a scheme, and a talosconfig endpoint is a host or a host and port",
		err.Error())
}

func TestTheDeclareToolRefusesARealTalosconfigNamingTwoNodesBeforeReachingEither(t *testing.T) {
	root := writeThrowawayCluster(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "talosconfig"),
		[]byte(talosconfigDocument+"      - node-b.example.invalid\n"), 0o600))

	_, err := NewHandlers().Declare(context.Background(), DeclareInput{Root: root, Spec: declaredSpec()})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "names 2 nodes")
	assert.Contains(t, err.Error(), "one node per cluster")
}

func TestThePublishToolRefusesByNameAndPublishesNothing(t *testing.T) {
	t.Parallel()

	out, err := NewHandlers().
		Publish(context.Background(), ArtifactInput{Revision: "3dd48e96ed7e", Version: "v0.1.0"})

	require.ErrorIs(t, err, machineconfigcontroller.ErrNeverPublishes)
	assert.Nil(t, out)
}
