package talosadapter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	configresource "github.com/siderolabs/talos/pkg/machinery/resources/config"
)

func TestAnEmptyApplyModeMeansAuto(t *testing.T) {
	t.Parallel()

	node, err := New("")
	require.NoError(t, err)
	assert.Equal(t, machineapi.ApplyConfigurationRequest_AUTO, node.mode.request)
}

func TestAnAutoApplyComparesAgainstTheConfigTheNodeIsRunning(t *testing.T) {
	t.Parallel()

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)
	assert.Equal(t, configresource.ActiveID, node.mode.read)
}

func TestAStagedApplyComparesAgainstTheConfigTheNodeSavedAndNotTheRunningOne(t *testing.T) {
	t.Parallel()

	node, err := New(ApplyModeStaged)
	require.NoError(t, err)
	assert.Equal(t, machineapi.ApplyConfigurationRequest_STAGED, node.mode.request)
	assert.Equal(t, configresource.PersistentID, node.mode.read)
	assert.NotEqual(t, configresource.ActiveID, node.mode.read)
}

func TestAnUnknownApplyModeIsRefusedByName(t *testing.T) {
	t.Parallel()

	_, err := New("try")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `reading the apply mode "try"`)
	assert.Contains(t, err.Error(), "auto")
	assert.Contains(t, err.Error(), "staged")
}

const thePastedClientConfiguration = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"ZZZTOPSECRETZZZclientprivatekey\n" +
	"-----END OPENSSH PRIVATE KEY-----\n"

func TestReadingAMachineConfigNamesTheNodeAndNeverEchoesTheClientConfigurationSlot(t *testing.T) {
	t.Chdir(t.TempDir())

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	_, err = node.MachineConfig(t.Context(), "192.168.1.10", thePastedClientConfiguration)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dialing node 192.168.1.10")
	assert.Contains(t, err.Error(), "must name a talosconfig file")
	assert.NotContains(t, err.Error(), "ZZZTOPSECRETZZZclientprivatekey")
	assert.NotContains(t, err.Error(), "BEGIN OPENSSH PRIVATE KEY")
}

func TestApplyingAMachineConfigNamesTheNodeAndNeverEchoesTheClientConfigurationSlot(t *testing.T) {
	t.Chdir(t.TempDir())

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	err = node.ApplyMachineConfig(
		t.Context(), "192.168.1.10", thePastedClientConfiguration, "version: v1alpha1\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "dialing node 192.168.1.10")
	assert.Contains(t, err.Error(), "must name a talosconfig file")
	assert.NotContains(t, err.Error(), "ZZZTOPSECRETZZZclientprivatekey")
	assert.NotContains(t, err.Error(), "BEGIN OPENSSH PRIVATE KEY")
}
