package machineconfigcontroller

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/talossecretsadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const kindMachineConfig = "machine-config"

var (
	ErrNodes = errors.New(
		"this engine needs spec.nodes naming at least one node, each with name and address")
	ErrNeverPublishes = errors.New(
		"this engine declares machine configs and never publishes: no substage may name it")
)

type Renderer interface {
	Load(bundle citypes.Secret) (citypes.Secret, error)
	MachineConfig(
		bundle citypes.Secret, cluster talossecretsadapter.Cluster, patch string,
	) (citypes.Secret, error)
}

type Controller struct {
	fs       fsadapter.FS
	renderer Renderer
}

func New(fs fsadapter.FS, renderer Renderer) *Controller {
	return &Controller{fs: fs, renderer: renderer}
}

func (c *Controller) Declare(in citypes.DeclareInput) (citypes.DeclareOutput, error) {
	nodes, err := declaredNodes(in.Spec)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	cluster, err := declaredCluster(in.Spec)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	config, err := c.render(in.Root, in.Spec, cluster)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	resources := make([]citypes.Resource, 0, len(nodes))

	for _, node := range nodes {
		resources = append(resources, citypes.Resource{
			Kind: kindMachineConfig,
			Name: node.Name,
			Spec: map[string]any{"node": node.Address, "config": string(config)},
		})
	}

	return citypes.DeclareOutput{Resources: resources}, nil
}

func (c *Controller) Publish() error {
	return ErrNeverPublishes
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

type node struct {
	Name    string
	Address string
}

func declaredCluster(spec map[string]any) (talossecretsadapter.Cluster, error) {
	fields := map[string]string{}

	for _, key := range []string{"clusterName", "endpoint", "kubernetesVersion"} {
		value, err := requiredString(spec, key)
		if err != nil {
			return talossecretsadapter.Cluster{}, err
		}

		fields[key] = value
	}

	return talossecretsadapter.Cluster{
		Name:              fields["clusterName"],
		Endpoint:          fields["endpoint"],
		KubernetesVersion: fields["kubernetesVersion"],
	}, nil
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

func declaredNodes(spec map[string]any) ([]node, error) {
	raw, ok := spec["nodes"].([]any)
	if !ok || len(raw) == 0 {
		return nil, ErrNodes
	}

	nodes := make([]node, 0, len(raw))

	for i, entry := range raw {
		held, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("reading spec.nodes[%d]: an object is required, the spec holds a %T", i, entry)
		}

		read, err := readNode(i, held)
		if err != nil {
			return nil, err
		}

		nodes = append(nodes, read)
	}

	return nodes, nil
}

func readNode(index int, held map[string]any) (node, error) {
	name, err := citypes.SpecString(held, "name")
	if err != nil {
		return node{}, fmt.Errorf("reading spec.nodes[%d]: %w", index, err)
	}

	if name == "" {
		return node{}, fmt.Errorf("reading spec.nodes[%d]: name is required", index)
	}

	address, err := citypes.SpecString(held, "address")
	if err != nil {
		return node{}, fmt.Errorf("reading node %s: %w", name, err)
	}

	if address == "" {
		return node{}, fmt.Errorf("reading node %s: address is required", name)
	}

	return node{Name: name, Address: address}, nil
}
