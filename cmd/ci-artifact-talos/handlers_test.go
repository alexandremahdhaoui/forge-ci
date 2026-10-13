package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/config/machine"
	"github.com/siderolabs/talos/pkg/machinery/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/machineconfigcontroller"
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
	theVariable = "A_TALOSCONFIG_THIS_TEST_OWNS"
)

func writeThrowawayCluster(t *testing.T) (string, []byte) {
	t.Helper()

	minted, err := secrets.NewBundle(secrets.NewFixedClock(time.Now()), config.TalosVersionCurrent)
	require.NoError(t, err)

	bundle, err := yaml.Marshal(minted)
	require.NoError(t, err)

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "bundle.yaml"), bundle, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "patch.yaml"), []byte(patchDocument), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "talosconfig"), []byte(talosconfigDocument), 0o600))

	t.Setenv(theVariable, filepath.Join(root, "talosconfig"))

	return root, bundle
}

func declaredSpec() Spec {
	return Spec{
		"clusterName":       "t0",
		"kubernetesVersion": constants.DefaultKubernetesVersion,
		"talosconfigEnv":    theVariable,
		"bundleFile":        "bundle.yaml",
		"patchFile":         "patch.yaml",
	}
}

func TestTheDeclareToolAnswersARenderedControlPlaneConfigCarryingThePatchThenTheBootstrap(t *testing.T) {
	root, _ := writeThrowawayCluster(t)

	out, err := NewHandlers().Declare(context.Background(), DeclareInput{Root: root, Spec: declaredSpec()})

	require.NoError(t, err)
	require.Len(t, out.Resources, 2)
	assert.Equal(t, "machine-config", out.Resources[0].Kind)
	assert.Equal(t, "t0", out.Resources[0].Name)
	assert.NotContains(t, out.Resources[0].Spec, "node")
	assert.Equal(t, theVariable, out.Resources[0].Spec["talosconfigEnv"])
	assert.Equal(t, "cluster-bootstrap", out.Resources[1].Kind)
	assert.Equal(t, "t0", out.Resources[1].Name)
	assert.Equal(t, Spec{"talosconfigEnv": theVariable}, out.Resources[1].Spec)

	rendered, ok := out.Resources[0].Spec["config"].(string)
	require.True(t, ok)
	assert.NotEqual(t, patchDocument, rendered)
	assert.Contains(t, rendered, "/dev/the-disk-the-patch-names")
	assert.Contains(t, rendered, "https://node-a.example.invalid:6443")

	loaded, err := configloader.NewFromBytes([]byte(rendered))
	require.NoError(t, err)
	assert.Equal(t, machine.TypeControlPlane, loaded.Machine().Type())
}

func TestTheDeclareToolRendersTheSameBytesTwiceFromTheSameFiles(t *testing.T) {
	root, _ := writeThrowawayCluster(t)

	first, err := NewHandlers().Declare(context.Background(), DeclareInput{Root: root, Spec: declaredSpec()})
	require.NoError(t, err)

	second, err := NewHandlers().Declare(context.Background(), DeclareInput{Root: root, Spec: declaredSpec()})
	require.NoError(t, err)

	assert.Equal(t, first.Resources[0].Spec["config"], second.Resources[0].Spec["config"])
}

func TestTheDeclareToolAnswersTheControllersRefusalAndNoResources(t *testing.T) {
	t.Parallel()

	out, err := NewHandlers().Declare(context.Background(), DeclareInput{Spec: Spec{}})

	require.Error(t, err)
	assert.Equal(t, "this engine needs spec.clusterName and the spec does not name it", err.Error())
	assert.Nil(t, out)
}

func TestTheDeclareToolRefusesARealTalosconfigWhoseSecondEndpointCarriesASchemeAndDeclaresNothing(t *testing.T) {
	root, _ := writeThrowawayCluster(t)
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

func TestARefusalFromARealTalosconfigNamingTwoNodesNeverCarriesTheBundleBytes(t *testing.T) {
	root, bundle := writeThrowawayCluster(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "talosconfig"),
		[]byte(talosconfigDocument+"      - node-b.example.invalid\n"), 0o600))

	_, err := NewHandlers().Declare(context.Background(), DeclareInput{Root: root, Spec: declaredSpec()})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "names 2 nodes")
	assert.Contains(t, err.Error(), "one node per cluster")

	for _, line := range strings.Split(string(bundle), "\n") {
		if len(strings.TrimSpace(line)) < 40 {
			continue
		}

		assert.False(t, strings.Contains(err.Error(), strings.TrimSpace(line)),
			"the refusal carries a line of the bundle")
	}
}

func TestThePublishToolRefusesByNameAndPublishesNothing(t *testing.T) {
	t.Parallel()

	out, err := NewHandlers().
		Publish(context.Background(), ArtifactInput{Revision: "3dd48e96ed7e", Version: "v0.1.0"})

	require.ErrorIs(t, err, machineconfigcontroller.ErrNeverPublishes)
	assert.Nil(t, out)
}
