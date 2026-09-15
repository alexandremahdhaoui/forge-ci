package managercontroller

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	KindHelmRelease = "helm-release"

	StatusDeployed = "deployed"
)

var (
	exactChartVersion = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
		`(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

	chartRepositorySchemes = []string{"oci://", "https://"}
)

type HelmRelease struct {
	Namespace       string
	Name            string
	Chart           string
	Version         string
	Repository      string
	Values          map[string]any
	CreateNamespace bool
	Status          string
}

type Helm interface {
	Release(ctx context.Context, namespace, name string) (release HelmRelease, found bool, err error)
	InstallRelease(ctx context.Context, release HelmRelease) error
}

func (r KubernetesRealizer) realizeHelmRelease(res citypes.Resource, opts Options) (Action, error) {
	declared, err := declaredRelease(res.Spec)
	if err != nil {
		return Action{}, err
	}

	id := declared.Namespace + "/" + declared.Name

	apiServer, err := citypes.SpecString(res.Spec, "apiServer")
	if err != nil {
		return Action{}, err
	}

	if err := r.confirmAPIServer(apiServer, "release "+id); err != nil {
		return Action{}, err
	}

	if r.helm == nil {
		return Action{}, fmt.Errorf("reading release %s: this manager carries no helm client yet", id)
	}

	live, found, err := r.helm.Release(r.ctx, declared.Namespace, declared.Name)
	if err != nil {
		return Action{}, fmt.Errorf("reading release %s: %w", id, err)
	}

	if !found {
		return r.installRelease(declared, id, opts)
	}

	return keptRelease(live, declared, id)
}

func (r KubernetesRealizer) confirmAPIServer(declared, subject string) error {
	if declared == "" {
		return fmt.Errorf("reading %s: spec.apiServer is required", subject)
	}

	if r.cluster == nil {
		return fmt.Errorf(
			"reading the api server holding %s: this manager carries no cluster client yet", subject)
	}

	live := r.cluster.APIServer()
	if live == "" {
		return fmt.Errorf(
			"reading the api server holding %s: the cluster client names no api server", subject)
	}

	if live != declared {
		return fmt.Errorf(
			"reading the api server holding %s: spec.apiServer names %q and the live cluster is %q",
			subject, declared, live)
	}

	return nil
}

func (r KubernetesRealizer) installRelease(declared HelmRelease, id string, opts Options) (Action, error) {
	text := "install release " + id + " from chart " + declared.Chart + " " + declared.Version

	if opts.DryRun {
		return Kept(opts.would(text)), nil
	}

	if err := r.helm.InstallRelease(r.ctx, declared); err != nil {
		return Action{}, fmt.Errorf("installing release %s: %w", id, err)
	}

	return Did("installed release " + id + " from chart " + declared.Chart + " " + declared.Version), nil
}

func keptRelease(live, declared HelmRelease, id string) (Action, error) {
	if live.Status != StatusDeployed {
		return Action{}, fmt.Errorf(
			"reading release %s: the cluster holds it in status %q, and only %q is healthy. "+
				"resolve it by hand before reconciling again",
			id, live.Status, StatusDeployed)
	}

	if live.Chart != declared.Chart {
		return Action{}, fmt.Errorf(
			"reading release %s: the cluster holds chart %q and the declaration names chart %q. "+
				"rename the release or remove the one that is there",
			id, live.Chart, declared.Chart)
	}

	if live.Version != declared.Version {
		return Kept(fmt.Sprintf(
			"kept release %s, the cluster holds chart %s %s and the declaration names %s",
			id, live.Chart, live.Version, declared.Version)), nil
	}

	return Kept("kept release " + id), nil
}

func declaredRelease(spec map[string]any) (HelmRelease, error) {
	namespace, err := citypes.SpecString(spec, "namespace")
	if err != nil {
		return HelmRelease{}, err
	}

	name, err := citypes.SpecString(spec, "name")
	if err != nil {
		return HelmRelease{}, err
	}

	chart, err := citypes.SpecString(spec, "chart")
	if err != nil {
		return HelmRelease{}, err
	}

	if namespace == "" || name == "" || chart == "" {
		return HelmRelease{}, fmt.Errorf(
			"reading release %s/%s: spec.namespace, spec.name and spec.chart are required",
			namespace, name)
	}

	id := namespace + "/" + name

	repository, err := declaredRepository(spec, id)
	if err != nil {
		return HelmRelease{}, err
	}

	version, err := declaredVersion(spec, id)
	if err != nil {
		return HelmRelease{}, err
	}

	values, err := citypes.SpecMap(spec, "values")
	if err != nil {
		return HelmRelease{}, err
	}

	createNamespace, err := citypes.SpecBool(spec, "createNamespace")
	if err != nil {
		return HelmRelease{}, err
	}

	return HelmRelease{
		Namespace:       namespace,
		Name:            name,
		Chart:           chart,
		Version:         version,
		Repository:      repository,
		Values:          values,
		CreateNamespace: createNamespace,
	}, nil
}

func declaredRepository(spec map[string]any, id string) (string, error) {
	repository, err := citypes.SpecString(spec, "repository")
	if err != nil {
		return "", err
	}

	if repository == "" {
		return "", fmt.Errorf("reading release %s: spec.repository is required", id)
	}

	for _, scheme := range chartRepositorySchemes {
		if strings.HasPrefix(repository, scheme) {
			return repository, nil
		}
	}

	return "", fmt.Errorf(
		"reading release %s: spec.repository is %q, and a chart repository is reached over %s",
		id, repository, strings.Join(chartRepositorySchemes, " or "))
}

func declaredVersion(spec map[string]any, id string) (string, error) {
	version, err := citypes.SpecString(spec, "version")
	if err != nil {
		return "", err
	}

	if version == "" {
		return "", fmt.Errorf("reading release %s: spec.version is required", id)
	}

	if !exactChartVersion.MatchString(version) {
		return "", fmt.Errorf(
			"reading release %s: spec.version is %q, and one exact chart version written as "+
				"major.minor.patch with an optional leading v is required, such as 2.13.0 or v2.13.0",
			id, version)
	}

	return strings.TrimPrefix(version, "v"), nil
}
