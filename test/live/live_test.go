//go:build live

package live_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	envRoot       = "FORGE_CI_LIVE_ROOT"
	envKubeconfig = "KUBECONFIG"

	apiServerKey = "apiServer"
	storageKey   = "storage"
	namespaceKey = "namespace"
	nameKey      = "name"
	chartKey     = "chart"
	versionKey   = "version"
	keysKey      = "keys"

	apiServerSubject = "api server"
	storageSubject   = "helm storage"

	reachTimeout = 3 * time.Second

	cli = "forge-ci"

	theFirstAPIServer  = "https://10.0.0.1:6443"
	theSecondAPIServer = "https://10.0.0.2:6443"
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

	out, err := declaredAt(path)
	require.NoError(t, err)

	return out
}

func declaredAt(path string) (declaration, error) {
	out, err := parsedAt(path)
	if err != nil {
		return declaration{}, err
	}

	if err := atLeastOneClusterResource(out); err != nil {
		return declaration{}, err
	}

	return out, nil
}

func agreesWithEveryEngineBefore(alias, subject, declared, held string) error {
	if held == "" || held == declared {
		return nil
	}

	return fmt.Errorf(
		"engine %s declares %s %q and an engine before it declares %q, "+
			"and one pipeline reads one cluster back", alias, subject, declared, held)
}

func atLeastOneClusterResource(declared declaration) error {
	if len(declared.resources) > 0 {
		return nil
	}

	return fmt.Errorf(
		"the pipeline at %s declares no cluster resource, "+
			"and this stage reads back every resource a pipeline declares", declared.pipeline)
}

func parsedAt(path string) (declaration, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return declaration{}, fmt.Errorf("resolving the pipeline at %s: %w", path, err)
	}

	data, err := os.ReadFile(abs)
	if err != nil {
		return declaration{}, fmt.Errorf("reading the pipeline at %s: %w", abs, err)
	}

	pipeline, err := config.Parse(data)
	if err != nil {
		return declaration{}, fmt.Errorf("reading the pipeline at %s: %w", abs, err)
	}

	root, err := rootHoldingTheRepos(abs)
	if err != nil {
		return declaration{}, err
	}

	out := declaration{pipeline: abs, root: root}
	controller := clusterresourcecontroller.New(fsadapter.New())

	for _, engine := range pipeline.Engines {
		server, err := citypes.SpecString(engine.Spec, apiServerKey)
		if err != nil {
			return declaration{}, fmt.Errorf("reading the spec of engine %s: %w", engine.Alias, err)
		}

		if server == "" {
			continue
		}

		if err := agreesWithEveryEngineBefore(
			engine.Alias, apiServerSubject, server, out.apiServer); err != nil {
			return declaration{}, err
		}

		storage, err := helmStorageOfManager(pipeline, engine.Manager)
		if err != nil {
			return declaration{}, err
		}

		if err := agreesWithEveryEngineBefore(
			engine.Alias, storageSubject, storage, out.storage); err != nil {
			return declaration{}, err
		}

		out.apiServer = server
		out.storage = storage

		declared, err := controller.Declare(
			citypes.DeclareInput{Spec: engine.Spec, Root: out.root})
		if err != nil {
			return declaration{}, fmt.Errorf(
				"declaring the resources of engine %s: %w", engine.Alias, err)
		}

		out.resources = append(out.resources, declared.Resources...)
	}

	return out, nil
}

func rootHoldingTheRepos(pipeline string) (string, error) {
	named := os.Getenv(envRoot)
	if named == "" {
		named = filepath.Dir(pipeline)
	}

	abs, err := filepath.Abs(named)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", named, err)
	}

	return abs, nil
}

func helmStorageOfManager(pipeline config.Pipeline, alias string) (string, error) {
	for _, manager := range pipeline.Managers {
		if manager.Alias != alias {
			continue
		}

		declared, err := citypes.SpecString(manager.Spec, storageKey)
		if err != nil {
			return "", fmt.Errorf("reading the spec of manager %s: %w", alias, err)
		}

		storage, err := helmadapter.Storage(declared)
		if err != nil {
			return "", fmt.Errorf("reading the spec of manager %s: %w", alias, err)
		}

		return storage, nil
	}

	return "", nil
}

func theLiveCluster(t *testing.T, declared declaration) (kubernetesadapter.Cluster, error) {
	t.Helper()

	if os.Getenv(envKubeconfig) == "" {
		t.Skipf("%s is unset, so nothing read the cluster at %s back", envKubeconfig, declared.apiServer)
	}

	cluster, err := kubernetesadapter.New()
	require.NoError(t, err)

	if err := managercontroller.ConfirmAPIServer(
		cluster, declared.apiServer, "the resources of "+declared.pipeline); err != nil {
		return kubernetesadapter.Cluster{}, err
	}

	live := cluster.APIServer()
	if !answers(live) {
		t.Skipf("the api server at %s does not answer, so nothing read the cluster back", live)
	}

	return cluster, nil
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

	cluster, err := theLiveCluster(t, declared)
	require.NoError(t, err)

	releases := theHelmClient(t, declared)

	for _, resource := range declared.resources {
		t.Run(resource.ID(), func(t *testing.T) {
			require.NoError(t, readBack(t.Context(), cluster, releases, resource))
		})
	}
}

func readBack(
	ctx context.Context,
	cluster kubernetesadapter.Cluster,
	releases helmadapter.Releases,
	resource citypes.Resource,
) error {
	switch resource.Kind {
	case managercontroller.KindHelmRelease:
		return readReleaseBack(ctx, releases, resource)
	case managercontroller.KindSecret:
		return readSecretBack(ctx, cluster, resource)
	default:
		return fmt.Errorf(
			"resource %s is a kind this stage cannot read back, and it reads %s and %s. "+
				"A kind the pipeline declares needs an arm here",
			resource.ID(), managercontroller.KindHelmRelease, managercontroller.KindSecret)
	}
}

func readReleaseBack(
	ctx context.Context,
	releases helmadapter.Releases,
	resource citypes.Resource,
) error {
	namespace, err := declaredValue(resource, namespaceKey)
	if err != nil {
		return err
	}

	name, err := declaredValue(resource, nameKey)
	if err != nil {
		return err
	}

	chart, err := declaredValue(resource, chartKey)
	if err != nil {
		return err
	}

	declaredVersion, err := declaredValue(resource, versionKey)
	if err != nil {
		return err
	}

	version := strings.TrimPrefix(declaredVersion, "v")

	live, found, err := releases.Release(ctx, namespace, name)
	if err != nil {
		return fmt.Errorf("reading release %s/%s: %w", namespace, name, err)
	}

	if !found {
		return fmt.Errorf("helm storage in namespace %s holds no release %s", namespace, name)
	}

	var drift []string

	if live.Status != managercontroller.StatusDeployed {
		drift = append(drift, fmt.Sprintf("is in status %q", live.Status))
	}

	if live.Chart != chart {
		drift = append(drift,
			fmt.Sprintf("holds chart %q and the pipeline declares %q", live.Chart, chart))
	}

	if live.Version != version {
		drift = append(drift,
			fmt.Sprintf("holds chart version %q and the pipeline declares %q", live.Version, version))
	}

	if len(drift) == 0 {
		return nil
	}

	return fmt.Errorf("release %s/%s %s", namespace, name, strings.Join(drift, ", and "))
}

func readSecretBack(
	ctx context.Context,
	cluster kubernetesadapter.Cluster,
	resource citypes.Resource,
) error {
	namespace, err := declaredValue(resource, namespaceKey)
	if err != nil {
		return err
	}

	name, err := declaredValue(resource, nameKey)
	if err != nil {
		return err
	}

	keys, err := citypes.SpecStringSlice(resource.Spec, keysKey)
	if err != nil {
		return fmt.Errorf("reading the keys of secret %s/%s: %w", namespace, name, err)
	}

	live, found, err := cluster.Secret(ctx, namespace, name)
	if err != nil {
		return fmt.Errorf("reading secret %s/%s: %w", namespace, name, err)
	}

	if !found || live == nil {
		return fmt.Errorf("the cluster holds no secret %s/%s", namespace, name)
	}

	var empty []string

	for _, key := range keys {
		if !holdsKey(live, key) {
			empty = append(empty, key)
		}
	}

	if len(empty) == 0 {
		return nil
	}

	return fmt.Errorf("secret %s/%s holds no value under %s",
		namespace, name, strings.Join(empty, " and "))
}

func holdsKey(secret *corev1.Secret, key string) bool {
	return len(bytes.TrimSpace(secret.Data[key])) > 0
}

func declaredValue(resource citypes.Resource, key string) (string, error) {
	value, err := citypes.SpecString(resource.Spec, key)
	if err != nil {
		return "", fmt.Errorf("reading %s of resource %s: %w", key, resource.ID(), err)
	}

	return value, nil
}

func TestForgeCIsOwnRunRecordSaysASecondApplyKeptEveryResourceAndThisReadsNoClusterObject(t *testing.T) {
	declared := declaredByThePipeline(t)

	_, err := theLiveCluster(t, declared)
	require.NoError(t, err)

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

	server := secretServer(t, namespace, name, map[string][]byte{key: []byte("a value")})

	root := t.TempDir()
	writeKubeconfig(t, root, server.URL)

	pipeline := filepath.Join(root, "forge-ci.yaml")
	require.NoError(t, os.WriteFile(pipeline,
		[]byte(secretPipelineYAML(root, server.URL, namespace, name, []string{key})), 0o600))

	declared, err := declaredAt(pipeline)
	require.NoError(t, err)
	require.Len(t, declared.resources, 1)

	cluster, err := theLiveCluster(t, declared)
	require.NoError(t, err)

	require.NoError(t, readBack(t.Context(), cluster, helmadapter.Releases{}, declared.resources[0]))
}

func secretServer(t *testing.T, namespace, name string, data map[string][]byte) *httptest.Server {
	t.Helper()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/"+namespace+"/secrets/"+name {
			w.WriteHeader(http.StatusNotFound)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
			Data:       data,
		})
	}))
	t.Cleanup(server.Close)

	return server
}

func TestThePipelineNamingAnAPIServerTheLiveClusterIsNotIsRefusedBeforeAnyResourceIsReadBack(t *testing.T) {
	const (
		namespace = "a-namespace"
		name      = "a-secret"
		key       = "a-key"
	)

	server := secretServer(t, namespace, name, map[string][]byte{key: []byte("a value")})

	root := t.TempDir()
	writeKubeconfig(t, root, server.URL)

	pipeline := filepath.Join(root, "forge-ci.yaml")
	require.NoError(t, os.WriteFile(pipeline,
		[]byte(secretPipelineYAML(root, theFirstAPIServer, namespace, name, []string{key})), 0o600))

	declared, err := declaredAt(pipeline)
	require.NoError(t, err)

	_, err = theLiveCluster(t, declared)
	require.Error(t, err)
	require.Equal(t,
		"reading the api server holding the resources of "+pipeline+
			`: spec.apiServer names "`+theFirstAPIServer+`" and the live cluster is "`+server.URL+`"`,
		err.Error())
}

func TestTheSecretArmRefusesADeclaredKeyTheLiveSecretHoldsEmptyAndOneItHoldsBlank(t *testing.T) {
	const (
		namespace = "a-namespace"
		name      = "a-secret"
		full      = "a-full-key"
		empty     = "an-empty-key"
		blank     = "a-blank-key"
	)

	server := secretServer(t, namespace, name, map[string][]byte{
		full:  []byte("a value"),
		empty: {},
		blank: []byte(" \n\t "),
	})

	root := t.TempDir()
	writeKubeconfig(t, root, server.URL)

	pipeline := filepath.Join(root, "forge-ci.yaml")
	require.NoError(t, os.WriteFile(pipeline,
		[]byte(secretPipelineYAML(root, server.URL, namespace, name,
			[]string{full, empty, blank})), 0o600))

	declared, err := declaredAt(pipeline)
	require.NoError(t, err)
	require.Len(t, declared.resources, 1)

	cluster, err := theLiveCluster(t, declared)
	require.NoError(t, err)

	require.EqualError(t,
		readBack(t.Context(), cluster, helmadapter.Releases{}, declared.resources[0]),
		"secret "+namespace+"/"+name+" holds no value under "+empty+" and "+blank)
}

func TestTheReleaseArmRefusesAReleaseHelmStorageDoesNotHold(t *testing.T) {
	releases, err := helmadapter.New(helmadapter.StorageMemory)
	require.NoError(t, err)

	err = readBack(t.Context(), kubernetesadapter.Cluster{}, releases, citypes.Resource{
		Kind: managercontroller.KindHelmRelease,
		Name: "a-namespace/a-release",
		Spec: map[string]any{
			namespaceKey: "a-namespace",
			nameKey:      "a-release",
			chartKey:     "a-chart",
			versionKey:   "v1.2.3",
		},
	})
	require.EqualError(t, err, "helm storage in namespace a-namespace holds no release a-release")
}

func TestAKindTheStageHasNoArmForIsRefusedByItsIDRatherThanReadBackWrong(t *testing.T) {
	err := readBack(t.Context(), kubernetesadapter.Cluster{}, helmadapter.Releases{},
		citypes.Resource{Kind: "config-map", Name: "a-namespace/a-map"})
	require.EqualError(t, err,
		"resource config-map/a-namespace/a-map is a kind this stage cannot read back, "+
			"and it reads helm-release and secret. "+
			"A kind the pipeline declares needs an arm here")
}

func TestTheRootOfTheLiveStageDefaultsToThePipelineFilesParentTheWayTheCLIDoes(t *testing.T) {
	t.Setenv(envRoot, "")

	root := t.TempDir()

	pipeline := filepath.Join(root, "forge-ci.yaml")
	require.NoError(t, os.WriteFile(pipeline,
		[]byte(secretPipelineYAML(root, theFirstAPIServer, "a-namespace", "a-secret",
			[]string{"a-key"})), 0o600))

	declared, err := declaredAt(pipeline)
	require.NoError(t, err)
	require.Equal(t, root, declared.root)
}

func TestTheRootOfTheLiveStageIsTheOneTheEnvironmentNamesWhenItNamesOne(t *testing.T) {
	root := t.TempDir()
	elsewhere := t.TempDir()

	t.Setenv(envRoot, elsewhere)

	pipeline := filepath.Join(root, "forge-ci.yaml")
	require.NoError(t, os.WriteFile(pipeline,
		[]byte(secretPipelineYAML(root, theFirstAPIServer, "a-namespace", "a-secret",
			[]string{"a-key"})), 0o600))

	declared, err := declaredAt(pipeline)
	require.NoError(t, err)
	require.Equal(t, elsewhere, declared.root)
}

func TestAPipelineFileWhoseManagerNamesAStorageHelmDoesNotKeepIsRefusedByTheFunctionTheStageReadsItThrough(t *testing.T) {
	root := t.TempDir()

	path := filepath.Join(root, "forge-ci.yaml")
	require.NoError(t, os.WriteFile(path, []byte(twoClusterEnginesPipelineYAML(
		root, theFirstAPIServer, theFirstAPIServer,
		"a-filing-cabinet", helmadapter.StorageSecrets)), 0o600))

	_, err := declaredAt(path)
	require.Error(t, err)
	require.Equal(t,
		`reading the spec of manager cluster-first: reading spec.storage: `+
			`it names "a-filing-cabinet", and helm keeps its release records in `+
			strings.Join(helmadapter.Storages, " or "),
		err.Error())
}

func TestAPipelineFileDeclaringNoClusterResourceIsRefusedByTheFunctionTheStageReadsItThrough(t *testing.T) {
	root := t.TempDir()

	path := filepath.Join(root, "forge-ci.yaml")
	require.NoError(t, os.WriteFile(path, []byte(clusterlessPipelineYAML(root)), 0o600))

	_, err := declaredAt(path)
	require.Error(t, err)
	require.Equal(t,
		"the pipeline at "+path+" declares no cluster resource, "+
			"and this stage reads back every resource a pipeline declares", err.Error())
}

func TestAPipelineFileWhoseTwoManagersDeclareDifferentHelmStorageIsRefusedByTheFunctionTheStageReadsItThrough(t *testing.T) {
	root := t.TempDir()

	path := filepath.Join(root, "forge-ci.yaml")
	require.NoError(t, os.WriteFile(path, []byte(twoClusterEnginesPipelineYAML(
		root, theFirstAPIServer, theFirstAPIServer,
		helmadapter.StorageSecrets, helmadapter.StorageMemory)), 0o600))

	_, err := declaredAt(path)
	require.Error(t, err)
	require.Equal(t,
		`engine second declares helm storage "memory" and an engine before it declares "secrets", `+
			"and one pipeline reads one cluster back", err.Error())
}

func TestAPipelineFileWhoseTwoEnginesDeclareDifferentAPIServersIsRefusedByTheFunctionTheStageReadsItThrough(t *testing.T) {
	root := t.TempDir()

	path := filepath.Join(root, "forge-ci.yaml")
	require.NoError(t, os.WriteFile(path, []byte(twoClusterEnginesPipelineYAML(
		root, theFirstAPIServer, theSecondAPIServer,
		helmadapter.StorageSecrets, helmadapter.StorageSecrets)), 0o600))

	_, err := declaredAt(path)
	require.Error(t, err)
	require.Equal(t,
		`engine second declares api server "`+theSecondAPIServer+`" and an engine before it `+
			`declares "`+theFirstAPIServer+`", and one pipeline reads one cluster back`,
		err.Error())
}

func TestTheFirstEngineAgreesWithEveryEngineBeforeItBecauseThereIsNone(t *testing.T) {
	require.NoError(t, agreesWithEveryEngineBefore("first", storageSubject, "memory", ""))
	require.NoError(t, agreesWithEveryEngineBefore("second", storageSubject, "memory", "memory"))
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

func clusterlessPipelineYAML(root string) string {
	return `name: live
managers:
  - alias: here
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-local@v0.1.0"
engines:
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

func twoClusterEnginesPipelineYAML(root, firstServer, secondServer, firstStorage, secondStorage string) string {
	return `name: live
managers:
  - alias: cluster-first
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-kubernetes@v0.1.0"
    spec:
      storage: ` + firstStorage + `
  - alias: cluster-second
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-kubernetes@v0.1.0"
    spec:
      storage: ` + secondStorage + `
  - alias: here
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-local@v0.1.0"
engines:
  - alias: first
    type: artifact
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-artifact-kubernetes@v0.1.0"
    manager: cluster-first
    spec:
      apiServer: ` + firstServer + `
      resources:
        - kind: secret
          namespace: a-namespace
          name: first-secret
          keys: [a-key]
  - alias: second
    type: artifact
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-artifact-kubernetes@v0.1.0"
    manager: cluster-second
    spec:
      apiServer: ` + secondServer + `
      resources:
        - kind: secret
          namespace: a-namespace
          name: second-secret
          keys: [a-key]
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

func secretPipelineYAML(root, server, namespace, name string, keys []string) string {
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
          keys: [` + strings.Join(keys, ", ") + `]
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
