//go:build e2e

package e2e_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	theAPIServer = "https://127.0.0.1:1"

	theChartRepository = "https://127.0.0.1:1"

	theReleaseNamespace = "app-system"

	theReleaseName = "app"

	theChartName = "app"

	theChartVersion = "1.18.2"
)

const helmReleaseDocument = `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: ` + theReleaseName + `
  namespace: ` + theReleaseNamespace + `
spec:
  interval: 10m
  chart:
    spec:
      chart: ` + theChartName + `
      version: ` + theChartVersion + `
      sourceRef:
        kind: HelmRepository
        name: ` + theChartName + `
        namespace: ` + theReleaseNamespace + `
`

const helmRepositoryDocument = `apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: ` + theChartName + `
  namespace: ` + theReleaseNamespace + `
spec:
  interval: 5m
  url: ` + theChartRepository + `
`

func clusterPipelineYAML(root, statePath string) string {
	return `name: demo
repos:
  - name: demo-repo
    url: file://` + filepath.Join(root, "demo-repo") + `
managers:
  - alias: local
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-local@v0.1.0"
    spec:
      statePath: ` + filepath.Join(root, "manager-local.json") + `
  - alias: kubernetes
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-kubernetes@v0.1.0"
    spec:
      statePath: ` + filepath.Join(root, "manager-kubernetes.json") + `
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
  - alias: cluster
    type: artifact
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-artifact-kubernetes@v0.1.0"
    manager: kubernetes
    spec:
      apiServer: ` + theAPIServer + `
      resources:
        - kind: helm-release
          helmReleaseFile: demo-repo/platform/app/helmrelease.yaml
          valuesFile: demo-repo/platform/app/values.yaml
          createNamespace: true
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

func clusterWorkspace(t *testing.T) (root string) {
	t.Helper()

	root, statePath := bareWorkspace(t, "true")

	gitops := filepath.Join(root, "demo-repo", "platform", "app")
	require.NoError(t, os.MkdirAll(gitops, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(gitops, "helmrelease.yaml"),
		[]byte(helmReleaseDocument), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(gitops, "helmrepository.yaml"),
		[]byte(helmRepositoryDocument), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(gitops, "values.yaml"),
		[]byte("replicas: 1\n"), 0o600))

	mustRun(t, filepath.Join(root, "demo-repo"), "git", "add", ".")
	mustRun(t, filepath.Join(root, "demo-repo"), "git", "commit", "-m", "declare a release")

	require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci.yaml"),
		[]byte(clusterPipelineYAML(root, statePath)), 0o600))

	kubeconfig := filepath.Join(root, "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
clusters:
  - name: here
    cluster:
      server: `+theAPIServer+`
contexts:
  - name: here
    context:
      cluster: here
current-context: here
`), 0o600))

	t.Setenv("KUBECONFIG", kubeconfig)
	t.Setenv("HELM_DRIVER", "memory")

	return root
}

func TestADeclaredReleaseReachesTheKubernetesManagerAndTheManagerAsksHelmForIt(t *testing.T) {
	root := clusterWorkspace(t)

	out, err := run(t, root, "forge-ci", "bootstrap", "--config", "forge-ci.yaml", "--root", root)

	require.Error(t, err, out)
	require.NotContains(t, out, "this helm client was never built by New",
		"the manager carries a helm client New built, or this case proves nothing")
	require.Contains(t, out,
		"realizing helm-release/"+theReleaseNamespace+"/"+theReleaseName+
			": installing release "+theReleaseNamespace+"/"+theReleaseName,
		"the declaration reached the kubernetes manager as a helm-release it owns")
	require.Contains(t, out, "locating chart "+theChartName+" "+theChartVersion+
		" for release "+theReleaseNamespace+"/"+theReleaseName,
		"the chart name, the chart version and the repository all come from the gitops tree")
}
