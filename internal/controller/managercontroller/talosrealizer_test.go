package managercontroller_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/managercontrollermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	controlplane = `version: v1alpha1
machine:
  type: controlplane
  install:
    disk: /dev/sda
cluster:
  clusterName: t0
`
	controlplaneReordered = `cluster:
  clusterName: t0
version: v1alpha1
machine:
  install:
    disk: /dev/sda
  type: controlplane
`
	controlplaneWithABiggerDisk = `version: v1alpha1
machine:
  type: controlplane
  install:
    disk: /dev/nvme0n1
cluster:
  clusterName: t0
`
	controlplaneWithAVolume = controlplane + `---
apiVersion: v1alpha1
kind: VolumeConfig
name: EPHEMERAL
provisioning:
  maxSize: 20GiB
`
	controlplaneWithACommentedSeparatorAndABiggerVolume = controlplane + `--- # the volume
apiVersion: v1alpha1
kind: VolumeConfig
name: EPHEMERAL
provisioning:
  maxSize: 40GiB
`
	controlplaneWithATrailingCommentOnlyDocument = controlplane + `---
# nothing lives in this document
`
	theVolume = `apiVersion: v1alpha1
kind: VolumeConfig
name: EPHEMERAL
provisioning:
  maxSize: 20GiB
`
	theIngressRule = `apiVersion: v1alpha1
kind: NetworkDefaultActionConfig
ingress: block
`
	controlplaneThenTheVolumeThenTheIngressRule = controlplane + "---\n" + theVolume + "---\n" + theIngressRule
	controlplaneThenTheIngressRuleThenTheVolume = controlplane + "---\n" + theIngressRule + "---\n" + theVolume
	theVolumeThenTheControlplane                = theVolume + "---\n" + controlplane
	controlplaneWithTheVolumeTwice              = controlplane + "---\n" + theVolume + "---\n" + theVolume
	controlplaneTwice                           = controlplane + "---\n" + controlplane
)

const talosconfigVariable = "FORGE_CI_TEST_CLIENT_CONFIG"

func talosRealizer(t *testing.T) (managercontroller.TalosRealizer, *managercontrollermock.MockTalos) {
	t.Helper()
	t.Setenv(talosconfigVariable, "/home/operator/.config/t0.yaml")

	talos := managercontrollermock.NewMockTalos(t)

	return managercontroller.NewTalosRealizer(t.Context(), talos, talosconfigVariable), talos
}

func machineConfig(document string) citypes.Resource {
	return citypes.Resource{
		Kind: managercontroller.KindMachineConfig,
		Name: "t0-controlplane",
		Spec: map[string]any{"node": "192.168.1.10", "config": document},
	}
}

func TestTheTalosRealizerNamesItselfTalos(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "talos", managercontroller.NewTalosRealizer(t.Context(), nil, "").Kind())
}

func TestTheTalosRealizerKeepsAMachineConfigTheNodeAlreadyHolds(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().
		MachineConfig(mock.Anything, "192.168.1.10", citypes.Secret("/home/operator/.config/t0.yaml")).
		Return(controlplane, nil)

	action, err := r.Realize(machineConfig(controlplane), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept machine config on node 192.168.1.10", action.Text)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerKeepsAMachineConfigThatDiffersOnlyByKeyOrder(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(controlplaneReordered, nil)

	action, err := r.Realize(machineConfig(controlplane), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept machine config on node 192.168.1.10", action.Text)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerKeepsAMultiDocumentMachineConfigTheNodeAlreadyHolds(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(controlplaneWithAVolume, nil)

	action, err := r.Realize(machineConfig(controlplaneWithAVolume), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept machine config on node 192.168.1.10", action.Text)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerKeepsAMachineConfigWhoseDocumentsAreInADifferentOrder(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(controlplaneThenTheIngressRuleThenTheVolume, nil)

	action, err := r.Realize(machineConfig(controlplaneThenTheVolumeThenTheIngressRule), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept machine config on node 192.168.1.10", action.Text)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerKeepsAMachineConfigWhoseVersionDocumentIsWrittenLast(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(theVolumeThenTheControlplane, nil)

	action, err := r.Realize(machineConfig(controlplaneWithAVolume), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept machine config on node 192.168.1.10", action.Text)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerKeepsAMachineConfigWrappedInEmptyDocuments(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return("---\n"+controlplane+"---\n", nil)

	action, err := r.Realize(machineConfig(controlplane), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept machine config on node 192.168.1.10", action.Text)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerAppliesWhenADocumentBehindACommentedSeparatorDiffers(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(controlplaneWithAVolume, nil)
	talos.EXPECT().
		ApplyMachineConfig(mock.Anything, mock.Anything, mock.Anything,
			controlplaneWithACommentedSeparatorAndABiggerVolume).
		Return(nil)

	action, err := r.Realize(
		machineConfig(controlplaneWithACommentedSeparatorAndABiggerVolume), plain)
	require.NoError(t, err)
	assert.Equal(t, "applied machine config to node 192.168.1.10", action.Text)
	assert.True(t, action.Changed)
}

func TestTheTalosRealizerKeepsAMachineConfigThatEndsInACommentOnlyDocument(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(controlplaneWithATrailingCommentOnlyDocument, nil)

	action, err := r.Realize(machineConfig(controlplane), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept machine config on node 192.168.1.10", action.Text)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerAppliesAMachineConfigThatDiffersFromTheNode(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(controlplane, nil)
	talos.EXPECT().
		ApplyMachineConfig(mock.Anything, "192.168.1.10", citypes.Secret("/home/operator/.config/t0.yaml"),
			controlplaneWithABiggerDisk).
		Return(nil)

	action, err := r.Realize(machineConfig(controlplaneWithABiggerDisk), plain)
	require.NoError(t, err)
	assert.Equal(t, "applied machine config to node 192.168.1.10", action.Text)
	assert.True(t, action.Changed)
}

func TestTheTalosRealizerAppliesAMachineConfigWhenADocumentWasAdded(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(controlplane, nil)
	talos.EXPECT().
		ApplyMachineConfig(mock.Anything, mock.Anything, mock.Anything, controlplaneWithAVolume).
		Return(nil)

	action, err := r.Realize(machineConfig(controlplaneWithAVolume), plain)
	require.NoError(t, err)
	assert.True(t, action.Changed)
}

func TestTheTalosRealizerReportsTheNodeWhenTheApplyFails(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(controlplane, nil)
	talos.EXPECT().ApplyMachineConfig(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(errors.New("node rejected the document"))

	_, err := r.Realize(machineConfig(controlplaneWithABiggerDisk), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "applying the machine config to node 192.168.1.10")
	assert.Contains(t, err.Error(), "node rejected the document")
}

func TestTheTalosRealizerReportsTheNodeWhenTheReadFails(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return("", errors.New("no route to host"))

	_, err := r.Realize(machineConfig(controlplane), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the machine config of node 192.168.1.10")
	assert.Contains(t, err.Error(), "no route to host")
}

func TestADryRunKeepsAndWritesNothingWhenTheNodeAlreadyHoldsTheConfig(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(controlplane, nil)

	action, err := r.Realize(machineConfig(controlplane), managercontroller.Options{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, "kept machine config on node 192.168.1.10", action.Text)
	assert.False(t, action.Changed)
	talos.AssertNotCalled(t, "ApplyMachineConfig",
		mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestADryRunKeepsAndWritesNothingWhenTheConfigDiffers(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(controlplane, nil)

	action, err := r.Realize(
		machineConfig(controlplaneWithABiggerDisk), managercontroller.Options{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, "would apply machine config to node 192.168.1.10", action.Text)
	assert.False(t, action.Changed)
	talos.AssertNotCalled(t, "ApplyMachineConfig",
		mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestTheTalosRealizerRefusesAnUnknownKindByName(t *testing.T) {
	r, _ := talosRealizer(t)

	_, err := r.Realize(citypes.Resource{Kind: "reboot", Name: "t0-controlplane"}, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot realize kind "reboot"`)
	assert.Contains(t, err.Error(), "machine-config")
}

func TestTheTalosRealizerRefusesAResourceWithoutANodeOrAConfig(t *testing.T) {
	r, _ := talosRealizer(t)

	for _, spec := range []map[string]any{
		{"config": controlplane},
		{"node": "192.168.1.10"},
	} {
		_, err := r.Realize(citypes.Resource{
			Kind: managercontroller.KindMachineConfig, Name: "t0-controlplane", Spec: spec,
		}, plain)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "spec.node and spec.config are required")
	}
}

func TestTheTalosRealizerNamesTheVariableItReadWhenNothingDeclaresOne(t *testing.T) {
	t.Setenv("TALOSCONFIG", "")

	r := managercontroller.NewTalosRealizer(t.Context(), managercontrollermock.NewMockTalos(t), "")

	_, err := r.Realize(machineConfig(controlplane), plain)
	require.Error(t, err)
	assert.Equal(t,
		"reading the client configuration path: nothing is set in TALOSCONFIG, which is read when "+
			"neither spec.talosconfigEnv nor the manager's talosconfigEnv is declared. "+
			"export it before bootstrapping",
		err.Error())
}

func TestTheTalosRealizerRefusesAnEmptyClientConfigurationVariable(t *testing.T) {
	t.Setenv(talosconfigVariable, "")

	r := managercontroller.NewTalosRealizer(
		t.Context(), managercontrollermock.NewMockTalos(t), talosconfigVariable)

	_, err := r.Realize(machineConfig(controlplane), plain)
	require.Error(t, err)
	assert.Equal(t,
		"reading the client configuration path: nothing is set in the variable the manager's "+
			"talosconfigEnv names. export it before bootstrapping",
		err.Error())
	assert.NotContains(t, err.Error(), talosconfigVariable)
}

func TestTheTalosRealizerNamesTheSlotThatFedTheClientConfigurationWhenTheNodeCannotBeRead(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, "192.168.1.10", citypes.Secret("/etc/t0/declared.yaml")).
		Return("", errors.New("no route to host"))

	res := machineConfig(controlplane)
	res.Spec["talosconfig"] = "/etc/t0/declared.yaml"

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"reading the machine config of node 192.168.1.10 using the client configuration from spec.talosconfig")
}

func TestTheTalosRealizerNamesTheSlotThatFedTheClientConfigurationWhenTheApplyFails(t *testing.T) {
	t.Setenv("FORGE_CI_TEST_OTHER_CLIENT_CONFIG", "/etc/t0/other.yaml")

	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, "192.168.1.10", citypes.Secret("/etc/t0/other.yaml")).
		Return(controlplane, nil)
	talos.EXPECT().
		ApplyMachineConfig(mock.Anything, "192.168.1.10", citypes.Secret("/etc/t0/other.yaml"), controlplaneWithABiggerDisk).
		Return(errors.New("no route to host"))

	res := machineConfig(controlplaneWithABiggerDisk)
	res.Spec["talosconfigEnv"] = "FORGE_CI_TEST_OTHER_CLIENT_CONFIG"

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"applying the machine config to node 192.168.1.10 using the client configuration from "+
			"the variable spec.talosconfigEnv names")
}

func TestTheTalosRealizerNeverEchoesASecretPastedIntoTheClientConfigurationVariableSlot(t *testing.T) {
	r := managercontroller.NewTalosRealizer(t.Context(), managercontrollermock.NewMockTalos(t), "")

	res := machineConfig(controlplane)
	res.Spec["talosconfigEnv"] = thePastedPrivateKey

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), thePastedPrivateKey)
	assert.NotContains(t, err.Error(), "BEGIN OPENSSH PRIVATE KEY")
	assert.Contains(t, err.Error(), "spec.talosconfigEnv")
}

func TestTheTalosRealizerReadsTheClientConfigurationPathFromTheResource(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, "192.168.1.10", citypes.Secret("/etc/t0/declared.yaml")).
		Return(controlplane, nil)

	res := machineConfig(controlplane)
	res.Spec["talosconfig"] = "/etc/t0/declared.yaml"

	action, err := r.Realize(res, plain)
	require.NoError(t, err)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerReadsTheClientConfigurationPathFromTheVariableTheResourceNames(t *testing.T) {
	t.Setenv("FORGE_CI_TEST_OTHER_CLIENT_CONFIG", "/etc/t0/other.yaml")

	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, "192.168.1.10", citypes.Secret("/etc/t0/other.yaml")).
		Return(controlplane, nil)

	res := machineConfig(controlplane)
	res.Spec["talosconfigEnv"] = "FORGE_CI_TEST_OTHER_CLIENT_CONFIG"

	action, err := r.Realize(res, plain)
	require.NoError(t, err)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerReportsLoadingTheConfigTheNodeHoldsWhenADocumentWillNotDecode(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(controlplane+"---\n\tnot: yaml\n", nil)

	_, err := r.Realize(machineConfig(controlplane), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "comparing the machine config of node 192.168.1.10")
	assert.Contains(t, err.Error(), "loading the machine config the node holds")
}

func TestTheTalosRealizerReportsLoadingTheDeclaredConfigWhenADocumentWillNotDecode(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(controlplane, nil)

	_, err := r.Realize(machineConfig("\tnot: yaml\n"), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading the declared machine config")
}

func TestTheTalosRealizerRefusesADeclaredConfigThatRepeatsAVolumeDocument(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(controlplane, nil)

	_, err := r.Realize(machineConfig(controlplaneWithTheVolumeTwice), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading the declared machine config")
	assert.Contains(t, err.Error(), "duplicate document v1alpha1/VolumeConfig/EPHEMERAL")
	assert.Contains(t, err.Error(), "line")
}

func TestTheTalosRealizerRefusesADeclaredConfigThatRepeatsTheVersionDocument(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(controlplane, nil)

	_, err := r.Realize(machineConfig(controlplaneTwice), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading the declared machine config")
	assert.Contains(t, err.Error(), "v1alpha1")
	assert.Contains(t, err.Error(), "line")
}

func TestTheTalosRealizerReportsTheNodeWhenTheDeclaredConfigCarriesNoVersionDocument(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(controlplane, nil)

	_, err := r.Realize(machineConfig(theVolume), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "comparing the machine config of node 192.168.1.10")
	assert.Contains(t, err.Error(), "patching the machine config the node holds")
}

func TestTheTalosRealizerRefusesASpecFieldThatHoldsSomethingOtherThanAString(t *testing.T) {
	for _, key := range []string{"node", "config", "talosconfig", "talosconfigEnv"} {
		t.Run(key, func(t *testing.T) {
			r, _ := talosRealizer(t)

			res := machineConfig(controlplane)
			res.Spec[key] = 7

			_, err := r.Realize(res, plain)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "reading spec."+key)
			assert.Contains(t, err.Error(), "a string is required")
			assert.Contains(t, err.Error(), "int")
		})
	}
}

func TestTheTalosRealizerRefusesToReachANodeWithNoClientWired(t *testing.T) {
	t.Setenv(talosconfigVariable, "/home/operator/.config/t0.yaml")

	r := managercontroller.NewTalosRealizer(t.Context(), nil, talosconfigVariable)

	_, err := r.Realize(machineConfig(controlplane), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the machine config of node 192.168.1.10")
	assert.Contains(t, err.Error(), "carries no node client yet")
}
