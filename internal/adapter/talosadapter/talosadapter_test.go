package talosadapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	configresource "github.com/siderolabs/talos/pkg/machinery/resources/config"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
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

const theKeyMarker = "ZZZTOPSECRETZZZclientprivatekey"

var thePastedClientConfiguration = citypes.Secret("-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAABlwAAAAdzc2gtcn\n" +
	theKeyMarker + "\n" +
	strings.Repeat("cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n", 24) +
	"-----END OPENSSH PRIVATE KEY-----\n")

func TestThePastedClientConfigurationFixtureMatchesARealKeyInSizeAndClearsThePathCeiling(t *testing.T) {
	t.Parallel()

	assert.Greater(t, len(thePastedClientConfiguration), 1400)
	assert.Greater(t, len(thePastedClientConfiguration), 255)
}

func assertNothingEchoesTheSlot(t *testing.T, err error) {
	t.Helper()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "dialing node 192.168.1.10")
	assert.Contains(t, err.Error(), "must name a talosconfig file")
	assert.NotContains(t, err.Error(), theKeyMarker)
	assert.NotContains(t, err.Error(), "BEGIN OPENSSH PRIVATE KEY")
}

func TestReadingAMachineConfigNamesTheNodeAndNeverEchoesTheClientConfigurationSlot(t *testing.T) {
	t.Chdir(t.TempDir())

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	_, err = node.MachineConfig(t.Context(), "192.168.1.10", thePastedClientConfiguration)
	assertNothingEchoesTheSlot(t, err)
}

func TestApplyingAMachineConfigNamesTheNodeAndNeverEchoesTheClientConfigurationSlot(t *testing.T) {
	t.Chdir(t.TempDir())

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	err = node.ApplyMachineConfig(
		t.Context(), "192.168.1.10", thePastedClientConfiguration, "version: v1alpha1\n")
	assertNothingEchoesTheSlot(t, err)
}

func TestAPastedSlotShortEnoughToBeAFilenameIsNeverTurnedIntoAFileOnDisk(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	_, err = node.MachineConfig(t.Context(), "192.168.1.10", citypes.Secret("swordfish"))
	require.Error(t, err)

	left, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, left)
}

func TestAMissingClientConfigurationFileNamesTheReasonAndNotThePath(t *testing.T) {
	t.Chdir(t.TempDir())

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	_, err = node.MachineConfig(t.Context(), "192.168.1.10", "/etc/t0/absent.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such file or directory")
	assert.NotContains(t, err.Error(), "/etc/t0/absent.yaml")
}

func TestAClientConfigurationFileThatIsNotATalosconfigIsRefusedWithoutEchoingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "talosconfig")

	require.NoError(t, os.WriteFile(path, []byte(theKeyMarker+": ["), 0o600))

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	_, err = node.MachineConfig(t.Context(), "192.168.1.10", citypes.Secret(path))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a talosconfig document")
	assert.NotContains(t, err.Error(), theKeyMarker)
}
