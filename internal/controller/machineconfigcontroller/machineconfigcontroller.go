package machineconfigcontroller

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const kindMachineConfig = "machine-config"

var (
	ErrNodes = errors.New(
		"this engine needs spec.nodes naming at least one node, each with name, address and configFile")
	ErrNeverPublishes = errors.New(
		"this engine declares machine configs and never publishes: no substage may name it")
)

type Controller struct {
	fs fsadapter.FS
}

func New(fs fsadapter.FS) *Controller {
	return &Controller{fs: fs}
}

func (c *Controller) Declare(in citypes.DeclareInput) (citypes.DeclareOutput, error) {
	nodes, err := declaredNodes(in.Spec)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	resources := make([]citypes.Resource, 0, len(nodes))

	for _, node := range nodes {
		text, err := c.fs.ReadFile(filepath.Join(in.Root, filepath.FromSlash(node.ConfigFile)))
		if err != nil {
			return citypes.DeclareOutput{}, fmt.Errorf(
				"reading the machine config of node %s from %s: %w", node.Name, node.ConfigFile, err)
		}

		resources = append(resources, citypes.Resource{
			Kind: kindMachineConfig,
			Name: node.Name,
			Spec: map[string]any{"node": node.Address, "config": string(text)},
		})
	}

	return citypes.DeclareOutput{Resources: resources}, nil
}

func (c *Controller) Publish() error {
	return ErrNeverPublishes
}

type node struct {
	Name       string
	Address    string
	ConfigFile string
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

	fields := map[string]string{}

	for _, key := range []string{"address", "configFile"} {
		value, err := citypes.SpecString(held, key)
		if err != nil {
			return node{}, fmt.Errorf("reading node %s: %w", name, err)
		}

		if value == "" {
			return node{}, fmt.Errorf("reading node %s: %s is required", name, key)
		}

		fields[key] = value
	}

	return node{Name: name, Address: fields["address"], ConfigFile: fields["configFile"]}, nil
}
