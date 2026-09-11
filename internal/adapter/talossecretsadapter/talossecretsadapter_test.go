package talossecretsadapter

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/siderolabs/talos/pkg/machinery/config/configloader"
	"github.com/siderolabs/talos/pkg/machinery/config/machine"
	"github.com/siderolabs/talos/pkg/machinery/constants"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const patchDocument = `apiVersion: v1alpha1
kind: UnattendedInstallConfig
provisioning:
  diskSelector:
    match: disk.wwid == "naa.5002538d009d9e70"
---
apiVersion: v1alpha1
kind: VolumeConfig
name: EPHEMERAL
provisioning:
  maxSize: 80%
---
apiVersion: v1alpha1
kind: KubeletConfig
image: ghcr.io/siderolabs/kubelet:v1.37.0
config:
  imageGCHighThresholdPercent: 70
---
apiVersion: v1alpha1
kind: KubeProxyConfig
enabled: false
`

func testCluster() Cluster {
	return Cluster{
		Name:              "t0",
		Endpoint:          "https://cluster.example.com:6443",
		KubernetesVersion: constants.DefaultKubernetesVersion,
	}
}

func TestAMintedBundleRoundTripsThroughYamlUnchanged(t *testing.T) {
	t.Parallel()

	minted, err := New().Mint()
	require.NoError(t, err)
	require.NotEmpty(t, minted)

	reread, err := New().Load(minted)
	require.NoError(t, err)
	assert.Equal(t, minted, reread)
}

func TestTwoMintsAnswerDifferentBundles(t *testing.T) {
	t.Parallel()

	first, err := New().Mint()
	require.NoError(t, err)

	second, err := New().Mint()
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
}

func TestTheSameBundleAndInputsRenderIdenticalMachineConfigBytesTwice(t *testing.T) {
	t.Parallel()

	bundle, err := New().Mint()
	require.NoError(t, err)

	first, err := New().MachineConfig(bundle, testCluster(), patchDocument)
	require.NoError(t, err)

	second, err := New().MachineConfig(bundle, testCluster(), patchDocument)
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

func TestARenderedMachineConfigLoadsThroughTheTalosConfigLoader(t *testing.T) {
	t.Parallel()

	bundle, err := New().Mint()
	require.NoError(t, err)

	rendered, err := New().MachineConfig(bundle, testCluster(), patchDocument)
	require.NoError(t, err)

	loaded, err := configloader.NewFromBytes([]byte(rendered))
	require.NoError(t, err)
	assert.Equal(t, machine.TypeControlPlane, loaded.Machine().Type())
	assert.NotEmpty(t, loaded.Cluster().Token().ID())
	assert.NotNil(t, loaded.Cluster().Etcd().CA())
}

func TestARenderedMachineConfigCarriesThePatchDocumentContent(t *testing.T) {
	t.Parallel()

	bundle, err := New().Mint()
	require.NoError(t, err)

	rendered, err := New().MachineConfig(bundle, testCluster(), patchDocument)
	require.NoError(t, err)

	assert.Contains(t, string(rendered), "naa.5002538d009d9e70")
	assert.Contains(t, string(rendered), "ghcr.io/siderolabs/kubelet:v1.37.0")
	assert.Contains(t, string(rendered), "80%")
}

func TestARenderedMachineConfigCarriesAVersionV1Alpha1DocumentThePatchAloneDoesNot(t *testing.T) {
	t.Parallel()

	require.NotContains(t, patchDocument, "version: v1alpha1")

	bundle, err := New().Mint()
	require.NoError(t, err)

	rendered, err := New().MachineConfig(bundle, testCluster(), patchDocument)
	require.NoError(t, err)

	assert.Contains(t, string(rendered), "version: v1alpha1")
}

func TestARenderedClientConfigurationNamesTheClusterAndItsEndpointHost(t *testing.T) {
	t.Parallel()

	bundle, err := New().Mint()
	require.NoError(t, err)

	rendered, err := New().Talosconfig(bundle, testCluster())
	require.NoError(t, err)

	assert.Contains(t, string(rendered), "t0")
	assert.Contains(t, string(rendered), "cluster.example.com")
	assert.NotContains(t, string(rendered), "6443")
}

func TestAnUnreadableBundleIsRefusedByName(t *testing.T) {
	t.Parallel()

	_, err := New().Load("\tthis is not yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the stored talos secret bundle")
}

func TestABundleMissingItsCertificatesIsRefusedByName(t *testing.T) {
	t.Parallel()

	_, err := New().Load("Cluster:\n  Id: a\n  Secret: b\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validating the stored talos secret bundle")
	assert.Contains(t, err.Error(), "certs is required")
}

func TestAMachineConfigWithNoPatchDocumentIsRefusedByName(t *testing.T) {
	t.Parallel()

	bundle, err := New().Mint()
	require.NoError(t, err)

	_, err = New().MachineConfig(bundle, testCluster(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs a patch document and got none")
}

func TestAnEndpointThatNamesNoHostIsRefusedByName(t *testing.T) {
	t.Parallel()

	bundle, err := New().Mint()
	require.NoError(t, err)

	cluster := testCluster()
	cluster.Endpoint = "6443"

	_, err = New().MachineConfig(bundle, cluster, patchDocument)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "it must be a url naming a host")
}

func TestNoRefusalEverCarriesBundleBytes(t *testing.T) {
	t.Parallel()

	bundle, err := New().Mint()
	require.NoError(t, err)

	secretLine := longestLine(string(bundle))
	require.NotEmpty(t, secretLine)

	cluster := testCluster()
	cluster.KubernetesVersion = ""

	_, err = New().MachineConfig(bundle, cluster, patchDocument)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secretLine)

	_, err = New().Talosconfig(bundle, cluster)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secretLine)

	_, err = New().Load(bundle[:len(bundle)/2])
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secretLine)
}

func TestABundleNeverPrintsItselfThroughTheSecretType(t *testing.T) {
	t.Parallel()

	bundle, err := New().Mint()
	require.NoError(t, err)

	assert.Equal(t, citypes.Redacted, bundle.String())
	assert.Equal(t, citypes.Redacted, fmt.Sprintf("%v", bundle))
	assert.Equal(t, citypes.Redacted, fmt.Sprintf("%#v", bundle))
}

func TestARenderedMachineConfigNeverPrintsItselfThroughTheSecretType(t *testing.T) {
	t.Parallel()

	bundle, err := New().Mint()
	require.NoError(t, err)

	rendered, err := New().MachineConfig(bundle, testCluster(), patchDocument)
	require.NoError(t, err)

	assert.Equal(t, citypes.Redacted, rendered.String())
}

func longestLine(in string) string {
	longest := ""

	for _, line := range strings.Split(in, "\n") {
		if len(line) > len(longest) {
			longest = strings.TrimSpace(line)
		}
	}

	return longest
}
