package managercontroller_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/fsadaptermock"
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

const (
	talosconfigVariable = "FORGE_CI_TEST_CLIENT_CONFIG"
	recoveryVariable    = "FORGE_CI_TEST_RECOVERY_DIR"

	theRecoveryDirectory = "/home/operator/recovery"
	theBundle            = "cluster:\n  secret: the cluster CA private key\n"
	theSavedTalosconfig  = "context: t0\ncontexts:\n  t0:\n    key: the admin private key\n"
)

func talosRealizer(t *testing.T) (managercontroller.TalosRealizer, *managercontrollermock.MockTalos) {
	t.Helper()

	r, talos, _ := talosRealizerWithFiles(t)

	return r, talos
}

func talosRealizerWithFiles(t *testing.T) (
	managercontroller.TalosRealizer, *managercontrollermock.MockTalos, *fsadaptermock.MockFS,
) {
	t.Helper()
	t.Setenv(talosconfigVariable, "/home/operator/.config/t0.yaml")
	t.Setenv(recoveryVariable, theRecoveryDirectory)

	talos := managercontrollermock.NewMockTalos(t)
	files := fsadaptermock.NewMockFS(t)

	return managercontroller.NewTalosRealizer(t.Context(), talos, files), talos, files
}

func machineConfig(document string) citypes.Resource {
	return citypes.Resource{
		Kind: managercontroller.KindMachineConfig,
		Name: "t0-controlplane",
		Spec: map[string]any{
			"config":         document,
			"bundle":         theBundle,
			"talosconfigEnv": talosconfigVariable,
			"recoveryDirEnv": recoveryVariable,
		},
	}
}

func TestTheTalosRealizerNamesItselfTalos(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "talos", managercontroller.NewTalosRealizer(t.Context(), nil, nil).Kind())
}

func TestTheTalosRealizerKeepsAMachineConfigTheNodeAlreadyHolds(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().
		MachineConfig(mock.Anything, citypes.Secret("/home/operator/.config/t0.yaml")).
		Return(controlplane, nil)

	action, err := r.Realize(machineConfig(controlplane), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept machine config t0-controlplane", action.Text)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerKeepsAMachineConfigThatDiffersOnlyByKeyOrder(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(controlplaneReordered, nil)

	action, err := r.Realize(machineConfig(controlplane), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept machine config t0-controlplane", action.Text)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerKeepsAMultiDocumentMachineConfigTheNodeAlreadyHolds(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(controlplaneWithAVolume, nil)

	action, err := r.Realize(machineConfig(controlplaneWithAVolume), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept machine config t0-controlplane", action.Text)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerKeepsAMachineConfigWhoseDocumentsAreInADifferentOrder(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(controlplaneThenTheIngressRuleThenTheVolume, nil)

	action, err := r.Realize(machineConfig(controlplaneThenTheVolumeThenTheIngressRule), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept machine config t0-controlplane", action.Text)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerKeepsAMachineConfigWhoseVersionDocumentIsWrittenLast(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(theVolumeThenTheControlplane, nil)

	action, err := r.Realize(machineConfig(controlplaneWithAVolume), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept machine config t0-controlplane", action.Text)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerKeepsAMachineConfigWrappedInEmptyDocuments(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return("---\n"+controlplane+"---\n", nil)

	action, err := r.Realize(machineConfig(controlplane), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept machine config t0-controlplane", action.Text)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerAppliesWhenADocumentBehindACommentedSeparatorDiffers(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(controlplaneWithAVolume, nil)
	talos.EXPECT().
		ApplyMachineConfig(mock.Anything, mock.Anything,
			controlplaneWithACommentedSeparatorAndABiggerVolume).
		Return(nil)

	action, err := r.Realize(
		machineConfig(controlplaneWithACommentedSeparatorAndABiggerVolume), plain)
	require.NoError(t, err)
	assert.Equal(t, "applied machine config t0-controlplane", action.Text)
	assert.True(t, action.Changed)
}

func TestTheTalosRealizerKeepsAMachineConfigThatEndsInACommentOnlyDocument(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(controlplaneWithATrailingCommentOnlyDocument, nil)

	action, err := r.Realize(machineConfig(controlplane), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept machine config t0-controlplane", action.Text)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerAppliesAMachineConfigThatDiffersFromTheNode(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(controlplane, nil)
	talos.EXPECT().
		ApplyMachineConfig(mock.Anything, citypes.Secret("/home/operator/.config/t0.yaml"),
			controlplaneWithABiggerDisk).
		Return(nil)

	action, err := r.Realize(machineConfig(controlplaneWithABiggerDisk), plain)
	require.NoError(t, err)
	assert.Equal(t, "applied machine config t0-controlplane", action.Text)
	assert.True(t, action.Changed)
}

func TestTheTalosRealizerAppliesAMachineConfigWhenADocumentWasAdded(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(controlplane, nil)
	talos.EXPECT().
		ApplyMachineConfig(mock.Anything, mock.Anything, controlplaneWithAVolume).
		Return(nil)

	action, err := r.Realize(machineConfig(controlplaneWithAVolume), plain)
	require.NoError(t, err)
	assert.True(t, action.Changed)
}

func TestTheTalosRealizerReportsTheNodeWhenTheApplyFails(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(controlplane, nil)
	talos.EXPECT().ApplyMachineConfig(mock.Anything, mock.Anything, mock.Anything).
		Return(errors.New("node rejected the document"))

	_, err := r.Realize(machineConfig(controlplaneWithABiggerDisk), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "applying the machine config of t0-controlplane")
	assert.Contains(t, err.Error(), "node rejected the document")
}

func TestTheTalosRealizerReportsTheNodeWhenTheReadFails(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return("", errors.New("no route to host"))

	_, err := r.Realize(machineConfig(controlplane), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the machine config of t0-controlplane")
	assert.Contains(t, err.Error(), "no route to host")
}

func TestADryRunKeepsAndWritesNothingWhenTheNodeAlreadyHoldsTheConfig(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(controlplane, nil)

	action, err := r.Realize(machineConfig(controlplane), managercontroller.Options{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, "kept machine config t0-controlplane", action.Text)
	assert.False(t, action.Changed)
	talos.AssertNotCalled(t, "ApplyMachineConfig", mock.Anything, mock.Anything, mock.Anything)
}

func TestADryRunKeepsAndWritesNothingWhenTheConfigDiffers(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(controlplane, nil)

	action, err := r.Realize(
		machineConfig(controlplaneWithABiggerDisk), managercontroller.Options{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, "would apply machine config t0-controlplane", action.Text)
	assert.False(t, action.Changed)
	talos.AssertNotCalled(t, "ApplyMachineConfig", mock.Anything, mock.Anything, mock.Anything)
}

func TestTheTalosRealizerRefusesAnUnknownKindByName(t *testing.T) {
	r, _ := talosRealizer(t)

	_, err := r.Realize(citypes.Resource{Kind: "reboot", Name: "t0-controlplane"}, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot realize kind "reboot"`)
	assert.Contains(t, err.Error(), "machine-config")
	assert.Contains(t, err.Error(), "cluster-bootstrap")
	assert.Contains(t, err.Error(), "secret-bundle")
}

func recoveryPath(file string) string {
	return filepath.Join(theRecoveryDirectory, file)
}

func aRecoveryDirectoryHoldingNoBundle(files *fsadaptermock.MockFS) *mock.Call {
	return files.EXPECT().Exists(recoveryPath("secrets.yaml")).Return(false, nil).Call
}

func expectTheRecoveryDirectory(files *fsadaptermock.MockFS, file string, content string) []*mock.Call {
	return []*mock.Call{
		files.EXPECT().MkdirAll(theRecoveryDirectory).Return(nil).Call,
		files.EXPECT().Chmod(theRecoveryDirectory, os.FileMode(0o700)).Return(nil).Call,
		files.EXPECT().WriteFile(recoveryPath(file), []byte(content)).Return(nil).Call,
		files.EXPECT().Chmod(recoveryPath(file), os.FileMode(0o600)).Return(nil).Call,
	}
}

func TestAFirstRunWritesTheBundleTheTalosconfigAndItsRecoveryCopyBeforeTheApplyOnANodeInMaintenance(t *testing.T) {
	r, talos, files := talosRealizerWithFiles(t)

	read := talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).Return("", nil).Call
	free := aRecoveryDirectoryHoldingNoBundle(files)
	bundle := expectTheRecoveryDirectory(files, "secrets.yaml", theBundle)
	save := talos.EXPECT().
		SaveTalosconfig(citypes.Secret("/home/operator/.config/t0.yaml"), citypes.Secret(theBundle)).
		Return(citypes.Secret(theSavedTalosconfig), nil).Call
	copied := expectTheRecoveryDirectory(files, "talosconfig", theSavedTalosconfig)
	apply := talos.EXPECT().
		ApplyMachineConfig(mock.Anything, citypes.Secret("/home/operator/.config/t0.yaml"), controlplane).
		Return(nil).Call

	order := append([]*mock.Call{read, free}, bundle...)
	order = append(order, save)
	order = append(order, copied...)
	mock.InOrder(append(order, apply)...)

	action, err := r.Realize(machineConfig(controlplane), plain)
	require.NoError(t, err)
	assert.Equal(t, "applied machine config t0-controlplane", action.Text)
	assert.True(t, action.Changed)
	assert.NotContains(t, action.Text, theRecoveryDirectory)
}

func TestANodeThatHoldsAConfigNeverTouchesTheRecoveryDirectoryOrTheTalosconfig(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).Return(controlplane, nil)
	talos.EXPECT().ApplyMachineConfig(mock.Anything, mock.Anything, controlplaneWithABiggerDisk).Return(nil)

	_, err := r.Realize(machineConfig(controlplaneWithABiggerDisk), plain)
	require.NoError(t, err)
	talos.AssertNotCalled(t, "SaveTalosconfig", mock.Anything, mock.Anything)
}

func TestADryRunOnANodeInMaintenanceWritesNoRecoveryFileNoTalosconfigAndAppliesNothing(t *testing.T) {
	r, talos, files := talosRealizerWithFiles(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).Return("", nil)
	aRecoveryDirectoryHoldingNoBundle(files)

	action, err := r.Realize(machineConfig(controlplane), managercontroller.Options{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, "would apply machine config t0-controlplane", action.Text)
	assert.False(t, action.Changed)
	talos.AssertNotCalled(t, "SaveTalosconfig", mock.Anything, mock.Anything)
	talos.AssertNotCalled(t, "ApplyMachineConfig", mock.Anything, mock.Anything, mock.Anything)
}

var aRealRunAndADryRun = map[string]managercontroller.Options{
	"a real run": plain,
	"a dry run":  {DryRun: true},
}

func TestAFirstRunWithAnEmptyRecoveryVariableIsRefusedBeforeAnyWriteAndAnyApply(t *testing.T) {
	for name, opts := range aRealRunAndADryRun {
		t.Run(name, func(t *testing.T) {
			r, talos := talosRealizer(t)
			t.Setenv(recoveryVariable, "")
			talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).Return("", nil)

			_, err := r.Realize(machineConfig(controlplane), opts)
			require.Error(t, err)
			assert.Equal(t,
				"writing the recovery directory of t0-controlplane: nothing is set in "+
					"the variable spec.recoveryDirEnv names. export it before a first run",
				err.Error())
			talos.AssertNotCalled(t, "SaveTalosconfig", mock.Anything, mock.Anything)
			talos.AssertNotCalled(t, "ApplyMachineConfig", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestAFirstRunOverARecoveryDirectoryHoldingABundleIsRefusedNamingTheFileAndTheVariableBeforeAnyWriteAnySaveAndAnyApply(t *testing.T) {
	for name, opts := range aRealRunAndADryRun {
		t.Run(name, func(t *testing.T) {
			r, talos, files := talosRealizerWithFiles(t)
			talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).Return("", nil)
			files.EXPECT().Exists(recoveryPath("secrets.yaml")).Return(true, nil)

			_, err := r.Realize(machineConfig(controlplane), opts)
			require.Error(t, err)
			assert.Equal(t,
				"writing the recovery directory of t0-controlplane: the directory "+
					"the variable spec.recoveryDirEnv names already holds secrets.yaml, "+
					"and a first run never overwrites a bundle",
				err.Error())
			assert.NotContains(t, err.Error(), theRecoveryDirectory)
			files.AssertNotCalled(t, "MkdirAll", mock.Anything)
			files.AssertNotCalled(t, "WriteFile", mock.Anything, mock.Anything)
			talos.AssertNotCalled(t, "SaveTalosconfig", mock.Anything, mock.Anything)
			talos.AssertNotCalled(t, "ApplyMachineConfig", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestARecoveryDirectoryThatCannotBeReadIsRefusedWithoutThePathBeforeAnyWrite(t *testing.T) {
	for name, opts := range aRealRunAndADryRun {
		t.Run(name, func(t *testing.T) {
			r, talos, files := talosRealizerWithFiles(t)
			talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).Return("", nil)
			files.EXPECT().Exists(recoveryPath("secrets.yaml")).
				Return(false, fmt.Errorf("inspecting %s: %w", recoveryPath("secrets.yaml"),
					&fs.PathError{Op: "stat", Path: recoveryPath("secrets.yaml"), Err: fs.ErrPermission}))

			_, err := r.Realize(machineConfig(controlplane), opts)
			require.Error(t, err)
			assert.Equal(t, "reading the recovery directory of t0-controlplane: permission denied", err.Error())
			files.AssertNotCalled(t, "WriteFile", mock.Anything, mock.Anything)
			talos.AssertNotCalled(t, "ApplyMachineConfig", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestAFirstRunThatCannotSaveTheAdminCredentialAppliesNothing(t *testing.T) {
	r, talos, files := talosRealizerWithFiles(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).Return("", nil)
	aRecoveryDirectoryHoldingNoBundle(files)
	expectTheRecoveryDirectory(files, "secrets.yaml", theBundle)
	talos.EXPECT().SaveTalosconfig(mock.Anything, mock.Anything).
		Return("", errors.New("minting the admin credential: validating the talos secret bundle"))

	_, err := r.Realize(machineConfig(controlplane), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"saving the admin credential of t0-controlplane into the client configuration from "+
			"the variable spec.talosconfigEnv names")
	talos.AssertNotCalled(t, "ApplyMachineConfig", mock.Anything, mock.Anything, mock.Anything)
}

func TestARecoveryWriteThatFailsAppliesNothingAndNamesTheFileButNeverTheDirectory(t *testing.T) {
	for name, fail := range map[string]func(*fsadaptermock.MockFS){
		"the directory": func(files *fsadaptermock.MockFS) {
			files.EXPECT().MkdirAll(theRecoveryDirectory).
				Return(fmt.Errorf("creating %s: %w", theRecoveryDirectory,
					&fs.PathError{Op: "mkdir", Path: theRecoveryDirectory, Err: fs.ErrPermission}))
		},
		"the directory mode": func(files *fsadaptermock.MockFS) {
			files.EXPECT().MkdirAll(theRecoveryDirectory).Return(nil)
			files.EXPECT().Chmod(theRecoveryDirectory, os.FileMode(0o700)).
				Return(&fs.PathError{Op: "chmod", Path: theRecoveryDirectory, Err: fs.ErrPermission})
		},
		"the file": func(files *fsadaptermock.MockFS) {
			files.EXPECT().MkdirAll(theRecoveryDirectory).Return(nil)
			files.EXPECT().Chmod(theRecoveryDirectory, os.FileMode(0o700)).Return(nil)
			files.EXPECT().WriteFile(recoveryPath("secrets.yaml"), mock.Anything).
				Return(&fs.PathError{Op: "open", Path: recoveryPath("secrets.yaml"), Err: fs.ErrPermission})
		},
		"the file mode": func(files *fsadaptermock.MockFS) {
			files.EXPECT().MkdirAll(theRecoveryDirectory).Return(nil)
			files.EXPECT().Chmod(theRecoveryDirectory, os.FileMode(0o700)).Return(nil)
			files.EXPECT().WriteFile(recoveryPath("secrets.yaml"), mock.Anything).Return(nil)
			files.EXPECT().Chmod(recoveryPath("secrets.yaml"), os.FileMode(0o600)).
				Return(errors.New("the disk is gone"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, talos, files := talosRealizerWithFiles(t)
			talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).Return("", nil)
			aRecoveryDirectoryHoldingNoBundle(files)
			fail(files)

			_, err := r.Realize(machineConfig(controlplane), plain)
			require.Error(t, err)
			assert.Contains(t, err.Error(),
				"writing the secret bundle of t0-controlplane into the recovery directory")
			assert.NotContains(t, err.Error(), theRecoveryDirectory)
			assert.NotContains(t, err.Error(), "the cluster CA private key")
			talos.AssertNotCalled(t, "SaveTalosconfig", mock.Anything, mock.Anything)
			talos.AssertNotCalled(t, "ApplyMachineConfig", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestAFirstRunThatCannotCopyTheTalosconfigIntoTheRecoveryDirectoryAppliesNothing(t *testing.T) {
	r, talos, files := talosRealizerWithFiles(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).Return("", nil)
	aRecoveryDirectoryHoldingNoBundle(files)
	expectTheRecoveryDirectory(files, "secrets.yaml", theBundle)
	talos.EXPECT().SaveTalosconfig(mock.Anything, mock.Anything).
		Return(citypes.Secret(theSavedTalosconfig), nil)
	files.EXPECT().WriteFile(recoveryPath("talosconfig"), mock.Anything).Return(errors.New("the disk is gone"))

	_, err := r.Realize(machineConfig(controlplane), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "writing the talosconfig of t0-controlplane into the recovery directory")
	assert.NotContains(t, err.Error(), "the admin private key")
	talos.AssertNotCalled(t, "ApplyMachineConfig", mock.Anything, mock.Anything, mock.Anything)
}

func TestAMachineConfigThatNamesNoBundleOrNoRecoveryVariableIsRefusedByNameBeforeAnyDial(t *testing.T) {
	for key, says := range map[string]string{
		"bundle": "spec.bundle is required: the engine that declares the resource writes the cluster's secret bundle into it",
		"recoveryDirEnv": "spec.recoveryDirEnv is required: the resource names the variable holding the recovery directory, " +
			"and the engine that declares the resource writes it",
	} {
		t.Run(key, func(t *testing.T) {
			r, _ := talosRealizer(t)

			res := machineConfig(controlplane)
			delete(res.Spec, key)

			_, err := r.Realize(res, plain)
			require.Error(t, err)
			assert.Equal(t, says, err.Error())
		})
	}
}

func TestTheTalosRealizerStillRefusesADeclaredConfigThatWillNotLoadWhenTheNodeHoldsNoConfig(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).Return("", nil)

	_, err := r.Realize(machineConfig(controlplaneWithTheVolumeTwice), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading the declared machine config")
	talos.AssertNotCalled(t, "ApplyMachineConfig", mock.Anything, mock.Anything, mock.Anything)
}

func clusterBootstrap() citypes.Resource {
	return citypes.Resource{
		Kind: managercontroller.KindClusterBootstrap,
		Name: "t0",
		Spec: map[string]any{"talosconfigEnv": talosconfigVariable},
	}
}

func TestAResourceThatNamesNoTalosconfigVariableIsRefusedByNameBeforeAnyDialEvenWhenTALOSCONFIGIsExported(t *testing.T) {
	t.Setenv("TALOSCONFIG", "/home/operator/.talos/config")

	for name, res := range map[string]citypes.Resource{
		"a machine config with no key": {
			Kind: managercontroller.KindMachineConfig, Name: "t0", Spec: map[string]any{"config": controlplane},
		},
		"a machine config with an empty variable": {
			Kind: managercontroller.KindMachineConfig, Name: "t0",
			Spec: map[string]any{"config": controlplane, "talosconfigEnv": ""},
		},
		"a cluster bootstrap with no spec": {Kind: managercontroller.KindClusterBootstrap, Name: "t0"},
		"a secret bundle with no key": {
			Kind: managercontroller.KindSecretBundle, Name: "t0", Spec: map[string]any{"bundle": theBundle},
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := managercontroller.NewTalosRealizer(t.Context(), managercontrollermock.NewMockTalos(t), nil)

			if res.Kind == managercontroller.KindMachineConfig {
				res.Spec["bundle"] = theBundle
				res.Spec["recoveryDirEnv"] = recoveryVariable
			}

			_, err := r.Realize(res, plain)
			require.Error(t, err)
			assert.Equal(t,
				"spec.talosconfigEnv is required: the resource names the variable holding the talosconfig path, "+
					"and the engine that declares the resource writes it",
				err.Error())
		})
	}
}

func TestEveryKindRefusesTheRetiredNodeAndTalosconfigKeysByNameBeforeAnyDial(t *testing.T) {
	for _, res := range []citypes.Resource{machineConfig(controlplane), clusterBootstrap(), secretBundle()} {
		for key, because := range map[string]string{
			"node":        "the node comes from the talosconfig the variable spec.talosconfigEnv names",
			"talosconfig": "the talosconfig path comes from the variable spec.talosconfigEnv names",
		} {
			t.Run(res.Kind+" "+key, func(t *testing.T) {
				r, _ := talosRealizer(t)

				res.Spec[key] = "a retired value"
				defer delete(res.Spec, key)

				_, err := r.Realize(res, plain)
				require.Error(t, err)
				assert.Equal(t,
					"the talos manager refuses spec."+key+" on "+res.Kind+" "+res.Name+": "+because,
					err.Error())
			})
		}
	}
}

func TestTheTalosRealizerKeepsAClusterWhoseEtcdRunsOnceItsNodeRegistered(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().
		Bootstrapped(mock.Anything, citypes.Secret("/home/operator/.config/t0.yaml")).
		Return(true, nil)
	talos.EXPECT().
		WaitNodeRegistered(mock.Anything, citypes.Secret("/home/operator/.config/t0.yaml")).
		Return(nil)

	action, err := r.Realize(clusterBootstrap(), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept cluster bootstrap t0", action.Text)
	assert.False(t, action.Changed)
	talos.AssertNotCalled(t, "Bootstrap", mock.Anything, mock.Anything)
}

func TestTheTalosRealizerBootstrapsAClusterWhoseEtcdDoesNotRunAndWaitsForItsNode(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().Bootstrapped(mock.Anything, mock.Anything).Return(false, nil)
	talos.EXPECT().
		Bootstrap(mock.Anything, citypes.Secret("/home/operator/.config/t0.yaml")).
		Return(nil)
	talos.EXPECT().WaitNodeRegistered(mock.Anything, mock.Anything).Return(nil)

	action, err := r.Realize(clusterBootstrap(), plain)
	require.NoError(t, err)
	assert.Equal(t, "bootstrapped cluster t0", action.Text)
	assert.True(t, action.Changed)
}

func TestADryRunOverAClusterWhoseEtcdDoesNotRunSaysItWouldBootstrapAndTouchesNothing(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().Bootstrapped(mock.Anything, mock.Anything).Return(false, nil)

	action, err := r.Realize(clusterBootstrap(), managercontroller.Options{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, "would bootstrap cluster t0", action.Text)
	assert.False(t, action.Changed)
	talos.AssertNotCalled(t, "Bootstrap", mock.Anything, mock.Anything)
	talos.AssertNotCalled(t, "WaitNodeRegistered", mock.Anything, mock.Anything)
}

func TestTheTalosRealizerReportsTheNodeWhenTheBootstrapStateCannotBeRead(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().Bootstrapped(mock.Anything, mock.Anything).
		Return(false, errors.New("no route to host"))

	_, err := r.Realize(clusterBootstrap(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"reading the bootstrap state of t0 using the client configuration from "+
			"the variable spec.talosconfigEnv names")
	assert.Contains(t, err.Error(), "no route to host")
}

func TestTheTalosRealizerReportsTheNodeWhenTheBootstrapFails(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().Bootstrapped(mock.Anything, mock.Anything).Return(false, nil)
	talos.EXPECT().Bootstrap(mock.Anything, mock.Anything).
		Return(errors.New("etcd data directory is not empty"))

	_, err := r.Realize(clusterBootstrap(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bootstrapping t0 using the client configuration from")
	assert.Contains(t, err.Error(), "etcd data directory is not empty")
	talos.AssertNotCalled(t, "WaitNodeRegistered", mock.Anything, mock.Anything)
}

func TestTheTalosRealizerReportsTheNodeWhenItNeverRegisters(t *testing.T) {
	for name, bootstrapped := range map[string]bool{"after a bootstrap": false, "on a kept cluster": true} {
		t.Run(name, func(t *testing.T) {
			r, talos := talosRealizer(t)
			talos.EXPECT().Bootstrapped(mock.Anything, mock.Anything).Return(bootstrapped, nil)
			talos.EXPECT().Bootstrap(mock.Anything, mock.Anything).Return(nil).Maybe()
			talos.EXPECT().WaitNodeRegistered(mock.Anything, mock.Anything).
				Return(errors.New("the ceiling of 10m0s passed"))

			_, err := r.Realize(clusterBootstrap(), plain)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "waiting for the node of t0 to register")
			assert.Contains(t, err.Error(), "the ceiling of 10m0s passed")
		})
	}
}

func TestTheTalosRealizerRefusesToBootstrapWithNoClientWired(t *testing.T) {
	t.Setenv(talosconfigVariable, "/home/operator/.config/t0.yaml")

	r := managercontroller.NewTalosRealizer(t.Context(), nil, nil)

	_, err := r.Realize(clusterBootstrap(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the bootstrap state of t0")
	assert.Contains(t, err.Error(), "carries no node client yet")
}

func TestAClusterBootstrapWithNoClientConfigurationIsRefusedBeforeAnyDial(t *testing.T) {
	t.Setenv(talosconfigVariable, "")

	r := managercontroller.NewTalosRealizer(t.Context(), managercontrollermock.NewMockTalos(t), nil)

	_, err := r.Realize(clusterBootstrap(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the client configuration path: nothing is set")
}

func TestTheTalosRealizerRefusesAMachineConfigResourceWithoutAConfig(t *testing.T) {
	r, _ := talosRealizer(t)

	for _, spec := range []map[string]any{nil, {}, {"config": ""}} {
		_, err := r.Realize(citypes.Resource{
			Kind: managercontroller.KindMachineConfig, Name: "t0-controlplane", Spec: spec,
		}, plain)
		require.Error(t, err)
		assert.Equal(t, "spec.config is required", err.Error())
	}
}

func TestTheTalosRealizerRefusesAnEmptyClientConfigurationVariable(t *testing.T) {
	t.Setenv(talosconfigVariable, "")

	r := managercontroller.NewTalosRealizer(t.Context(), managercontrollermock.NewMockTalos(t), nil)

	_, err := r.Realize(machineConfig(controlplane), plain)
	require.Error(t, err)
	assert.Equal(t,
		"reading the client configuration path: nothing is set in the variable spec.talosconfigEnv names. "+
			"export it before bootstrapping",
		err.Error())
	assert.NotContains(t, err.Error(), talosconfigVariable)
}

func TestTheTalosRealizerNamesTheSlotThatFedTheClientConfigurationWhenTheApplyFails(t *testing.T) {
	t.Setenv("FORGE_CI_TEST_OTHER_CLIENT_CONFIG", "/etc/t0/other.yaml")

	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, citypes.Secret("/etc/t0/other.yaml")).
		Return(controlplane, nil)
	talos.EXPECT().
		ApplyMachineConfig(mock.Anything, citypes.Secret("/etc/t0/other.yaml"), controlplaneWithABiggerDisk).
		Return(errors.New("no route to host"))

	res := machineConfig(controlplaneWithABiggerDisk)
	res.Spec["talosconfigEnv"] = "FORGE_CI_TEST_OTHER_CLIENT_CONFIG"

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"applying the machine config of t0-controlplane using the client configuration from "+
			"the variable spec.talosconfigEnv names")
}

func TestTheTalosRealizerNeverEchoesASecretPastedIntoTheClientConfigurationVariableSlot(t *testing.T) {
	r := managercontroller.NewTalosRealizer(t.Context(), managercontrollermock.NewMockTalos(t), nil)

	res := machineConfig(controlplane)
	res.Spec["talosconfigEnv"] = thePastedPrivateKey

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), thePastedPrivateKey)
	assert.NotContains(t, err.Error(), "BEGIN OPENSSH PRIVATE KEY")
	assert.Contains(t, err.Error(), "spec.talosconfigEnv")
}

func TestTheTalosRealizerReadsTheClientConfigurationPathFromTheVariableTheResourceNames(t *testing.T) {
	t.Setenv("FORGE_CI_TEST_OTHER_CLIENT_CONFIG", "/etc/t0/other.yaml")

	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, citypes.Secret("/etc/t0/other.yaml")).
		Return(controlplane, nil)

	res := machineConfig(controlplane)
	res.Spec["talosconfigEnv"] = "FORGE_CI_TEST_OTHER_CLIENT_CONFIG"

	action, err := r.Realize(res, plain)
	require.NoError(t, err)
	assert.False(t, action.Changed)
}

func TestTheTalosRealizerReportsLoadingTheConfigTheNodeHoldsWhenADocumentWillNotDecode(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(controlplane+"---\n\tnot: yaml\n", nil)

	_, err := r.Realize(machineConfig(controlplane), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "comparing the machine config of t0-controlplane")
	assert.Contains(t, err.Error(), "loading the machine config the node holds")
}

func TestTheTalosRealizerReportsLoadingTheDeclaredConfigWhenADocumentWillNotDecode(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(controlplane, nil)

	_, err := r.Realize(machineConfig("\tnot: yaml\n"), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading the declared machine config")
}

func TestTheTalosRealizerRefusesADeclaredConfigThatRepeatsAVolumeDocument(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(controlplane, nil)

	_, err := r.Realize(machineConfig(controlplaneWithTheVolumeTwice), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading the declared machine config")
	assert.Contains(t, err.Error(), "duplicate document v1alpha1/VolumeConfig/EPHEMERAL")
	assert.Contains(t, err.Error(), "line")
}

func TestTheTalosRealizerRefusesADeclaredConfigThatRepeatsTheVersionDocument(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(controlplane, nil)

	_, err := r.Realize(machineConfig(controlplaneTwice), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading the declared machine config")
	assert.Contains(t, err.Error(), "v1alpha1")
	assert.Contains(t, err.Error(), "line")
}

func TestTheTalosRealizerReportsTheNodeWhenTheDeclaredConfigCarriesNoVersionDocument(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().MachineConfig(mock.Anything, mock.Anything).
		Return(controlplane, nil)

	_, err := r.Realize(machineConfig(theVolume), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "comparing the machine config of t0-controlplane")
	assert.Contains(t, err.Error(), "patching the machine config the node holds")
}

func TestTheTalosRealizerRefusesASpecFieldThatHoldsSomethingOtherThanAString(t *testing.T) {
	for _, key := range []string{"config", "bundle", "recoveryDirEnv", "talosconfigEnv"} {
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

	r := managercontroller.NewTalosRealizer(t.Context(), nil, nil)

	_, err := r.Realize(machineConfig(controlplane), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the machine config of t0-controlplane")
	assert.Contains(t, err.Error(), "carries no node client yet")
}

func secretBundle() citypes.Resource {
	return citypes.Resource{
		Kind: managercontroller.KindSecretBundle,
		Name: "t0",
		Spec: map[string]any{"bundle": theBundle, "talosconfigEnv": talosconfigVariable},
	}
}

func TestTheTalosRealizerStoresABundleTheClusterDoesNotHoldYet(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().StoredBundle(mock.Anything, citypes.Secret("/home/operator/.config/t0.yaml")).
		Return("", false, nil)
	talos.EXPECT().
		StoreBundle(mock.Anything, citypes.Secret("/home/operator/.config/t0.yaml"), citypes.Secret(theBundle)).
		Return(nil)

	action, err := r.Realize(secretBundle(), plain)
	require.NoError(t, err)
	assert.Equal(t, "stored secret bundle t0", action.Text)
	assert.True(t, action.Changed)
}

func TestTheTalosRealizerKeepsABundleTheClusterHoldsByteForByte(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().StoredBundle(mock.Anything, mock.Anything).Return(citypes.Secret(theBundle), true, nil)

	action, err := r.Realize(secretBundle(), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept secret bundle t0", action.Text)
	assert.False(t, action.Changed)
	talos.AssertNotCalled(t, "StoreBundle", mock.Anything, mock.Anything, mock.Anything)
}

func TestTheTalosRealizerRefusesByNameAClusterHoldingADifferentBundleAndNeverOverwritesIt(t *testing.T) {
	for name, opts := range map[string]managercontroller.Options{"a real run": plain, "a dry run": {DryRun: true}} {
		t.Run(name, func(t *testing.T) {
			r, talos := talosRealizer(t)
			talos.EXPECT().StoredBundle(mock.Anything, mock.Anything).
				Return(citypes.Secret("cluster:\n  secret: another cluster's key\n"), true, nil)

			_, err := r.Realize(secretBundle(), opts)
			require.Error(t, err)
			assert.Equal(t,
				"reading the secret bundle of t0: the cluster holds a different bundle in secret "+
					"kube-system/talos-secret-bundle, and this manager never overwrites one",
				err.Error())
			talos.AssertNotCalled(t, "StoreBundle", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestADryRunOverAnAbsentBundleSaysItWouldStoreAndStoresNothing(t *testing.T) {
	r, talos := talosRealizer(t)
	talos.EXPECT().StoredBundle(mock.Anything, mock.Anything).Return("", false, nil)

	action, err := r.Realize(secretBundle(), managercontroller.Options{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, "would store secret bundle t0", action.Text)
	assert.False(t, action.Changed)
	talos.AssertNotCalled(t, "StoreBundle", mock.Anything, mock.Anything, mock.Anything)
}

func TestEveryBundleFailureNamesTheResourceTheSlotAndTheCauseAndNeverTheBundle(t *testing.T) {
	for name, failure := range map[string]struct {
		expect func(*managercontrollermock.MockTalos)
		says   string
	}{
		"the read": {
			expect: func(talos *managercontrollermock.MockTalos) {
				talos.EXPECT().StoredBundle(mock.Anything, mock.Anything).
					Return("", false, errors.New("forbidden"))
			},
			says: "reading the secret bundle of t0 using the client configuration from " +
				"the variable spec.talosconfigEnv names: forbidden",
		},
		"the store": {
			expect: func(talos *managercontrollermock.MockTalos) {
				talos.EXPECT().StoredBundle(mock.Anything, mock.Anything).Return("", false, nil)
				talos.EXPECT().StoreBundle(mock.Anything, mock.Anything, mock.Anything).
					Return(errors.New("already exists"))
			},
			says: "storing the secret bundle of t0 using the client configuration from " +
				"the variable spec.talosconfigEnv names: already exists",
		},
	} {
		t.Run(name, func(t *testing.T) {
			r, talos := talosRealizer(t)
			failure.expect(talos)

			_, err := r.Realize(secretBundle(), plain)
			require.Error(t, err)
			assert.Equal(t, failure.says, err.Error())
			assert.NotContains(t, err.Error(), "the cluster CA private key")
		})
	}
}

func TestASecretBundleThatNamesNoBundleIsRefusedByNameBeforeAnyDial(t *testing.T) {
	for _, spec := range []map[string]any{
		{"talosconfigEnv": talosconfigVariable},
		{"talosconfigEnv": talosconfigVariable, "bundle": ""},
	} {
		r, _ := talosRealizer(t)

		_, err := r.Realize(citypes.Resource{Kind: managercontroller.KindSecretBundle, Name: "t0", Spec: spec}, plain)
		require.Error(t, err)
		assert.Equal(t,
			"spec.bundle is required: the engine that declares the resource writes the cluster's secret bundle into it",
			err.Error())
	}
}

func TestTheTalosRealizerRefusesToReadABundleWithNoClientWired(t *testing.T) {
	t.Setenv(talosconfigVariable, "/home/operator/.config/t0.yaml")

	_, err := managercontroller.NewTalosRealizer(t.Context(), nil, nil).Realize(secretBundle(), plain)
	require.Error(t, err)
	assert.Equal(t, "reading the secret bundle of t0: this manager carries no node client yet", err.Error())
}
