//go:build e2e

package e2e_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/siderolabs/talos/pkg/machinery/constants"
	"github.com/stretchr/testify/require"
)

const (
	theTalosEndpoint = "127.0.0.1:1"

	theTalosNode = "127.0.0.1"

	theTalosCluster = "t0"

	theTalosconfigVariable = "FORGE_CI_E2E_TALOSCONFIG"

	theRecoveryVariable = "FORGE_CI_E2E_TALOS_RECOVERY_DIR"
)

const talosconfigDocument = `context: ` + theTalosCluster + `
contexts:
  ` + theTalosCluster + `:
    endpoints:
      - ` + theTalosEndpoint + `
    nodes:
      - ` + theTalosNode + `
`

const controlplanePatchDocument = `apiVersion: v1alpha1
kind: VolumeConfig
name: EPHEMERAL
provisioning:
  maxSize: 80%
---
apiVersion: v1alpha1
kind: KubeProxyConfig
enabled: false
`

func talosPipelineYAML(root, statePath string) string {
	return `name: demo
repos:
  - name: demo-repo
    url: file://` + filepath.Join(root, "demo-repo") + `
managers:
  - alias: local
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-local@v0.1.0"
    spec:
      statePath: ` + filepath.Join(root, "manager-local.json") + `
  - alias: talos
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-talos@v0.1.0"
    spec:
      statePath: ` + filepath.Join(root, "manager-talos.json") + `
engines:
  - alias: here
    type: compute
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-compute-local@v0.1.0"
    manager: local
  - alias: ci-state
    type: state
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-state-git@v0.1.0"
    manager: local
    spec:
      path: ` + statePath + `
  - alias: all-pass
    type: promotion
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-promotion-all@v0.1.0"
    manager: local
  - alias: machine
    type: artifact
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-artifact-talos@v0.1.0"
    manager: talos
    spec:
      clusterName: ` + theTalosCluster + `
      kubernetesVersion: ` + constants.DefaultKubernetesVersion + `
      talosconfigEnv: ` + theTalosconfigVariable + `
      recoveryDirEnv: ` + theRecoveryVariable + `
      patchFile: demo-repo/talos/controlplane.patch.yaml
state: ci-state
targets:
  - alias: build-all
    binary: forge
    args: [test-all]
    in: [demo-repo]
stages:
  - name: build
    promotion: all-pass
    substages:
      - name: default
        engine: here
        targets: [build-all]
`
}

func talosWorkspace(t *testing.T) (root, recovery string) {
	t.Helper()

	root, statePath := bareWorkspace(t, "true")

	talos := filepath.Join(root, "demo-repo", "talos")
	require.NoError(t, os.MkdirAll(talos, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(talos, "controlplane.patch.yaml"),
		[]byte(controlplanePatchDocument), 0o600))

	mustRun(t, filepath.Join(root, "demo-repo"), "git", "add", ".")
	mustRun(t, filepath.Join(root, "demo-repo"), "git", "commit", "-m", "declare a machine config")

	require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci.yaml"),
		[]byte(talosPipelineYAML(root, statePath)), 0o600))

	talosconfig := filepath.Join(root, "talosconfig")
	require.NoError(t, os.WriteFile(talosconfig, []byte(talosconfigDocument), 0o600))
	t.Setenv(theTalosconfigVariable, talosconfig)
	t.Setenv("TALOSCONFIG", "")

	recovery = filepath.Join(t.TempDir(), "recovery")
	t.Setenv(theRecoveryVariable, recovery)

	return root, recovery
}

func TestADeclaredClusterWhoseNodeIsNotInMaintenanceReadsItsBundleThroughTheNodeTheTalosconfigNamesBeforeAnyManagerRuns(t *testing.T) {
	root, recovery := talosWorkspace(t)

	out, err := run(t, root, "forge-ci", "bootstrap", "--config", "forge-ci.yaml", "--root", root)

	require.Error(t, err, out)
	require.Contains(t, out, "reading the bootstrap state of cluster "+theTalosCluster,
		"a node that does not read maintenance sends the engine to the cluster for its bundle")
	require.Contains(t, out, "etcd service of node "+theTalosNode,
		"the engine read the node from the talosconfig context, not from the declaration")
	require.Contains(t, out, "dial tcp "+theTalosEndpoint,
		"every dial went through the endpoint the talosconfig context names")
	require.NotContains(t, out, "realizing machine-config/"+theTalosCluster,
		"no manager runs on a declaration the engine could not finish")
	require.NoDirExists(t, recovery, "nothing but a first run on a node in maintenance writes the recovery directory")
}
