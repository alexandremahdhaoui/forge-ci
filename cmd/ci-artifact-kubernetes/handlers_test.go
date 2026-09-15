package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/clusterresourcecontroller"
)

const (
	releaseDocument = `apiVersion: helm.toolkit.fluxcd.io/v2
kind: HelmRelease
metadata:
  name: demo
  namespace: demo-system
spec:
  chart:
    spec:
      chart: demo
      version: 2.4.0
      sourceRef:
        kind: HelmRepository
        name: demo
        namespace: demo-system
`

	repositoryDocument = `apiVersion: source.toolkit.fluxcd.io/v1
kind: HelmRepository
metadata:
  name: demo
  namespace: demo-system
spec:
  url: oci://registry.example.test/charts
`

	valuesDocument = "replicas: 2\n"
)

func writeCheckout(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	dir := filepath.Join(root, "gitops", "demo")
	require.NoError(t, os.MkdirAll(dir, 0o750))

	for name, content := range map[string]string{
		"helmrelease.yaml":    releaseDocument,
		"helmrepository.yaml": repositoryDocument,
		"values.yaml":         valuesDocument,
	} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}

	return root
}

func declaredSpec() Spec {
	return Spec{
		"apiServer": "https://10.0.0.1:6443",
		"resources": []any{
			map[string]any{
				"kind":            "helm-release",
				"helmReleaseFile": "gitops/demo/helmrelease.yaml",
				"valuesFile":      "gitops/demo/values.yaml",
				"createNamespace": true,
			},
			map[string]any{
				"kind":      "secret",
				"namespace": "demo-system",
				"name":      "deploy-key",
				"data":      map[string]any{"identity": "A_DEPLOY_KEY"},
			},
		},
	}
}

func TestTheDeclareToolAnswersTheResourcesInTheOrderTheSpecListsThem(t *testing.T) {
	t.Parallel()

	out, err := NewHandlers().
		Declare(context.Background(), DeclareInput{Root: writeCheckout(t), Spec: declaredSpec()})

	require.NoError(t, err)
	require.Len(t, out.Resources, 2)

	assert.Equal(t, "helm-release", out.Resources[0].Kind)
	assert.Equal(t, "demo-system/demo", out.Resources[0].Name)
	assert.Equal(t, "demo", out.Resources[0].Spec["chart"])
	assert.Equal(t, "2.4.0", out.Resources[0].Spec["version"])
	assert.Equal(t, "oci://registry.example.test/charts", out.Resources[0].Spec["repository"])
	assert.Equal(t, "https://10.0.0.1:6443", out.Resources[0].Spec["apiServer"])
	assert.Equal(t, true, out.Resources[0].Spec["createNamespace"])

	assert.Equal(t, "secret", out.Resources[1].Kind)
	assert.Equal(t, "demo-system/deploy-key", out.Resources[1].Name)
	assert.Equal(t, map[string]any{"identity": "A_DEPLOY_KEY"}, out.Resources[1].Spec["data"])
}

func TestTheDeclareToolAnswersTheControllersRefusalAndNoResources(t *testing.T) {
	t.Parallel()

	out, err := NewHandlers().Declare(context.Background(), DeclareInput{
		Spec: Spec{"apiServer": "https://10.0.0.1:6443"},
	})

	require.ErrorIs(t, err, clusterresourcecontroller.ErrResources)
	assert.Nil(t, out)
}

func TestThePublishToolRefusesByNameAndPublishesNothing(t *testing.T) {
	t.Parallel()

	out, err := NewHandlers().
		Publish(context.Background(), ArtifactInput{Revision: "3dd48e96ed7e", Version: "v0.1.0"})

	require.ErrorIs(t, err, clusterresourcecontroller.ErrNeverPublishes)
	assert.Nil(t, out)
}
