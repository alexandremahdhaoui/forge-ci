package managercontroller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/kubeconfigadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	KubeconfigKey = "kubeconfig"

	SourcePath  = "path"
	SourceTalos = "talos"
	SourceKind  = "kind"

	talosconfigEnvKey = "talosconfigEnv"
	clusterKey        = "cluster"

	kubeconfigPath = "spec." + KubeconfigKey
)

var KubeconfigSources = []string{SourcePath, SourceTalos, SourceKind}

type KubeconfigSource interface {
	Kubeconfig(ctx context.Context) ([]byte, error)
}

type TalosKubeconfig interface {
	Kubeconfig(ctx context.Context, talosconfig citypes.Secret) ([]byte, error)
}

type DeclaredKubeconfig struct {
	Source         string
	Path           string
	TalosconfigEnv string
	Cluster        string
}

func (d DeclaredKubeconfig) Names() string {
	switch d.Source {
	case SourcePath:
		return SourcePath + " " + d.Path
	case SourceTalos:
		return SourceTalos + " through " + d.TalosconfigEnv
	case SourceKind:
		return SourceKind + " cluster " + d.Cluster
	default:
		return d.Source
	}
}

func Kubeconfig(spec map[string]any) (DeclaredKubeconfig, error) {
	held, err := citypes.SpecMap(spec, KubeconfigKey)
	if err != nil {
		return DeclaredKubeconfig{}, err
	}

	source, err := theOneKubeconfigSource(held)
	if err != nil {
		return DeclaredKubeconfig{}, err
	}

	switch source {
	case SourcePath:
		return declaredPath(held)
	case SourceTalos:
		return declaredTalos(held)
	default:
		return declaredKind(held)
	}
}

func theOneKubeconfigSource(held map[string]any) (string, error) {
	for _, key := range slices.Sorted(maps.Keys(held)) {
		if !slices.Contains(KubeconfigSources, key) {
			return "", fmt.Errorf(
				"reading spec.kubeconfig: it declares %q, and the cluster credential comes from %s",
				key, eachSource())
		}
	}

	named := make([]string, 0, len(KubeconfigSources))

	for _, source := range KubeconfigSources {
		if _, declared := held[source]; declared {
			named = append(named, source)
		}
	}

	if len(named) == 0 {
		return "", fmt.Errorf(
			"reading spec.kubeconfig: it declares no source, and the cluster credential comes from %s",
			eachSource())
	}

	if len(named) > 1 {
		return "", fmt.Errorf(
			"reading spec.kubeconfig: it declares %s, and exactly one of %s is declared",
			strings.Join(named, " and "), eachSource())
	}

	return named[0], nil
}

func eachSource() string {
	return strings.Join(KubeconfigSources, " or ")
}

func declaredString(block map[string]any, prefix, key string) (string, error) {
	value, declared := block[key]
	if !declared || value == nil {
		return "", nil
	}

	text, isString := value.(string)
	if !isString {
		return "", fmt.Errorf(
			"reading %s.%s: a string is required, the spec holds a %T", prefix, key, value)
	}

	return text, nil
}

func declaredBlock(block map[string]any, prefix, key string) (map[string]any, error) {
	value, declared := block[key]
	if !declared || value == nil {
		return nil, nil
	}

	held, isMap := value.(map[string]any)
	if !isMap {
		return nil, fmt.Errorf(
			"reading %s.%s: a map is required, the spec holds a %T", prefix, key, value)
	}

	return held, nil
}

func declaredPath(held map[string]any) (DeclaredKubeconfig, error) {
	path, err := declaredString(held, kubeconfigPath, SourcePath)
	if err != nil {
		return DeclaredKubeconfig{}, err
	}

	if strings.TrimSpace(path) == "" {
		return DeclaredKubeconfig{}, errors.New(
			"reading spec.kubeconfig.path: it names no file, and a path source names a kubeconfig file")
	}

	return DeclaredKubeconfig{Source: SourcePath, Path: path}, nil
}

func declaredTalos(held map[string]any) (DeclaredKubeconfig, error) {
	block, err := declaredBlock(held, kubeconfigPath, SourceTalos)
	if err != nil {
		return DeclaredKubeconfig{}, err
	}

	for _, key := range slices.Sorted(maps.Keys(block)) {
		if key != talosconfigEnvKey {
			return DeclaredKubeconfig{}, fmt.Errorf(
				"reading spec.kubeconfig.talos: it declares %q, and a talos source declares %s only, "+
					"the node and the endpoints come from the talosconfig that variable names",
				key, talosconfigEnvKey)
		}
	}

	variable, err := declaredString(block, kubeconfigPath+"."+SourceTalos, talosconfigEnvKey)
	if err != nil {
		return DeclaredKubeconfig{}, err
	}

	if strings.TrimSpace(variable) == "" {
		return DeclaredKubeconfig{}, fmt.Errorf(
			"reading spec.kubeconfig.talos: it declares no %s, and a talos source declares %s",
			talosconfigEnvKey, talosconfigEnvKey)
	}

	return DeclaredKubeconfig{Source: SourceTalos, TalosconfigEnv: variable}, nil
}

func declaredKind(held map[string]any) (DeclaredKubeconfig, error) {
	block, err := declaredBlock(held, kubeconfigPath, SourceKind)
	if err != nil {
		return DeclaredKubeconfig{}, err
	}

	cluster, err := declaredString(block, kubeconfigPath+"."+SourceKind, clusterKey)
	if err != nil {
		return DeclaredKubeconfig{}, err
	}

	if strings.TrimSpace(cluster) == "" {
		return DeclaredKubeconfig{}, errors.New(
			"reading spec.kubeconfig.kind: it declares no cluster, and a kind source declares cluster")
	}

	return DeclaredKubeconfig{Source: SourceKind, Cluster: cluster}, nil
}

func NewKubeconfigSource(
	declared DeclaredKubeconfig, talos TalosKubeconfig, runner execadapter.Runner,
) (KubeconfigSource, error) {
	switch declared.Source {
	case SourcePath:
		return kubeconfigadapter.NewFile(declared.Path), nil
	case SourceKind:
		return kubeconfigadapter.NewKind(runner, declared.Cluster), nil
	case SourceTalos:
		return talosKubeconfig{talos: talos, talosconfigEnv: declared.TalosconfigEnv}, nil
	default:
		return nil, fmt.Errorf(
			"building the cluster credential source: the declaration names source %q, "+
				"and the cluster credential comes from %s", declared.Source, eachSource())
	}
}

type talosKubeconfig struct {
	talos          TalosKubeconfig
	talosconfigEnv string
}

func (s talosKubeconfig) Kubeconfig(ctx context.Context) ([]byte, error) {
	talosconfig := citypes.SecretFromEnv(s.talosconfigEnv)
	if talosconfig == "" {
		return nil, fmt.Errorf(
			"reading the client configuration: nothing is set in %s, "+
				"and spec.kubeconfig.talos.talosconfigEnv names it. export it before reconciling",
			s.talosconfigEnv)
	}

	raw, err := s.talos.Kubeconfig(ctx, talosconfig)
	if err != nil {
		return nil, fmt.Errorf("reading the kubeconfig through %s: %w", s.talosconfigEnv, err)
	}

	return raw, nil
}
