package managercontroller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const KindMachineConfig = "machine-config"

type Talos interface {
	MachineConfig(ctx context.Context, node, talosconfig string) (string, error)
	ApplyMachineConfig(ctx context.Context, node, talosconfig, config string) error
}

type TalosRealizer struct {
	ctx            context.Context
	talos          Talos
	talosconfigEnv string
}

var _ Realizer = TalosRealizer{}

func NewTalosRealizer(ctx context.Context, talos Talos, talosconfigEnv string) TalosRealizer {
	return TalosRealizer{ctx: ctx, talos: talos, talosconfigEnv: talosconfigEnv}
}

func (TalosRealizer) Kind() string {
	return "talos"
}

func (r TalosRealizer) Realize(res citypes.Resource, opts Options) (Action, error) {
	switch res.Kind {
	case KindMachineConfig:
		return r.realizeMachineConfig(res, opts)
	default:
		return Action{}, fmt.Errorf(
			"the talos manager cannot realize kind %q, it knows %s", res.Kind, KindMachineConfig)
	}
}

func (r TalosRealizer) realizeMachineConfig(res citypes.Resource, opts Options) (Action, error) {
	node, _ := res.Spec["node"].(string)
	declared, _ := res.Spec["config"].(string)

	if node == "" || declared == "" {
		return Action{}, errors.New("spec.node and spec.config are required")
	}

	talosconfig, err := r.talosconfigFor(res.Spec)
	if err != nil {
		return Action{}, err
	}

	if r.talos == nil {
		return Action{}, fmt.Errorf(
			"reading the machine config of node %s: this manager carries no node client yet", node)
	}

	running, err := r.talos.MachineConfig(r.ctx, node, talosconfig)
	if err != nil {
		return Action{}, fmt.Errorf("reading the machine config of node %s: %w", node, err)
	}

	same, err := sameMachineConfig(running, declared)
	if err != nil {
		return Action{}, fmt.Errorf("comparing the machine config of node %s: %w", node, err)
	}

	if same {
		return Kept("kept machine config on node " + node), nil
	}

	if opts.DryRun {
		return Kept(opts.would("apply machine config to node " + node)), nil
	}

	if err := r.talos.ApplyMachineConfig(r.ctx, node, talosconfig, declared); err != nil {
		return Action{}, fmt.Errorf("applying the machine config to node %s: %w", node, err)
	}

	return Did("applied machine config to node " + node), nil
}

func (r TalosRealizer) talosconfigFor(spec map[string]any) (string, error) {
	if path, _ := spec["talosconfig"].(string); path != "" {
		return path, nil
	}

	name, _ := spec["talosconfigEnv"].(string)
	if name == "" {
		name = r.talosconfigEnv
	}

	if name == "" {
		return "", errors.New("spec.talosconfig or spec.talosconfigEnv is required")
	}

	path := os.Getenv(name)
	if path == "" {
		return "", fmt.Errorf("reading %s for the client configuration path: the variable is empty", name)
	}

	return path, nil
}

func sameMachineConfig(running, declared string) (bool, error) {
	have, err := machineConfigDocuments(running, "the machine config the node holds")
	if err != nil {
		return false, err
	}

	want, err := machineConfigDocuments(declared, "the declared machine config")
	if err != nil {
		return false, err
	}

	return reflect.DeepEqual(have, want), nil
}

func machineConfigDocuments(text, what string) ([]any, error) {
	out := []any{}

	for i, raw := range splitDocuments(text) {
		if strings.TrimSpace(raw) == "" {
			continue
		}

		var doc any
		if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
			return nil, fmt.Errorf("decoding document %d of %s: %w", i+1, what, err)
		}

		out = append(out, doc)
	}

	return out, nil
}

func splitDocuments(text string) []string {
	out := []string{}
	current := []string{}

	for _, line := range strings.Split(text, "\n") {
		if strings.TrimRight(line, " \t") == "---" {
			out = append(out, strings.Join(current, "\n"))
			current = nil

			continue
		}

		current = append(current, line)
	}

	return append(out, strings.Join(current, "\n"))
}
