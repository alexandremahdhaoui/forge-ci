package talos

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

func testCluster() citypes.TalosCluster {
	return citypes.TalosCluster{
		Name:              "t0",
		Endpoint:          "https://cluster.example.com:6443",
		KubernetesVersion: constants.DefaultKubernetesVersion,
	}
}

func throwawayBundleBytes(t *testing.T) citypes.Secret {
	t.Helper()

	bundle, _ := throwawayBundle(t)

	return bundle
}

func TestAStoredBundleRoundTripsThroughLoadUnchanged(t *testing.T) {
	t.Parallel()

	stored := throwawayBundleBytes(t)

	reread, err := NewSecrets().Load(stored)
	require.NoError(t, err)
	assert.Equal(t, stored, reread)
}

func TestAMintedBundleIsAValidBundleThatLoadsBackToTheSameBytes(t *testing.T) {
	t.Parallel()

	minted, err := NewSecrets().Mint()
	require.NoError(t, err)

	reread, err := NewSecrets().Load(minted)
	require.NoError(t, err)
	assert.Equal(t, minted, reread)
}

func TestTwoMintsNeverShareABundle(t *testing.T) {
	t.Parallel()

	first, err := NewSecrets().Mint()
	require.NoError(t, err)

	second, err := NewSecrets().Mint()
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
}

func TestTheSameBundleAndInputsRenderIdenticalMachineConfigBytesTwice(t *testing.T) {
	t.Parallel()

	bundle := throwawayBundleBytes(t)

	first, err := NewSecrets().MachineConfig(bundle, testCluster(), patchDocument)
	require.NoError(t, err)

	second, err := NewSecrets().MachineConfig(bundle, testCluster(), patchDocument)
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

func TestARenderedMachineConfigLoadsThroughTheTalosConfigLoader(t *testing.T) {
	t.Parallel()

	rendered, err := NewSecrets().MachineConfig(throwawayBundleBytes(t), testCluster(), patchDocument)
	require.NoError(t, err)

	loaded, err := configloader.NewFromBytes([]byte(rendered))
	require.NoError(t, err)
	assert.Equal(t, machine.TypeControlPlane, loaded.Machine().Type())
	assert.NotEmpty(t, loaded.Cluster().Token().ID())
	assert.NotNil(t, loaded.Cluster().Etcd().CA())
}

func TestARenderedMachineConfigCarriesThePatchDocumentContent(t *testing.T) {
	t.Parallel()

	rendered, err := NewSecrets().MachineConfig(throwawayBundleBytes(t), testCluster(), patchDocument)
	require.NoError(t, err)

	assert.Contains(t, string(rendered), "naa.5002538d009d9e70")
	assert.Contains(t, string(rendered), "ghcr.io/siderolabs/kubelet:v1.37.0")
	assert.Contains(t, string(rendered), "80%")
}

func TestARenderedMachineConfigCarriesAVersionV1Alpha1DocumentThePatchAloneDoesNot(t *testing.T) {
	t.Parallel()

	require.NotContains(t, patchDocument, "version: v1alpha1")

	rendered, err := NewSecrets().MachineConfig(throwawayBundleBytes(t), testCluster(), patchDocument)
	require.NoError(t, err)

	assert.Contains(t, string(rendered), "version: v1alpha1")
}

func TestTheInstallerImageIsReadFromTheUnattendedInstallDocumentOfTheRenderedConfig(t *testing.T) {
	t.Parallel()

	const pinned = "factory.example.invalid/metal-installer:v1.14.0@sha256:" +
		"bf6f5b110f6104a1504db697c7ce209aca38c871c015f9104e0b18a7234f1ad7"

	patch := strings.Replace(patchDocument, "kind: UnattendedInstallConfig\n",
		"kind: UnattendedInstallConfig\ninstaller:\n  image: "+pinned+"\n", 1)

	rendered, err := NewSecrets().MachineConfig(throwawayBundleBytes(t), testCluster(), patch)
	require.NoError(t, err)

	image, err := NewSecrets().InstallerImage(rendered)
	require.NoError(t, err)
	assert.Equal(t, pinned, image)
}

func TestARenderedConfigWithNoUnattendedInstallDocumentHasNoInstallerImage(t *testing.T) {
	t.Parallel()

	rendered, err := NewSecrets().MachineConfig(throwawayBundleBytes(t), testCluster(), "apiVersion: v1alpha1\nkind: KubeProxyConfig\nenabled: false\n")
	require.NoError(t, err)

	image, err := NewSecrets().InstallerImage(rendered)
	require.NoError(t, err)
	assert.Empty(t, image)
}

func TestAConfigThatDoesNotLoadIsRefusedWithoutItsBytes(t *testing.T) {
	t.Parallel()

	_, err := NewSecrets().InstallerImage("\tkey: the private key bytes")
	require.Error(t, err)
	assert.Equal(t,
		"reading the installer image: the rendered machine config does not load as a talos config",
		err.Error())
}

func TestAnUnreadableBundleIsRefusedByName(t *testing.T) {
	t.Parallel()

	_, err := NewSecrets().Load("\tthis is not yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the talos secret bundle: it is not a secret bundle document")
}

func TestABundleMissingItsCertificatesIsRefusedByName(t *testing.T) {
	t.Parallel()

	_, err := NewSecrets().Load("Cluster:\n  Id: a\n  Secret: b\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validating the talos secret bundle")
	assert.Contains(t, err.Error(), "certs is required")
}

func TestAMachineConfigWithNoPatchDocumentIsRefusedByName(t *testing.T) {
	t.Parallel()

	_, err := NewSecrets().MachineConfig(throwawayBundleBytes(t), testCluster(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs a patch document and got none")
}

func TestAnEndpointThatNamesNoHostIsRefusedByName(t *testing.T) {
	t.Parallel()

	cluster := testCluster()
	cluster.Endpoint = "6443"

	_, err := NewSecrets().MachineConfig(throwawayBundleBytes(t), cluster, patchDocument)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "it must be a url naming a host")
}

func TestNoRefusalEverCarriesBundleBytes(t *testing.T) {
	t.Parallel()

	bundle := throwawayBundleBytes(t)

	secretLine := longestLine(string(bundle))
	require.NotEmpty(t, secretLine)

	cluster := testCluster()
	cluster.KubernetesVersion = ""

	_, err := NewSecrets().MachineConfig(bundle, cluster, patchDocument)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secretLine)

	_, err = NewSecrets().Load(bundle[:len(bundle)/2])
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secretLine)
}

func TestABundleNeverPrintsItselfThroughTheSecretType(t *testing.T) {
	t.Parallel()

	bundle, err := NewSecrets().Load(throwawayBundleBytes(t))
	require.NoError(t, err)

	assert.Equal(t, citypes.Redacted, bundle.String())
	assert.Equal(t, citypes.Redacted, fmt.Sprintf("%v", bundle))
	assert.Equal(t, citypes.Redacted, fmt.Sprintf("%#v", bundle))
}

func TestARenderedMachineConfigNeverPrintsItselfThroughTheSecretType(t *testing.T) {
	t.Parallel()

	rendered, err := NewSecrets().MachineConfig(throwawayBundleBytes(t), testCluster(), patchDocument)
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
