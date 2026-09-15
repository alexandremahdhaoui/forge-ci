//go:build live

package live_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/helmadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/kubernetesadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/clusterresourcecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
)

const (
	envPipeline   = "FORGE_CI_LIVE_CONFIG"
	envKubeconfig = "KUBECONFIG"

	apiServerKey = "apiServer"
	storageKey   = "storage"
	namespaceKey = "namespace"
	nameKey      = "name"
	chartKey     = "chart"
	versionKey   = "version"
	keysKey      = "keys"

	reachTimeout = 3 * time.Second

	cli = "forge-ci"
)

type declaration struct {
	pipeline  string
	root      string
	apiServer string
	storage   string
	resources []citypes.Resource
}

func declaredByThePipeline(t *testing.T) declaration {
	t.Helper()

	path := os.Getenv(envPipeline)
	if path == "" {
		t.Skipf("%s names no pipeline file, so this stage read no cluster back", envPipeline)
	}

	return declaredAt(t, path)
}

func declaredAt(t *testing.T, path string) declaration {
	t.Helper()

	abs, err := filepath.Abs(path)
	require.NoError(t, err)

	data, err := os.ReadFile(abs)
	require.NoError(t, err, "reading the pipeline at %s", abs)

	pipeline, err := config.Parse(data)
	require.NoError(t, err, "reading the pipeline at %s", abs)

	out := declaration{pipeline: abs, root: filepath.Dir(abs)}
	controller := clusterresourcecontroller.New(fsadapter.New())

	for _, engine := range pipeline.Engines {
		server, err := citypes.SpecString(engine.Spec, apiServerKey)
		require.NoError(t, err, "reading the spec of engine %s", engine.Alias)

		if server == "" {
			continue
		}

		if out.apiServer != "" && out.apiServer != server {
			t.Fatalf(
				"engine %s declares api server %q and an engine before it declares %q, "+
					"and one pipeline reads one cluster back",
				engine.Alias, server, out.apiServer)
		}

		out.apiServer = server
		out.storage = helmStorageOfManager(t, pipeline, engine.Manager)

		declared, err := controller.Declare(
			citypes.DeclareInput{Spec: engine.Spec, Root: out.root})
		require.NoError(t, err, "declaring the resources of engine %s", engine.Alias)

		out.resources = append(out.resources, declared.Resources...)
	}

	if len(out.resources) == 0 {
		t.Skipf("the pipeline at %s declares no cluster resource, so this stage read nothing back", abs)
	}

	return out
}

func helmStorageOfManager(t *testing.T, pipeline config.Pipeline, alias string) string {
	t.Helper()

	for _, manager := range pipeline.Managers {
		if manager.Alias != alias {
			continue
		}

		storage, err := citypes.SpecString(manager.Spec, storageKey)
		require.NoError(t, err, "reading the spec of manager %s", alias)

		if storage == "" {
			return helmadapter.StorageSecrets
		}

		return storage
	}

	t.Fatalf("an engine names manager %q and the pipeline declares no manager of that alias", alias)

	return ""
}

func theLiveCluster(t *testing.T, declared declaration) kubernetesadapter.Cluster {
	t.Helper()

	if os.Getenv(envKubeconfig) == "" {
		t.Skipf("%s is unset, so nothing read the cluster at %s back", envKubeconfig, declared.apiServer)
	}

	cluster, err := kubernetesadapter.New()
	require.NoError(t, err)

	live := cluster.APIServer()
	if live == "" {
		t.Fatalf(
			"reading the api server holding the resources of %s: the cluster client names no api server",
			declared.pipeline)
	}

	if live != declared.apiServer {
		t.Fatalf(
			"reading the api server holding the resources of %s: the pipeline names %q "+
				"and the live cluster is %q",
			declared.pipeline, declared.apiServer, live)
	}

	if !answers(live) {
		t.Skipf("the api server at %s does not answer, so nothing read the cluster back", live)
	}

	return cluster
}

func answers(server string) bool {
	parsed, err := url.Parse(server)
	if err != nil {
		return false
	}

	address := parsed.Host
	if parsed.Port() == "" {
		address = net.JoinHostPort(parsed.Hostname(), "443")
	}

	conn, err := net.DialTimeout("tcp", address, reachTimeout)
	if err != nil {
		return false
	}

	_ = conn.Close()

	return true
}

func theHelmClient(t *testing.T, declared declaration) helmadapter.Releases {
	t.Helper()

	for _, resource := range declared.resources {
		if resource.Kind != managercontroller.KindHelmRelease {
			continue
		}

		releases, err := helmadapter.New(declared.storage)
		require.NoError(t, err)

		return releases
	}

	return helmadapter.Releases{}
}

func TestEveryResourceThePipelineDeclaresIsRealAndHealthyInTheLiveCluster(t *testing.T) {
	declared := declaredByThePipeline(t)
	cluster := theLiveCluster(t, declared)
	releases := theHelmClient(t, declared)

	for _, resource := range declared.resources {
		t.Run(resource.ID(), func(t *testing.T) {
			readBack(t, cluster, releases, resource)
		})
	}
}

func readBack(
	t *testing.T,
	cluster kubernetesadapter.Cluster,
	releases helmadapter.Releases,
	resource citypes.Resource,
) {
	t.Helper()

	switch resource.Kind {
	case managercontroller.KindHelmRelease:
		readReleaseBack(t, releases, resource)
	case managercontroller.KindSecret:
		readSecretBack(t, cluster, resource)
	default:
		t.Fatalf(
			"resource %s is a kind this stage cannot read back, and it reads %s and %s. "+
				"A kind the pipeline declares needs an arm here",
			resource.ID(), managercontroller.KindHelmRelease, managercontroller.KindSecret)
	}
}

func readReleaseBack(t *testing.T, releases helmadapter.Releases, resource citypes.Resource) {
	t.Helper()

	namespace := declaredString(t, resource, namespaceKey)
	name := declaredString(t, resource, nameKey)
	chart := declaredString(t, resource, chartKey)
	version := strings.TrimPrefix(declaredString(t, resource, versionKey), "v")

	live, found, err := releases.Release(t.Context(), namespace, name)
	require.NoError(t, err)
	require.True(t, found, "helm storage in namespace %s holds no release %s", namespace, name)

	assert.Equal(t, managercontroller.StatusDeployed, live.Status,
		"release %s/%s is in status %q", namespace, name, live.Status)
	assert.Equal(t, chart, live.Chart, "release %s/%s holds another chart", namespace, name)
	assert.Equal(t, version, live.Version,
		"release %s/%s holds another chart version", namespace, name)
}

func readSecretBack(t *testing.T, cluster kubernetesadapter.Cluster, resource citypes.Resource) {
	t.Helper()

	namespace := declaredString(t, resource, namespaceKey)
	name := declaredString(t, resource, nameKey)

	keys, err := citypes.SpecStringSlice(resource.Spec, keysKey)
	require.NoError(t, err, "reading the keys of secret %s/%s", namespace, name)
	require.NotEmpty(t, keys, "secret %s/%s declares no key", namespace, name)

	live, found, err := cluster.Secret(t.Context(), namespace, name)
	require.NoError(t, err)
	require.True(t, found, "the cluster holds no secret %s/%s", namespace, name)
	require.NotNil(t, live)

	for _, key := range keys {
		assert.True(t, holdsKey(live, key), "secret %s/%s holds no %s", namespace, name, key)
	}
}

func holdsKey(secret *corev1.Secret, key string) bool {
	if _, held := secret.Data[key]; held {
		return true
	}

	_, held := secret.StringData[key]

	return held
}

func declaredString(t *testing.T, resource citypes.Resource, key string) string {
	t.Helper()

	value, err := citypes.SpecString(resource.Spec, key)
	require.NoError(t, err, "reading %s of resource %s", key, resource.ID())
	require.NotEmpty(t, value, "resource %s declares no %s", resource.ID(), key)

	return value
}

func TestForgeCIsOwnRunRecordSaysASecondApplyKeptEveryResourceAndThisReadsNoClusterObject(t *testing.T) {
	declared := declaredByThePipeline(t)
	theLiveCluster(t, declared)

	binary, err := exec.LookPath(cli)
	if err != nil {
		t.Skipf("%s is not on PATH, so no run record was read", cli)
	}

	command := exec.CommandContext(t.Context(), binary,
		"apply", "--dry-run", "--config", declared.pipeline, "--root", declared.root)
	command.Dir = declared.root
	command.Env = append(os.Environ(), "FORGE_CI_IN_APPLY=")

	out, err := command.CombinedOutput()
	record := string(out)
	require.NoError(t, err, record)

	for _, resource := range declared.resources {
		assertTheRecordKept(t, record, resource)
	}
}

func assertTheRecordKept(t *testing.T, record string, resource citypes.Resource) {
	t.Helper()

	for _, line := range strings.Split(record, "\n") {
		if strings.Contains(line, resource.Name) && strings.Contains(line, "kept") {
			return
		}
	}

	t.Fatalf("no line of the run record says the apply kept %s:\n%s", resource.ID(), record)
}

func TestTheSecretArmReadsADeclaredKeyBackFromAnAPIServerStoodUpInThisTestProcess(t *testing.T) {
	const (
		namespace = "a-namespace"
		name      = "a-secret"
		key       = "a-key"
	)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/"+namespace+"/secrets/"+name {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
			Data:       map[string][]byte{key: []byte("a value")},
		})
	}))
	t.Cleanup(server.Close)

	root := t.TempDir()
	writeKubeconfig(t, root, server.URL)

	pipeline := filepath.Join(root, "forge-ci.yaml")
	require.NoError(t, os.WriteFile(pipeline,
		[]byte(secretPipelineYAML(root, server.URL, namespace, name, key)), 0o600))

	t.Setenv(envPipeline, pipeline)

	declared := declaredAt(t, pipeline)
	require.Len(t, declared.resources, 1)

	cluster := theLiveCluster(t, declared)
	readBack(t, cluster, helmadapter.Releases{}, declared.resources[0])
}

func writeKubeconfig(t *testing.T, root, server string) {
	t.Helper()

	path := filepath.Join(root, "kubeconfig")
	require.NoError(t, os.WriteFile(path, []byte(`apiVersion: v1
kind: Config
clusters:
  - name: here
    cluster:
      server: `+server+`
      insecure-skip-tls-verify: true
contexts:
  - name: here
    context:
      cluster: here
current-context: here
`), 0o600))

	t.Setenv(envKubeconfig, path)
}

func secretPipelineYAML(root, server, namespace, name, key string) string {
	return `name: live
managers:
  - alias: cluster
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-kubernetes@v0.1.0"
  - alias: here
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-local@v0.1.0"
engines:
  - alias: content
    type: artifact
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-artifact-kubernetes@v0.1.0"
    manager: cluster
    spec:
      apiServer: ` + server + `
      resources:
        - kind: secret
          namespace: ` + namespace + `
          name: ` + name + `
          keys: [` + key + `]
  - alias: ci-state
    type: state
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-state-git@v0.1.0"
    manager: here
    spec:
      path: ` + filepath.Join(root, "state") + `
  - alias: all-pass
    type: promotion
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-promotion-all@v0.1.0"
    manager: here
  - alias: compute
    type: compute
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-compute-local@v0.1.0"
    manager: here
state: ci-state
targets:
  - alias: noop
    binary: "true"
stages:
  - name: build
    promotion: all-pass
    substages:
      - name: default
        engine: compute
        targets: [noop]
`
}
