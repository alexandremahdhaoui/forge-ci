package helmadapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/cli"
	"helm.sh/helm/v4/pkg/getter"
	"helm.sh/helm/v4/pkg/kube"
	"helm.sh/helm/v4/pkg/registry"
	"helm.sh/helm/v4/pkg/release"
	repo "helm.sh/helm/v4/pkg/repo/v1"
	"helm.sh/helm/v4/pkg/storage/driver"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	installTimeout = 10 * time.Minute

	StorageSecrets = "secrets"
	StorageMemory  = "memory"

	chartNameKey    = "Name"
	chartVersionKey = "Version"

	releaseInfoField = "Info"

	scratchRepositoryFile      = "repositories.yaml"
	scratchRepositoryCache     = "repository"
	scratchContentCache        = "content"
	scratchRegistryFile        = "registry.json"
	scratchRegistryCredentials = "registry-credentials.json"
	scratchPlugins             = "plugins"
)

var Storages = []string{StorageSecrets, StorageMemory}

func Storage(declared string) (string, error) {
	if declared == "" {
		return StorageSecrets, nil
	}

	if !slices.Contains(Storages, declared) {
		return "", fmt.Errorf(
			"reading spec.storage: it names %q, and helm keeps its release records in %s",
			declared, strings.Join(Storages, " or "))
	}

	return declared, nil
}

type Releases struct {
	kubeconfigPath string
	open           func(namespace string) (*action.Configuration, error)
}

func New(storage, kubeconfigPath string) (Releases, error) {
	if strings.TrimSpace(kubeconfigPath) == "" {
		return Releases{}, errors.New(
			"building the helm client: it was handed no kubeconfig file, " +
				"and the cluster credential is declared. nothing ambient names the cluster")
	}

	return Releases{
		kubeconfigPath: kubeconfigPath,
		open: func(namespace string) (*action.Configuration, error) {
			return openStorage(kubeconfigPath, storage, namespace)
		},
	}, nil
}

func declaredCredential() auth.Client {
	return auth.Client{
		Client: &http.Client{Transport: registry.NewTransport(false)},
		Credential: func(context.Context, string) (auth.Credential, error) {
			return auth.EmptyCredential, nil
		},
	}
}

func declaredRegistry(scratch string) (*registry.Client, error) {
	client, err := registry.NewClient(
		registry.ClientOptCredentialsFile(filepath.Join(scratch, scratchRegistryCredentials)),
		registry.ClientOptAuthorizer(declaredCredential()))
	if err != nil {
		return nil, fmt.Errorf("building the chart registry client: %w", err)
	}

	return client, nil
}

func declaredCluster(kubeconfigPath, namespace string) *cli.EnvSettings {
	settings := cli.New()

	settings.SetNamespace(namespace)
	settings.KubeConfig = kubeconfigPath
	settings.KubeContext = ""
	settings.KubeToken = ""
	settings.KubeAsUser = ""
	settings.KubeAsGroups = nil
	settings.KubeAPIServer = ""
	settings.KubeCaFile = ""
	settings.KubeTLSServerName = ""
	settings.KubeInsecureSkipTLSVerify = false

	return settings
}

func chartFetch(kubeconfigPath, namespace, scratch string) *cli.EnvSettings {
	settings := declaredCluster(kubeconfigPath, namespace)

	settings.RepositoryConfig = filepath.Join(scratch, scratchRepositoryFile)
	settings.RepositoryCache = filepath.Join(scratch, scratchRepositoryCache)
	settings.ContentCache = filepath.Join(scratch, scratchContentCache)
	settings.RegistryConfig = filepath.Join(scratch, scratchRegistryFile)
	settings.PluginsDirectory = filepath.Join(scratch, scratchPlugins)

	return settings
}

func (r Releases) storage(namespace string) (*action.Configuration, error) {
	if r.open == nil {
		return nil, fmt.Errorf(
			"opening helm storage in namespace %s: this helm client was never built by New", namespace)
	}

	return r.open(namespace)
}

func (r Releases) Release(_ context.Context, namespace, name string) (citypes.HelmRelease, bool, error) {
	cfg, err := r.storage(namespace)
	if err != nil {
		return citypes.HelmRelease{}, false, err
	}

	live, err := cfg.Releases.Last(name)
	if errors.Is(err, driver.ErrReleaseNotFound) {
		return citypes.HelmRelease{}, false, nil
	}

	if err != nil {
		return citypes.HelmRelease{}, false,
			fmt.Errorf("reading release %s/%s from helm storage: %w", namespace, name, err)
	}

	described, err := describe(live, namespace, name)
	if err != nil {
		return citypes.HelmRelease{}, false, err
	}

	return described, true, nil
}

func (r Releases) InstallRelease(ctx context.Context, declared citypes.HelmRelease) error {
	cfg, err := r.storage(declared.Namespace)
	if err != nil {
		return err
	}

	id := declared.Namespace + "/" + declared.Name

	scratch, err := os.MkdirTemp("", "helm-chart-")
	if err != nil {
		return fmt.Errorf("making the scratch directory chart %s is fetched into for release %s: %w",
			declared.Chart, id, err)
	}

	defer func() { _ = os.RemoveAll(scratch) }()

	chartRegistry, err := declaredRegistry(scratch)
	if err != nil {
		return err
	}

	install := action.NewInstall(cfg)
	install.Namespace = declared.Namespace
	install.ReleaseName = declared.Name
	install.CreateNamespace = declared.CreateNamespace
	install.Version = declared.Version
	install.WaitStrategy = kube.StatusWatcherStrategy
	install.Timeout = installTimeout
	install.SetRegistryClient(chartRegistry)

	settings := chartFetch(r.kubeconfigPath, declared.Namespace, scratch)

	reference, repositoryURL := chartReference(declared)

	if repositoryURL != "" {
		reference, err = indexedChartURL(repositoryURL, declared, settings)
		if err != nil {
			return fmt.Errorf("locating chart %s %s for release %s: %w",
				declared.Chart, declared.Version, id, err)
		}
	}

	path, err := install.LocateChart(reference, settings)
	if err != nil {
		return fmt.Errorf("locating chart %s %s for release %s: %w",
			reference, declared.Version, id, err)
	}

	chrt, err := loader.Load(path)
	if err != nil {
		return fmt.Errorf("loading chart %s %s for release %s: %w",
			reference, declared.Version, id, err)
	}

	if _, err := install.RunWithContext(ctx, chrt, declared.Values); err != nil {
		return fmt.Errorf("installing release %s from chart %s %s: %w",
			id, reference, declared.Version, err)
	}

	return nil
}

func absent(held any) bool {
	value := reflect.ValueOf(held)

	return !value.IsValid() || (value.Kind() == reflect.Pointer && value.IsNil())
}

func fieldAbsent(held any, field string) (bool, error) {
	value := reflect.ValueOf(held)

	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return true, nil
		}

		value = value.Elem()
	}

	if value.Kind() != reflect.Struct {
		return false, fmt.Errorf("a %T holds no field named %s", held, field)
	}

	carried := value.FieldByName(field)
	if !carried.IsValid() {
		return false, fmt.Errorf("a %T holds no field named %s", held, field)
	}

	return carried.Kind() == reflect.Pointer && carried.IsNil(), nil
}

func indexedChartURL(
	repositoryURL string, declared citypes.HelmRelease, settings *cli.EnvSettings,
) (string, error) {
	repository, err := repo.NewChartRepository(
		&repo.Entry{Name: declared.Chart, URL: repositoryURL}, getter.All(settings))
	if err != nil {
		return "", fmt.Errorf("reading repository %s: %w", repositoryURL, err)
	}

	repository.CachePath = settings.RepositoryCache

	index, err := repository.DownloadIndexFile()
	if err != nil {
		return "", fmt.Errorf("fetching the index of repository %s: %w", repositoryURL, err)
	}

	listed, err := repo.LoadIndexFile(index)
	if err != nil {
		return "", fmt.Errorf("reading the index of repository %s: %w", repositoryURL, err)
	}

	found, err := listed.Get(declared.Chart, declared.Version)
	if err != nil {
		return "", fmt.Errorf("finding chart %s %s in the index of repository %s: %w",
			declared.Chart, declared.Version, repositoryURL, err)
	}

	if len(found.URLs) == 0 {
		return "", fmt.Errorf("finding chart %s %s in the index of repository %s: "+
			"the index lists it with no download url",
			declared.Chart, declared.Version, repositoryURL)
	}

	return repo.ResolveReferenceURL(repositoryURL, found.URLs[0])
}

func chartReference(declared citypes.HelmRelease) (reference, repositoryURL string) {
	if registry.IsOCI(declared.Repository) {
		return strings.TrimSuffix(declared.Repository, "/") + "/" + declared.Chart, ""
	}

	return declared.Chart, declared.Repository
}

func openStorage(kubeconfigPath, storage, namespace string) (*action.Configuration, error) {
	settings := declaredCluster(kubeconfigPath, namespace)

	cfg := new(action.Configuration)

	if err := cfg.Init(settings.RESTClientGetter(), namespace, storage); err != nil {
		return nil, fmt.Errorf("opening helm storage in namespace %s: %w", namespace, err)
	}

	return cfg, nil
}

func describe(live release.Releaser, namespace, name string) (citypes.HelmRelease, error) {
	held, err := release.NewAccessor(live)
	if err != nil {
		return citypes.HelmRelease{},
			fmt.Errorf("reading release %s/%s from helm storage: %w", namespace, name, err)
	}

	charter := held.Chart()
	if absent(charter) {
		return citypes.HelmRelease{}, fmt.Errorf(
			"reading the chart of release %s/%s: the stored release carries no chart", namespace, name)
	}

	chrt, err := chart.NewAccessor(charter)
	if err != nil {
		return citypes.HelmRelease{},
			fmt.Errorf("reading the chart of release %s/%s: %w", namespace, name, err)
	}

	metadata := chrt.MetadataAsMap()

	chartName, _ := metadata[chartNameKey].(string)
	if chartName == "" {
		return citypes.HelmRelease{}, fmt.Errorf(
			"reading the chart of release %s/%s: the stored chart carries no name in its metadata",
			namespace, name)
	}

	infoAbsent, err := fieldAbsent(live, releaseInfoField)
	if err != nil {
		return citypes.HelmRelease{}, fmt.Errorf(
			"reading the status of release %s/%s: %w", namespace, name, err)
	}

	if infoAbsent || held.Status() == "" {
		return citypes.HelmRelease{}, fmt.Errorf(
			"reading the status of release %s/%s: the stored release carries no status",
			namespace, name)
	}

	version, _ := metadata[chartVersionKey].(string)
	if version == "" {
		return citypes.HelmRelease{}, fmt.Errorf(
			"reading the chart of release %s/%s: chart %s carries no version in its metadata",
			namespace, name, chartName)
	}

	return citypes.HelmRelease{
		Namespace: held.Namespace(),
		Name:      held.Name(),
		Chart:     chartName,
		Version:   version,
		Status:    held.Status(),
	}, nil
}
