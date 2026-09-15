package helmadapter

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"helm.sh/helm/v4/pkg/action"
	"helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/cli"
	"helm.sh/helm/v4/pkg/kube"
	"helm.sh/helm/v4/pkg/registry"
	"helm.sh/helm/v4/pkg/release"
	"helm.sh/helm/v4/pkg/storage/driver"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	installTimeout = 10 * time.Minute

	StorageSecrets = "secrets"
	StorageMemory  = "memory"

	chartNameKey    = "Name"
	chartVersionKey = "Version"

	releaseInfoField = "Info"
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
	settings *cli.EnvSettings
	registry *registry.Client
	open     func(namespace string) (*action.Configuration, error)
}

func New(storage string) (Releases, error) {
	client, err := registry.NewClient()
	if err != nil {
		return Releases{}, fmt.Errorf("building the chart registry client: %w", err)
	}

	settings := cli.New()

	return Releases{
		settings: settings,
		registry: client,
		open: func(namespace string) (*action.Configuration, error) {
			return openStorage(settings, client, storage, namespace)
		},
	}, nil
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

	install := action.NewInstall(cfg)
	install.Namespace = declared.Namespace
	install.ReleaseName = declared.Name
	install.CreateNamespace = declared.CreateNamespace
	install.Version = declared.Version
	install.WaitStrategy = kube.StatusWatcherStrategy
	install.Timeout = installTimeout
	install.SetRegistryClient(r.registry)

	reference, repositoryURL := chartReference(declared)
	install.RepoURL = repositoryURL

	path, err := install.LocateChart(reference, r.settings)
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

func chartReference(declared citypes.HelmRelease) (reference, repositoryURL string) {
	if registry.IsOCI(declared.Repository) {
		return strings.TrimSuffix(declared.Repository, "/") + "/" + declared.Chart, ""
	}

	return declared.Chart, declared.Repository
}

func openStorage(
	settings *cli.EnvSettings, client *registry.Client, storage, namespace string,
) (*action.Configuration, error) {
	cfg := new(action.Configuration)

	if err := cfg.Init(settings.RESTClientGetter(), namespace, storage); err != nil {
		return nil, fmt.Errorf("opening helm storage in namespace %s: %w", namespace, err)
	}

	cfg.RegistryClient = client

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
