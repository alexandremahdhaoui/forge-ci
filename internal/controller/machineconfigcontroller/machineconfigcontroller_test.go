package machineconfigcontroller_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/containeradapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/talossecretsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/machineconfigcontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/fsadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/machineconfigcontrollermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	mintedBundle   = "bundle: the minted cluster CA private key\n"
	storedBundle   = "bundle: the stored cluster CA private key\n"
	loadedBundle   = "bundle: the loaded cluster CA private key\n"
	patchText      = "machine:\n  install:\n    disk: /dev/sda\n"
	renderedConfig = "version: v1alpha1\nmachine:\n  ca:\n    key: the rendered private key\n"

	theVariable         = "A_TALOSCONFIG_THIS_TEST_OWNS"
	theRecoveryVariable = "A_RECOVERY_DIRECTORY_THIS_TEST_OWNS"
	thePath             = "/home/a-person/talosconfig"
	theNode             = "node-a.example.invalid"
)

func theContext() citypes.TalosContext {
	return citypes.TalosContext{Endpoints: []string{"node-a.example.invalid:50000", "node-b.example.invalid"}, Node: theNode}
}

func theCluster() talossecretsadapter.Cluster {
	return talossecretsadapter.Cluster{
		Name: "t0", Endpoint: "https://node-a.example.invalid:6443", KubernetesVersion: "1.37.0",
	}
}

func fullSpec() map[string]any {
	return map[string]any{
		"clusterName":       "t0",
		"kubernetesVersion": "1.37.0",
		"talosconfigEnv":    theVariable,
		"recoveryDirEnv":    theRecoveryVariable,
		"patchFile":         "talos/controlplane.patch.yaml",
	}
}

func declare(
	t *testing.T, fs *fsadaptermock.MockFS, renderer *machineconfigcontrollermock.MockRenderer,
	talosconfig *machineconfigcontrollermock.MockTalosconfig, spec map[string]any,
) (citypes.DeclareOutput, error) {
	t.Helper()

	return machineconfigcontroller.New(fs, renderer, talosconfig, machineconfigcontrollermock.NewMockRegistry(t)).
		Declare(t.Context(), citypes.DeclareInput{Root: "/w", Spec: spec})
}

func filesOnDisk(t *testing.T) *fsadaptermock.MockFS {
	t.Helper()

	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().
		ReadFile(filepath.Join("/w", "talos", "controlplane.patch.yaml")).
		Return([]byte(patchText), nil).Maybe()

	return fs
}

func aRendererFor(t *testing.T, bundle string) *machineconfigcontrollermock.MockRenderer {
	t.Helper()

	renderer := machineconfigcontrollermock.NewMockRenderer(t)
	renderer.EXPECT().Load(citypes.Secret(bundle)).Return(citypes.Secret(loadedBundle), nil).Maybe()
	renderer.EXPECT().
		MachineConfig(citypes.Secret(loadedBundle), theCluster(), patchText).
		Return(citypes.Secret(renderedConfig), nil).Maybe()
	renderer.EXPECT().InstallerImage(citypes.Secret(renderedConfig)).Return(aDigestOnlyInstaller, nil).Maybe()

	return renderer
}

func aRendererNeverCalled(t *testing.T) *machineconfigcontrollermock.MockRenderer {
	t.Helper()

	return machineconfigcontrollermock.NewMockRenderer(t)
}

func aMintingRenderer(t *testing.T) *machineconfigcontrollermock.MockRenderer {
	t.Helper()

	renderer := aRendererFor(t, mintedBundle)
	renderer.EXPECT().Mint().Return(citypes.Secret(mintedBundle), nil).Maybe()

	return renderer
}

func aTalosconfig(t *testing.T) *machineconfigcontrollermock.MockTalosconfig {
	t.Helper()

	talosconfig := machineconfigcontrollermock.NewMockTalosconfig(t)
	talosconfig.EXPECT().Context(citypes.Secret(thePath)).Return(theContext(), nil).Maybe()

	return talosconfig
}

func aNodeInMaintenance(t *testing.T) *machineconfigcontrollermock.MockTalosconfig {
	t.Helper()

	talosconfig := aTalosconfig(t)
	talosconfig.EXPECT().InMaintenance(mock.Anything, citypes.Secret(thePath)).Return(true, nil).Maybe()

	return talosconfig
}

func aRunningCluster(t *testing.T) *machineconfigcontrollermock.MockTalosconfig {
	t.Helper()

	talosconfig := aTalosconfig(t)
	talosconfig.EXPECT().InMaintenance(mock.Anything, citypes.Secret(thePath)).Return(false, nil).Maybe()
	talosconfig.EXPECT().Bootstrapped(mock.Anything, citypes.Secret(thePath)).Return(true, nil).Maybe()
	talosconfig.EXPECT().StoredBundle(mock.Anything, citypes.Secret(thePath)).
		Return(citypes.Secret(storedBundle), true, nil).Maybe()

	return talosconfig
}

func exported(t *testing.T) {
	t.Helper()
	t.Setenv(theVariable, thePath)
}

func TestAFirstRunOnANodeInMaintenanceDeclaresTheConfigTheBootstrapAndTheMintedBundleEachNamedByTheCluster(t *testing.T) {
	exported(t)

	out, err := declare(t, filesOnDisk(t), aMintingRenderer(t), aNodeInMaintenance(t), fullSpec())

	require.NoError(t, err)
	require.Len(t, out.Resources, 3)
	assert.Equal(t, citypes.Resource{
		Kind: "machine-config",
		Name: "t0",
		Spec: map[string]any{
			"config":         citypes.Secret(renderedConfig),
			"bundle":         citypes.Secret(mintedBundle),
			"talosconfigEnv": theVariable,
			"recoveryDirEnv": theRecoveryVariable,
		},
	}, out.Resources[0])
	assert.Equal(t, citypes.Resource{
		Kind: "cluster-bootstrap",
		Name: "t0",
		Spec: map[string]any{"talosconfigEnv": theVariable},
	}, out.Resources[1])
	assert.Equal(t, citypes.Resource{
		Kind: "secret-bundle",
		Name: "t0",
		Spec: map[string]any{"bundle": citypes.Secret(mintedBundle), "talosconfigEnv": theVariable},
	}, out.Resources[2])
}

func TestTheMachineConfigAndTheSecretBundleCarryIdenticalBundleBytesAsSecretsThatNeverPrint(t *testing.T) {
	exported(t)

	for name, talosconfig := range map[string]func(*testing.T) *machineconfigcontrollermock.MockTalosconfig{
		"a first run": aNodeInMaintenance,
		"a later run": aRunningCluster,
	} {
		t.Run(name, func(t *testing.T) {
			renderer := aMintingRenderer(t)
			renderer.EXPECT().Load(citypes.Secret(storedBundle)).Return(citypes.Secret(loadedBundle), nil).Maybe()

			out, err := declare(t, filesOnDisk(t), renderer, talosconfig(t), fullSpec())
			require.NoError(t, err)

			onTheConfig, isSecret := out.Resources[0].Spec["bundle"].(citypes.Secret)
			require.True(t, isSecret, "the machine config carries the bundle as a secret")

			onTheBundle, isSecret := out.Resources[2].Spec["bundle"].(citypes.Secret)
			require.True(t, isSecret, "the secret bundle carries the bundle as a secret")

			assert.Equal(t, onTheConfig, onTheBundle)

			printed := fmt.Sprintf("%v %+v", out, out.Resources)
			for _, secret := range []string{mintedBundle, storedBundle, renderedConfig} {
				assert.NotContains(t, printed, strings.TrimSpace(secret))
			}
		})
	}
}

func TestAFirstRunNeverReadsTheCluster(t *testing.T) {
	exported(t)

	talosconfig := aNodeInMaintenance(t)

	_, err := declare(t, filesOnDisk(t), aMintingRenderer(t), talosconfig, fullSpec())

	require.NoError(t, err)
	talosconfig.AssertNotCalled(t, "Bootstrapped", mock.Anything, mock.Anything)
	talosconfig.AssertNotCalled(t, "StoredBundle", mock.Anything, mock.Anything)
}

func TestALaterRunRendersFromTheBundleTheClusterHoldsAndNeverMints(t *testing.T) {
	exported(t)

	renderer := aRendererFor(t, storedBundle)

	out, err := declare(t, filesOnDisk(t), renderer, aRunningCluster(t), fullSpec())

	require.NoError(t, err)
	assert.Equal(t, citypes.Secret(storedBundle), out.Resources[0].Spec["bundle"])
	assert.Equal(t, citypes.Secret(storedBundle), out.Resources[2].Spec["bundle"])
	assert.Equal(t, citypes.Secret(renderedConfig), out.Resources[0].Spec["config"])
	renderer.AssertNotCalled(t, "Mint")
}

func TestTheRendererReceivesTheLoadedBundleTheClusterEndpointBuiltFromTheFirstContextEndpointAndThePatchText(t *testing.T) {
	exported(t)

	renderer := machineconfigcontrollermock.NewMockRenderer(t)
	renderer.EXPECT().Mint().Return(citypes.Secret(mintedBundle), nil).Once()
	renderer.EXPECT().Load(citypes.Secret(mintedBundle)).Return(citypes.Secret(loadedBundle), nil).Once()
	renderer.EXPECT().
		MachineConfig(citypes.Secret(loadedBundle), theCluster(), patchText).
		Return(citypes.Secret(renderedConfig), nil).Once()
	renderer.EXPECT().InstallerImage(citypes.Secret(renderedConfig)).Return(aDigestOnlyInstaller, nil).Once()

	_, err := declare(t, filesOnDisk(t), renderer, aNodeInMaintenance(t), fullSpec())

	require.NoError(t, err)
}

func TestAContextEndpointWithoutAPortStillBecomesTheClusterEndpointOnPort6443(t *testing.T) {
	exported(t)

	talosconfig := machineconfigcontrollermock.NewMockTalosconfig(t)
	talosconfig.EXPECT().Context(citypes.Secret(thePath)).
		Return(citypes.TalosContext{Endpoints: []string{"node-a.example.invalid"}, Node: theNode}, nil)
	talosconfig.EXPECT().InMaintenance(mock.Anything, mock.Anything).Return(true, nil)

	_, err := declare(t, filesOnDisk(t), aMintingRenderer(t), talosconfig, fullSpec())

	require.NoError(t, err)
}

func TestANodeThatHoldsAConfigAndRunsNoClusterIsRefusedNamingThatStateAndTheRecoveryDirectory(t *testing.T) {
	exported(t)

	talosconfig := aTalosconfig(t)
	talosconfig.EXPECT().InMaintenance(mock.Anything, mock.Anything).Return(false, nil)
	talosconfig.EXPECT().Bootstrapped(mock.Anything, citypes.Secret(thePath)).Return(false, nil)

	renderer := machineconfigcontrollermock.NewMockRenderer(t)

	_, err := declare(t, filesOnDisk(t), renderer, talosconfig, fullSpec())

	require.Error(t, err)
	assert.Equal(t,
		"reading the secret bundle of cluster t0: its node holds a machine config and runs no cluster yet, "+
			"so no cluster holds the bundle. a first run stopped between the apply and the bootstrap, "+
			"and the bundle it minted is secrets.yaml in the recovery directory the variable "+
			theRecoveryVariable+" names",
		err.Error())
	talosconfig.AssertNotCalled(t, "StoredBundle", mock.Anything, mock.Anything)
	renderer.AssertNotCalled(t, "Mint")
}

func TestAClusterHoldingNoBundleSecretIsRefusedPrintingTheOneCommandThatWritesItFromTheRecoveryDirectory(t *testing.T) {
	exported(t)

	talosconfig := aTalosconfig(t)
	talosconfig.EXPECT().InMaintenance(mock.Anything, mock.Anything).Return(false, nil)
	talosconfig.EXPECT().Bootstrapped(mock.Anything, mock.Anything).Return(true, nil)
	talosconfig.EXPECT().StoredBundle(mock.Anything, citypes.Secret(thePath)).Return("", false, nil)

	renderer := machineconfigcontrollermock.NewMockRenderer(t)

	_, err := declare(t, filesOnDisk(t), renderer, talosconfig, fullSpec())

	require.Error(t, err)
	assert.Equal(t,
		"reading the secret bundle of cluster t0: the cluster holds no secret kube-system/talos-secret-bundle. "+
			"write it once from the recovery directory: kubectl --namespace kube-system create secret generic "+
			"talos-secret-bundle --from-file=bundle.yaml=\"$"+theRecoveryVariable+"/secrets.yaml\"",
		err.Error())
	renderer.AssertNotCalled(t, "Mint")
}

func TestEveryFailureOnTheWayToTheBundleIsAnErrorNamingTheClusterAndTheStep(t *testing.T) {
	exported(t)

	for name, failure := range map[string]struct {
		talosconfig func(*testing.T) *machineconfigcontrollermock.MockTalosconfig
		renderer    func(*testing.T) *machineconfigcontrollermock.MockRenderer
		says        string
	}{
		"the probe": {
			talosconfig: func(t *testing.T) *machineconfigcontrollermock.MockTalosconfig {
				talosconfig := aTalosconfig(t)
				talosconfig.EXPECT().InMaintenance(mock.Anything, mock.Anything).
					Return(false, errors.New("not a talosconfig document"))

				return talosconfig
			},
			renderer: aRendererNeverCalled,
			says:     "probing the node of cluster t0: not a talosconfig document",
		},
		"the mint": {
			talosconfig: aNodeInMaintenance,
			renderer: func(t *testing.T) *machineconfigcontrollermock.MockRenderer {
				renderer := machineconfigcontrollermock.NewMockRenderer(t)
				renderer.EXPECT().Mint().Return("", errors.New("no entropy"))

				return renderer
			},
			says: "minting the secret bundle of cluster t0: no entropy",
		},
		"the bootstrap state": {
			talosconfig: func(t *testing.T) *machineconfigcontrollermock.MockTalosconfig {
				talosconfig := aTalosconfig(t)
				talosconfig.EXPECT().InMaintenance(mock.Anything, mock.Anything).Return(false, nil)
				talosconfig.EXPECT().Bootstrapped(mock.Anything, mock.Anything).
					Return(false, errors.New("connection refused"))

				return talosconfig
			},
			renderer: aRendererNeverCalled,
			says:     "reading the bootstrap state of cluster t0: connection refused",
		},
		"the stored bundle": {
			talosconfig: func(t *testing.T) *machineconfigcontrollermock.MockTalosconfig {
				talosconfig := aTalosconfig(t)
				talosconfig.EXPECT().InMaintenance(mock.Anything, mock.Anything).Return(false, nil)
				talosconfig.EXPECT().Bootstrapped(mock.Anything, mock.Anything).Return(true, nil)
				talosconfig.EXPECT().StoredBundle(mock.Anything, mock.Anything).
					Return("", false, errors.New("forbidden"))

				return talosconfig
			},
			renderer: aRendererNeverCalled,
			says:     "reading the secret bundle of cluster t0 from the cluster: forbidden",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := declare(t, filesOnDisk(t), failure.renderer(t), failure.talosconfig(t), fullSpec())

			require.Error(t, err)
			assert.Equal(t, failure.says, err.Error())
		})
	}
}

func TestAVariableHoldingNothingIsRefusedNamingTheClusterAndTheVariableBeforeAnyFileIsRead(t *testing.T) {
	t.Setenv(theVariable, "")

	_, err := declare(t,
		fsadaptermock.NewMockFS(t),
		machineconfigcontrollermock.NewMockRenderer(t),
		machineconfigcontrollermock.NewMockTalosconfig(t),
		fullSpec())

	require.Error(t, err)
	assert.Equal(t,
		"reading the client configuration of cluster t0: nothing is set in "+theVariable+
			", and spec.talosconfigEnv names it. export it before declaring", err.Error())
}

func TestATalosconfigTheAdapterRefusesIsAnErrorNamingTheClusterAndNeverThePath(t *testing.T) {
	exported(t)

	talosconfig := machineconfigcontrollermock.NewMockTalosconfig(t)
	talosconfig.EXPECT().Context(citypes.Secret(thePath)).
		Return(citypes.TalosContext{}, errors.New("context \"t0\" names 2 nodes"))

	_, err := declare(t,
		fsadaptermock.NewMockFS(t), machineconfigcontrollermock.NewMockRenderer(t), talosconfig, fullSpec())

	require.Error(t, err)
	assert.Equal(t,
		"reading the client configuration of cluster t0: context \"t0\" names 2 nodes", err.Error())
	assert.NotContains(t, err.Error(), thePath)
}

func TestAMissingPatchFileIsRefusedNamingTheClusterAndTheFileBeforeTheNodeIsReached(t *testing.T) {
	exported(t)

	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().
		ReadFile(filepath.Join("/w", "talos", "controlplane.patch.yaml")).
		Return(nil, errors.New("no such file"))

	_, err := declare(t, fs, machineconfigcontrollermock.NewMockRenderer(t), aTalosconfig(t), fullSpec())

	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"reading the patch document of cluster t0 from talos/controlplane.patch.yaml")
	assert.Contains(t, err.Error(), "no such file")
}

func TestEachAbsentSpecKeyIsRefusedByNameBeforeAnythingIsRead(t *testing.T) {
	exported(t)

	for _, key := range []string{"clusterName", "kubernetesVersion", "talosconfigEnv", "recoveryDirEnv", "patchFile"} {
		t.Run(key, func(t *testing.T) {
			spec := fullSpec()
			delete(spec, key)

			_, err := declare(t,
				fsadaptermock.NewMockFS(t),
				machineconfigcontrollermock.NewMockRenderer(t),
				machineconfigcontrollermock.NewMockTalosconfig(t),
				spec)

			require.Error(t, err)
			assert.Equal(t, "this engine needs spec."+key+" and the spec does not name it", err.Error())
		})
	}
}

func TestEachRetiredKeyIsRefusedByNameBeforeAnythingIsRead(t *testing.T) {
	exported(t)

	for key, because := range map[string]string{
		"endpoint": "the node and the endpoint come from the talosconfig the variable spec.talosconfigEnv names",
		"nodes":    "the node and the endpoint come from the talosconfig the variable spec.talosconfigEnv names",
		"bundleFile": "the bundle is minted on a node in maintenance and read from the cluster after, " +
			"and no bundle file is ever read",
	} {
		t.Run(key, func(t *testing.T) {
			spec := fullSpec()
			spec[key] = "a retired value"

			_, err := declare(t,
				fsadaptermock.NewMockFS(t),
				machineconfigcontrollermock.NewMockRenderer(t),
				machineconfigcontrollermock.NewMockTalosconfig(t),
				spec)

			require.Error(t, err)
			assert.Equal(t, "this engine refuses spec."+key+": "+because, err.Error())
		})
	}
}

func TestASpecKeyThatIsNotAStringIsRefusedByName(t *testing.T) {
	exported(t)

	spec := fullSpec()
	spec["kubernetesVersion"] = 1.37

	_, err := declare(t,
		fsadaptermock.NewMockFS(t),
		machineconfigcontrollermock.NewMockRenderer(t),
		machineconfigcontrollermock.NewMockTalosconfig(t),
		spec)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.kubernetesVersion")
}

func TestABundleTheRendererRefusesIsAnErrorNamingTheCluster(t *testing.T) {
	exported(t)

	renderer := machineconfigcontrollermock.NewMockRenderer(t)
	renderer.EXPECT().Load(citypes.Secret(storedBundle)).
		Return("", errors.New("reading the stored talos secret bundle: it is not a secret bundle document"))

	_, err := declare(t, filesOnDisk(t), renderer, aRunningCluster(t), fullSpec())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "loading the secret bundle of cluster t0")
}

func TestNoRefusalEverCarriesBundlePatchConfigOrTalosconfigBytes(t *testing.T) {
	exported(t)

	failures := map[string]func(*testing.T) *machineconfigcontrollermock.MockRenderer{
		"the bundle does not load": func(t *testing.T) *machineconfigcontrollermock.MockRenderer {
			renderer := machineconfigcontrollermock.NewMockRenderer(t)
			renderer.EXPECT().Load(citypes.Secret(storedBundle)).Return("", errors.New("not a bundle"))

			return renderer
		},
		"the config does not render": func(t *testing.T) *machineconfigcontrollermock.MockRenderer {
			renderer := machineconfigcontrollermock.NewMockRenderer(t)
			renderer.EXPECT().Load(citypes.Secret(storedBundle)).Return(citypes.Secret(loadedBundle), nil)
			renderer.EXPECT().
				MachineConfig(citypes.Secret(loadedBundle), theCluster(), patchText).
				Return(citypes.Secret(renderedConfig), errors.New("patching failed"))

			return renderer
		},
	}

	for name, failure := range failures {
		t.Run(name, func(t *testing.T) {
			_, err := declare(t, filesOnDisk(t), failure(t), aRunningCluster(t), fullSpec())

			require.Error(t, err)

			for _, secret := range []string{storedBundle, loadedBundle, patchText, renderedConfig, thePath} {
				assert.False(t, strings.Contains(err.Error(), strings.TrimSpace(secret)),
					"the refusal carries bytes it must never print")
			}
		})
	}
}

const (
	aDigestNoRegistryServes = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	aDigestOnlyInstaller    = "factory.example.invalid/metal-installer@" + aDigestNoRegistryServes
)

func TestAnInstallerImageWithNoDigestIsRefusedNamingTheReferenceWithNoRegistryCall(t *testing.T) {
	exported(t)

	for name, reference := range map[string]string{
		"a tag and no digest":           "factory.example.invalid/metal-installer:v1.14.0",
		"a registry port and no digest": "registry.example.invalid:5000/metal-installer:v1.14.0",
		"no tag and no digest":          "factory.example.invalid/metal-installer",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := declare(t, filesOnDisk(t), aMintingRendererPinning(t, reference), aNodeInMaintenance(t), fullSpec())
			require.Error(t, err)
			assert.Equal(t,
				"checking the installer image "+reference+" of cluster t0: it names no digest, "+
					"so a rebuild behind its tag would change what installs. pin it by digest",
				err.Error())
		})
	}
}

func TestAnInstallerImageWithADigestAndNoTagIsDeclaredWithNoRegistryCall(t *testing.T) {
	exported(t)

	for name, reference := range map[string]string{
		"a digest":                   aDigestOnlyInstaller,
		"a registry port and digest": "registry.example.invalid:5000/metal-installer@" + aDigestNoRegistryServes,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := declare(t, filesOnDisk(t), aMintingRendererPinning(t, reference), aNodeInMaintenance(t), fullSpec())
			require.NoError(t, err)
			require.Len(t, out.Resources, 3)
		})
	}
}

func TestARenderedConfigWithNoInstallerImageInItsUnattendedInstallDocumentIsRefusedNamingTheDocument(t *testing.T) {
	exported(t)

	_, err := declare(t, filesOnDisk(t), aMintingRendererPinning(t, ""), aNodeInMaintenance(t), fullSpec())
	require.Error(t, err)
	assert.Equal(t,
		"reading the installer image of cluster t0: the rendered config holds no installer image "+
			"in an UnattendedInstallConfig document. declare installer.image there, pinned by digest",
		err.Error())
}

func aRegistryServingAnInstaller(t *testing.T) (string, string) {
	t.Helper()

	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)

	tagged := strings.TrimPrefix(server.URL, "http://") + "/metal-installer:v1.14.0"

	ref, err := name.NewTag(tagged)
	require.NoError(t, err)

	img, err := random.Image(64, 1)
	require.NoError(t, err)
	require.NoError(t, remote.Write(ref, img))

	served, err := img.Digest()
	require.NoError(t, err)

	return tagged, served.String()
}

func anUnreachableRegistryTag(t *testing.T) string {
	t.Helper()

	server := httptest.NewServer(registry.New())
	tagged := strings.TrimPrefix(server.URL, "http://") + "/metal-installer:v1.14.0"
	server.Close()

	return tagged
}

func aMintingRendererReadingTheInstaller(
	t *testing.T, reference string, err error,
) *machineconfigcontrollermock.MockRenderer {
	t.Helper()

	renderer := machineconfigcontrollermock.NewMockRenderer(t)
	renderer.EXPECT().Mint().Return(citypes.Secret(mintedBundle), nil)
	renderer.EXPECT().Load(citypes.Secret(mintedBundle)).Return(citypes.Secret(loadedBundle), nil)
	renderer.EXPECT().
		MachineConfig(citypes.Secret(loadedBundle), theCluster(), patchText).
		Return(citypes.Secret(renderedConfig), nil)
	renderer.EXPECT().InstallerImage(citypes.Secret(renderedConfig)).Return(reference, err)

	return renderer
}

func aMintingRendererPinning(t *testing.T, reference string) *machineconfigcontrollermock.MockRenderer {
	t.Helper()

	return aMintingRendererReadingTheInstaller(t, reference, nil)
}

func declareAgainstTheRegistry(t *testing.T, renderer *machineconfigcontrollermock.MockRenderer) (citypes.DeclareOutput, error) {
	t.Helper()

	return machineconfigcontroller.New(filesOnDisk(t), renderer, aNodeInMaintenance(t), &containeradapter.Remote{}).
		Declare(t.Context(), citypes.DeclareInput{Root: "/w", Spec: fullSpec()})
}

func TestAnInstallerTagThatStillResolvesToItsPinnedDigestIsDeclared(t *testing.T) {
	exported(t)

	tagged, served := aRegistryServingAnInstaller(t)

	out, err := declareAgainstTheRegistry(t, aMintingRendererPinning(t, tagged+"@"+served))
	require.NoError(t, err)
	require.Len(t, out.Resources, 3)
}

func TestAnInstallerTagThatMovedOffItsPinnedDigestIsRefusedNamingTheReferenceTheTagAndBothDigests(t *testing.T) {
	exported(t)

	tagged, served := aRegistryServingAnInstaller(t)
	reference := tagged + "@" + aDigestNoRegistryServes

	_, err := declareAgainstTheRegistry(t, aMintingRendererPinning(t, reference))
	require.Error(t, err)
	assert.Equal(t,
		"checking the installer image "+reference+" of cluster t0: the tag v1.14.0 resolves to "+served+
			", and the reference pins "+aDigestNoRegistryServes,
		err.Error())
}

func TestAnInstallerRegistryThatCannotBeReachedIsRefusedNamingTheReference(t *testing.T) {
	exported(t)

	reference := anUnreachableRegistryTag(t) + "@" + aDigestNoRegistryServes

	_, err := declareAgainstTheRegistry(t, aMintingRendererPinning(t, reference))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving the installer image "+reference+" of cluster t0: ")
}

func TestAnInstallerRegistryThatAcceptsAndNeverAnswersIsRefusedNamingTheReferenceAndTheCeiling(t *testing.T) {
	exported(t)

	silent := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(silent.Close)

	reference := strings.TrimPrefix(silent.URL, "http://") + "/metal-installer:v1.14.0@" + aDigestNoRegistryServes

	shortened, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	_, err := machineconfigcontroller.New(
		filesOnDisk(t), aMintingRendererPinning(t, reference), aNodeInMaintenance(t), &containeradapter.Remote{},
	).Declare(shortened, citypes.DeclareInput{Root: "/w", Spec: fullSpec()})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(),
		"resolving the installer image "+reference+" of cluster t0: the registry did not answer within 30s: ")
}

func TestTheRegistryIsAskedWithADeadlineThirtySecondsAway(t *testing.T) {
	exported(t)

	var deadline time.Time

	registry := machineconfigcontrollermock.NewMockRegistry(t)
	registry.EXPECT().Digest(mock.Anything, "registry.example.invalid/metal-installer:v1.14.0").
		RunAndReturn(func(ctx context.Context, _ string) (string, error) {
			deadline, _ = ctx.Deadline()

			return aDigestNoRegistryServes, nil
		}).Once()

	asked := time.Now()

	_, err := machineconfigcontroller.New(
		filesOnDisk(t),
		aMintingRendererPinning(t, "registry.example.invalid/metal-installer:v1.14.0@"+aDigestNoRegistryServes),
		aNodeInMaintenance(t), registry,
	).Declare(t.Context(), citypes.DeclareInput{Root: "/w", Spec: fullSpec()})
	require.NoError(t, err)
	require.False(t, deadline.IsZero(), "the registry call carries a deadline")
	assert.WithinDuration(t, asked.Add(30*time.Second), deadline, 5*time.Second)
}

func TestAnInstallerImageTheRendererCannotReadIsAnErrorNamingTheCluster(t *testing.T) {
	exported(t)

	_, err := declareAgainstTheRegistry(t, aMintingRendererReadingTheInstaller(t, "", errors.New("it does not load")))
	require.Error(t, err)
	assert.Equal(t, "reading the installer image of cluster t0: it does not load", err.Error())
}

func TestPublishRefusesByNameBecauseThisEngineOnlyDeclares(t *testing.T) {
	t.Parallel()

	err := machineconfigcontroller.New(
		fsadaptermock.NewMockFS(t),
		machineconfigcontrollermock.NewMockRenderer(t),
		machineconfigcontrollermock.NewMockTalosconfig(t),
		machineconfigcontrollermock.NewMockRegistry(t),
	).Publish()

	require.ErrorIs(t, err, machineconfigcontroller.ErrNeverPublishes)
	assert.Equal(t,
		"this engine declares machine configs and never publishes: no substage may name it",
		err.Error())
}
