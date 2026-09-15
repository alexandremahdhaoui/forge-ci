package clusterresourcecontroller_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/clusterresourcecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/fsadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	theAPIServer   = "https://10.0.0.1:6443"
	ciliumRelease  = "platform/cilium/helmrelease.yaml"
	ciliumValues   = "platform/cilium/values.yaml"
	fluxRelease    = "platform/flux/flux-operator.helmrelease.yaml"
	fluxValues     = "platform/flux/flux-operator.values.yaml"
	brokenValues   = "broken/list.values.yaml"
	demoRelease    = "broken/demo.helmrelease.yaml"
	orphanRelease  = "broken/orphan.helmrelease.yaml"
	urllessRelease = "broken/urlless.helmrelease.yaml"
	kustomization  = "broken/not-a-release.yaml"
	incomplete     = "broken/incomplete.helmrelease.yaml"
)

func onDisk() *clusterresourcecontroller.Controller {
	return clusterresourcecontroller.New(fsadapter.New())
}

func helmRelease(releaseFile, valuesFile string) map[string]any {
	return map[string]any{
		"kind":            "helm-release",
		"helmReleaseFile": releaseFile,
		"valuesFile":      valuesFile,
	}
}

func declaring(resources ...any) citypes.DeclareInput {
	return citypes.DeclareInput{
		Root: "testdata",
		Spec: map[string]any{"apiServer": theAPIServer, "resources": resources},
	}
}

func TestAReleaseTakesItsChartVersionAndRepositoryFromTheDocumentsInGitopsAndNeverFromTheSpec(t *testing.T) {
	t.Parallel()

	out, err := onDisk().Declare(declaring(helmRelease(ciliumRelease, ciliumValues)))

	require.NoError(t, err)
	require.Len(t, out.Resources, 1)

	resource := out.Resources[0]
	assert.Equal(t, "helm-release", resource.Kind)
	assert.Equal(t, "kube-system/cilium", resource.Name)
	assert.Equal(t, "kube-system", resource.Spec["namespace"])
	assert.Equal(t, "cilium", resource.Spec["release"])
	assert.Equal(t, "cilium", resource.Spec["chart"])
	assert.Equal(t, "1.20.1", resource.Spec["version"])
	assert.Equal(t, "https://helm.cilium.io", resource.Spec["repository"])
	assert.Equal(t, false, resource.Spec["createNamespace"])

	values, ok := resource.Spec["values"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, values["kubeProxyReplacement"])
}

func TestAReleaseWhoseRepositoryIsReachedOverOciCarriesThatSchemeThrough(t *testing.T) {
	t.Parallel()

	out, err := onDisk().Declare(declaring(helmRelease(fluxRelease, fluxValues)))

	require.NoError(t, err)
	require.Len(t, out.Resources, 1)
	assert.Equal(t, "flux-system/flux-operator", out.Resources[0].Name)
	assert.Equal(t, "flux-operator", out.Resources[0].Spec["chart"])
	assert.Equal(t, "0.59.0", out.Resources[0].Spec["version"])
	assert.Equal(t, "oci://ghcr.io/controlplaneio-fluxcd/charts", out.Resources[0].Spec["repository"])
}

func TestAValuesFileHoldingAnEmptyMappingDeclaresAnEmptyMapAndNeverANullOne(t *testing.T) {
	t.Parallel()

	out, err := onDisk().Declare(declaring(helmRelease(fluxRelease, fluxValues)))

	require.NoError(t, err)

	values, ok := out.Resources[0].Spec["values"].(map[string]any)
	require.True(t, ok)
	assert.Empty(t, values)
}

func TestCreateNamespaceRidesTheResourceWhenTheEntryDeclaresIt(t *testing.T) {
	t.Parallel()

	entry := helmRelease(fluxRelease, fluxValues)
	entry["createNamespace"] = true

	out, err := onDisk().Declare(declaring(entry))

	require.NoError(t, err)
	assert.Equal(t, true, out.Resources[0].Spec["createNamespace"])
}

func TestASecretDeclaresTheNameOfAnEnvironmentVariablePerKeyAndNeverReadsOne(t *testing.T) {
	t.Parallel()

	out, err := onDisk().Declare(declaring(map[string]any{
		"kind":      "secret",
		"namespace": "flux-system",
		"name":      "deploy-key",
		"data":      map[string]any{"identity": "A_DEPLOY_KEY"},
	}))

	require.NoError(t, err)
	require.Len(t, out.Resources, 1)
	assert.Equal(t, "secret", out.Resources[0].Kind)
	assert.Equal(t, "flux-system/deploy-key", out.Resources[0].Name)
	assert.Equal(t, "flux-system", out.Resources[0].Spec["namespace"])
	assert.Equal(t, "deploy-key", out.Resources[0].Spec["name"])
	assert.Equal(t,
		map[string]any{"identity": "A_DEPLOY_KEY"}, out.Resources[0].Spec["data"])
	assert.False(t, out.Resources[0].BootstrapOnly)
}

func TestTheResourcesComeBackInTheOrderTheSpecListsThem(t *testing.T) {
	t.Parallel()

	out, err := onDisk().Declare(declaring(
		helmRelease(ciliumRelease, ciliumValues),
		map[string]any{
			"kind":      "secret",
			"namespace": "flux-system",
			"name":      "deploy-key",
			"data":      map[string]any{"identity": "A_DEPLOY_KEY"},
		},
		helmRelease(fluxRelease, fluxValues),
	))

	require.NoError(t, err)
	require.Len(t, out.Resources, 3)
	assert.Equal(t, "kube-system/cilium", out.Resources[0].Name)
	assert.Equal(t, "flux-system/deploy-key", out.Resources[1].Name)
	assert.Equal(t, "flux-system/flux-operator", out.Resources[2].Name)
}

func TestTheApiServerSitsOnceOnTheSpecAndRidesEveryDeclaredResource(t *testing.T) {
	t.Parallel()

	out, err := onDisk().Declare(declaring(
		helmRelease(ciliumRelease, ciliumValues),
		map[string]any{
			"kind":      "secret",
			"namespace": "flux-system",
			"name":      "deploy-key",
			"data":      map[string]any{"identity": "A_DEPLOY_KEY"},
		},
	))

	require.NoError(t, err)

	for _, resource := range out.Resources {
		assert.Equal(t, theAPIServer, resource.Spec["apiServer"], resource.Name)
	}
}

func TestEveryMalformedSpecIsRefusedByName(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		spec  map[string]any
		names string
	}{
		{
			name:  "a spec naming no api server is refused by name",
			spec:  map[string]any{"resources": []any{}},
			names: "spec.apiServer",
		},
		{
			name:  "a spec whose api server is not a string is refused by name",
			spec:  map[string]any{"apiServer": 6443},
			names: "spec.apiServer",
		},
		{
			name:  "a spec whose resources are not a list is refused by name",
			spec:  map[string]any{"apiServer": theAPIServer, "resources": "one"},
			names: "spec.resources",
		},
		{
			name:  "a resource entry that is not an object is refused by its index",
			spec:  map[string]any{"apiServer": theAPIServer, "resources": []any{"one"}},
			names: "spec.resources[0]",
		},
		{
			name: "a resource entry naming no kind is refused by its index",
			spec: map[string]any{
				"apiServer": theAPIServer, "resources": []any{map[string]any{}},
			},
			names: "kind is required",
		},
		{
			name: "a resource entry whose kind is not a string is refused by its index",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{map[string]any{"kind": 1}},
			},
			names: "spec.resources[0]",
		},
		{
			name: "a resource entry naming an unknown kind is refused by that kind",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{map[string]any{"kind": "config-map"}},
			},
			names: `kind "config-map" is unknown`,
		},
		{
			name: "a release entry naming no release file is refused by name",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{map[string]any{"kind": "helm-release"}},
			},
			names: "helmReleaseFile is required",
		},
		{
			name: "a release entry naming no values file is refused by name",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{map[string]any{
					"kind": "helm-release", "helmReleaseFile": ciliumRelease,
				}},
			},
			names: "valuesFile is required",
		},
		{
			name: "a release entry whose createNamespace is not a bool is refused by name",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{map[string]any{
					"kind":            "helm-release",
					"helmReleaseFile": ciliumRelease,
					"valuesFile":      ciliumValues,
					"createNamespace": "yes",
				}},
			},
			names: "spec.createNamespace",
		},
		{
			name: "a release file the checkout does not hold is refused by its path",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{helmRelease("platform/absent/helmrelease.yaml", ciliumValues)},
			},
			names: "platform/absent/helmrelease.yaml",
		},
		{
			name: "a document that is not a release is refused by the kind it holds",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{helmRelease(kustomization, ciliumValues)},
			},
			names: `its kind is "Kustomization" and HelmRelease is required`,
		},
		{
			name: "a release document naming no chart version is refused by that key",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{helmRelease(incomplete, ciliumValues)},
			},
			names: "spec.chart.spec.version is required",
		},
		{
			name: "a source reference that resolves to nothing is refused by what it named",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{helmRelease(orphanRelease, ciliumValues)},
			},
			names: "sourceRef names HelmRepository demo/nowhere",
		},
		{
			name: "a repository document naming no url is refused by its file",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{helmRelease(urllessRelease, ciliumValues)},
			},
			names: "urlless.helmrepository.yaml names no spec.url",
		},
		{
			name: "a values file the checkout does not hold is refused by its path",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{helmRelease(ciliumRelease, "platform/cilium/absent.yaml")},
			},
			names: "platform/cilium/absent.yaml",
		},
		{
			name: "a values file holding no mapping is refused by its path",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{helmRelease(demoRelease, brokenValues)},
			},
			names: "the file holds no yaml mapping",
		},
		{
			name: "a secret naming no namespace is refused by name",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{map[string]any{"kind": "secret", "name": "deploy-key"}},
			},
			names: "namespace is required",
		},
		{
			name: "a secret naming no name is refused by name",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{map[string]any{"kind": "secret", "namespace": "flux-system"}},
			},
			names: "name is required",
		},
		{
			name: "a secret naming no data is refused by its id",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{map[string]any{
					"kind": "secret", "namespace": "flux-system", "name": "deploy-key",
				}},
			},
			names: "reading the data of secret flux-system/deploy-key: data is required",
		},
		{
			name: "a secret whose data is not a map of strings is refused by its id",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{map[string]any{
					"kind": "secret", "namespace": "flux-system", "name": "deploy-key",
					"data": map[string]any{"identity": 1},
				}},
			},
			names: "reading the data of secret flux-system/deploy-key",
		},
		{
			name: "a secret key naming no environment variable is refused by that key",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{map[string]any{
					"kind": "secret", "namespace": "flux-system", "name": "deploy-key",
					"data": map[string]any{"identity": ""},
				}},
			},
			names: `key "identity" names no environment variable`,
		},
		{
			name: "a secret data key with no name is refused by its id",
			spec: map[string]any{
				"apiServer": theAPIServer,
				"resources": []any{map[string]any{
					"kind": "secret", "namespace": "flux-system", "name": "deploy-key",
					"data": map[string]any{"": "A_DEPLOY_KEY"},
				}},
			},
			names: "data holds a key with no name",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			out, err := onDisk().Declare(citypes.DeclareInput{Root: "testdata", Spec: tc.spec})

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.names)
			assert.Empty(t, out.Resources)
		})
	}
}

func TestASpecNamingNoResourcesAtAllIsRefusedByName(t *testing.T) {
	t.Parallel()

	_, err := onDisk().Declare(citypes.DeclareInput{
		Root: "testdata",
		Spec: map[string]any{"apiServer": theAPIServer},
	})

	require.ErrorIs(t, err, clusterresourcecontroller.ErrResources)
}

func TestASpecNamingAnEmptyResourceListIsRefusedByName(t *testing.T) {
	t.Parallel()

	_, err := onDisk().Declare(citypes.DeclareInput{
		Root: "testdata",
		Spec: map[string]any{"apiServer": theAPIServer, "resources": []any{}},
	})

	require.ErrorIs(t, err, clusterresourcecontroller.ErrResources)
}

func TestASpecWhoseResourcesAreNotAListNamesTheTypeItFoundAndAnAbsentOneKeepsItsOwnMessage(t *testing.T) {
	t.Parallel()

	_, err := onDisk().Declare(citypes.DeclareInput{
		Root: "testdata",
		Spec: map[string]any{"apiServer": theAPIServer, "resources": "one"},
	})

	require.Error(t, err)
	require.NotErrorIs(t, err, clusterresourcecontroller.ErrResources)
	assert.Equal(t, "reading spec.resources: a list is required, the spec holds a string", err.Error())

	_, err = onDisk().Declare(citypes.DeclareInput{
		Root: "testdata",
		Spec: map[string]any{"apiServer": theAPIServer},
	})

	require.ErrorIs(t, err, clusterresourcecontroller.ErrResources)
}

func TestASecretHoldingTwoUnusableDataKeysRefusesWithTheSameMessageEveryRun(t *testing.T) {
	t.Parallel()

	input := declaring(map[string]any{
		"kind":      "secret",
		"namespace": "flux-system",
		"name":      "deploy-key",
		"data":      map[string]any{"alpha": "", "beta": ""},
	})

	_, first := onDisk().Declare(input)
	require.Error(t, first)

	for range 60 {
		_, again := onDisk().Declare(input)
		require.Error(t, again)
		assert.Equal(t, first.Error(), again.Error())
	}

	assert.Contains(t, first.Error(), `key "alpha" names no environment variable`)
}

func TestARefusalOverAValuesFileNeverCarriesALineOfIt(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(brokenValues)))
	require.NoError(t, err)

	_, err = onDisk().Declare(declaring(helmRelease(demoRelease, brokenValues)))

	require.Error(t, err)

	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if trimmed == "" {
			continue
		}

		assert.NotContains(t, err.Error(), trimmed)
	}
}

func TestADirectoryThatCannotBeListedRefusesTheReleaseByName(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(ciliumRelease)))
	require.NoError(t, err)

	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().
		ReadFile(filepath.Join("testdata", "platform", "cilium", "helmrelease.yaml")).
		Return(raw, nil)
	fs.EXPECT().
		List(filepath.Join("testdata", "platform", "cilium")).
		Return(nil, errors.New("the directory cannot be listed"))

	_, err = clusterresourcecontroller.New(fs).
		Declare(declaring(helmRelease(ciliumRelease, ciliumValues)))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "resolving the chart repository of release kube-system/cilium")
}

func TestADocumentThatCannotBeReadIsPassedOverAndTheNextOneResolvesTheSourceReference(t *testing.T) {
	t.Parallel()

	dir := filepath.Join("testdata", "platform", "cilium")

	release, err := os.ReadFile(filepath.Join(dir, "helmrelease.yaml"))
	require.NoError(t, err)

	repository, err := os.ReadFile(filepath.Join(dir, "helmrepository.yaml"))
	require.NoError(t, err)

	values, err := os.ReadFile(filepath.Join(dir, "values.yaml"))
	require.NoError(t, err)

	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().ReadFile(filepath.Join(dir, "helmrelease.yaml")).Return(release, nil)
	fs.EXPECT().List(dir).Return([]string{"ghost.yaml", "notes.txt", "helmrepository.yaml"}, nil)
	fs.EXPECT().ReadFile(filepath.Join(dir, "ghost.yaml")).Return(nil, errors.New("no such file"))
	fs.EXPECT().ReadFile(filepath.Join(dir, "helmrepository.yaml")).Return(repository, nil)
	fs.EXPECT().ReadFile(filepath.Join(dir, "values.yaml")).Return(values, nil)

	out, err := clusterresourcecontroller.New(fs).
		Declare(declaring(helmRelease(ciliumRelease, ciliumValues)))

	require.NoError(t, err)
	assert.Equal(t, "https://helm.cilium.io", out.Resources[0].Spec["repository"])
}

func TestThePublishToolRefusesByNameAndPublishesNothing(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, onDisk().Publish(), clusterresourcecontroller.ErrNeverPublishes)
}
