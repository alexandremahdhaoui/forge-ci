package machineconfigcontroller

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/talossecretsadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	kindMachineConfig    = "machine-config"
	kindClusterBootstrap = "cluster-bootstrap"

	apiServerPort = "6443"
)

var ErrNeverPublishes = errors.New(
	"this engine declares machine configs and never publishes: no substage may name it")

type Renderer interface {
	Load(bundle citypes.Secret) (citypes.Secret, error)
	MachineConfig(
		bundle citypes.Secret, cluster talossecretsadapter.Cluster, patch string,
	) (citypes.Secret, error)
}

type Talosconfig interface {
	Context(talosconfig citypes.Secret) (citypes.TalosContext, error)
}

type Controller struct {
	fs          fsadapter.FS
	renderer    Renderer
	talosconfig Talosconfig
}

func New(fs fsadapter.FS, renderer Renderer, talosconfig Talosconfig) *Controller {
	return &Controller{fs: fs, renderer: renderer, talosconfig: talosconfig}
}

func (c *Controller) Declare(in citypes.DeclareInput) (citypes.DeclareOutput, error) {
	clusterName, err := requiredString(in.Spec, "clusterName")
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	kubernetesVersion, err := requiredString(in.Spec, "kubernetesVersion")
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	target, err := c.declaredContext(in.Spec, clusterName)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	cluster := talossecretsadapter.Cluster{
		Name:              clusterName,
		Endpoint:          "https://" + net.JoinHostPort(hostOf(target.Endpoints[0]), apiServerPort),
		KubernetesVersion: kubernetesVersion,
	}

	config, err := c.render(in.Root, in.Spec, cluster)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	return citypes.DeclareOutput{Resources: []citypes.Resource{
		{
			Kind: kindMachineConfig,
			Name: clusterName,
			Spec: map[string]any{"node": target.Node, "config": string(config)},
		},
		{
			Kind: kindClusterBootstrap,
			Name: clusterName,
			Spec: map[string]any{"node": target.Node},
		},
	}}, nil
}

func (c *Controller) Publish() error {
	return ErrNeverPublishes
}

func (c *Controller) declaredContext(spec map[string]any, clusterName string) (citypes.TalosContext, error) {
	variable, err := requiredString(spec, "talosconfigEnv")
	if err != nil {
		return citypes.TalosContext{}, err
	}

	talosconfig := citypes.SecretFromEnv(variable)
	if talosconfig == "" {
		return citypes.TalosContext{}, fmt.Errorf(
			"reading the client configuration of cluster %s: nothing is set in %s, "+
				"and spec.talosconfigEnv names it. export it before declaring", clusterName, variable)
	}

	target, err := c.talosconfig.Context(talosconfig)
	if err != nil {
		return citypes.TalosContext{}, fmt.Errorf(
			"reading the client configuration of cluster %s: %w", clusterName, err)
	}

	return target, nil
}

func hostOf(endpoint string) string {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return endpoint
	}

	return host
}

func (c *Controller) render(
	root string, spec map[string]any, cluster talossecretsadapter.Cluster,
) (citypes.Secret, error) {
	bundleFile, err := requiredString(spec, "bundleFile")
	if err != nil {
		return "", err
	}

	patchFile, err := requiredString(spec, "patchFile")
	if err != nil {
		return "", err
	}

	stored, err := c.fs.ReadFile(filepath.Join(root, filepath.FromSlash(bundleFile)))
	if err != nil {
		return "", fmt.Errorf(
			"reading the secret bundle of cluster %s from %s: %w", cluster.Name, bundleFile, err)
	}

	bundle, err := c.renderer.Load(citypes.Secret(stored))
	if err != nil {
		return "", fmt.Errorf("loading the secret bundle of cluster %s: %w", cluster.Name, err)
	}

	patch, err := c.fs.ReadFile(filepath.Join(root, filepath.FromSlash(patchFile)))
	if err != nil {
		return "", fmt.Errorf(
			"reading the patch document of cluster %s from %s: %w", cluster.Name, patchFile, err)
	}

	config, err := c.renderer.MachineConfig(bundle, cluster, string(patch))
	if err != nil {
		return "", fmt.Errorf("rendering the machine config of cluster %s: %w", cluster.Name, err)
	}

	return config, nil
}

func requiredString(spec map[string]any, key string) (string, error) {
	value, err := citypes.SpecString(spec, key)
	if err != nil {
		return "", err
	}

	if value == "" {
		return "", fmt.Errorf("this engine needs spec.%s and the spec does not name it", key)
	}

	return value, nil
}
