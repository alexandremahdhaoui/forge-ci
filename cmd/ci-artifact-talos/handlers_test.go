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

const patchDocument = `machine:
  install:
    disk: /dev/the-disk-the-patch-names
`

func writeThrowawayCluster(t *testing.T) (string, []byte) {
	t.Helper()

	minted, err := secrets.NewBundle(secrets.NewFixedClock(time.Now()), config.TalosVersionCurrent)
	require.NoError(t, err)

	bundle, err := yaml.Marshal(minted)
	require.NoError(t, err)

	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "bundle.yaml"), bundle, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "patch.yaml"), []byte(patchDocument), 0o600))

	return root, bundle
}

func declaredSpec() Spec {
	return Spec{
		"nodes":             []any{map[string]any{"name": "t0-controlplane", "address": "192.168.1.10"}},
		"clusterName":       "t0",
		"endpoint":          "https://192.168.1.10:6443",
		"kubernetesVersion": constants.DefaultKubernetesVersion,
		"bundleFile":        "bundle.yaml",
		"patchFile":         "patch.yaml",
	}
}

func TestTheDeclareToolAnswersARenderedControlPlaneConfigCarryingThePatch(t *testing.T) {
	t.Parallel()

	root, _ := writeThrowawayCluster(t)

	out, err := NewHandlers().Declare(context.Background(), DeclareInput{Root: root, Spec: declaredSpec()})

	require.NoError(t, err)
	require.Len(t, out.Resources, 1)
	assert.Equal(t, "machine-config", out.Resources[0].Kind)
	assert.Equal(t, "t0-controlplane", out.Resources[0].Name)
	assert.Equal(t, "192.168.1.10", out.Resources[0].Spec["node"])

	rendered, ok := out.Resources[0].Spec["config"].(string)
	require.True(t, ok)
	assert.NotEqual(t, patchDocument, rendered)
	assert.Contains(t, rendered, "/dev/the-disk-the-patch-names")

	loaded, err := configloader.NewFromBytes([]byte(rendered))
	require.NoError(t, err)
	assert.Equal(t, machine.TypeControlPlane, loaded.Machine().Type())
}

func TestTheDeclareToolRendersTheSameBytesTwiceFromTheSameFiles(t *testing.T) {
	t.Parallel()

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

	require.ErrorIs(t, err, machineconfigcontroller.ErrNodes)
	assert.Nil(t, out)
}

func TestARefusalFromARealRenderNeverCarriesTheBundleBytes(t *testing.T) {
	t.Parallel()

	root, bundle := writeThrowawayCluster(t)
	spec := declaredSpec()
	spec["endpoint"] = "6443"

	_, err := NewHandlers().Declare(context.Background(), DeclareInput{Root: root, Spec: spec})

	require.Error(t, err)

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
