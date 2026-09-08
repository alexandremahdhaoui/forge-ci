package managercontroller

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configdiff"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"

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
	have, err := loadMachineConfig(running, "the machine config the node holds")
	if err != nil {
		return false, err
	}

	want, err := loadMachineConfig(declared, "the declared machine config")
	if err != nil {
		return false, err
	}

	patches, err := configdiff.Patch(have, want)
	if err != nil {
		return false, fmt.Errorf("patching the machine config the node holds: %w", err)
	}

	return len(patches) == 0, nil
}

func loadMachineConfig(text, what string) (config.Provider, error) {
	provider, err := configloader.NewFromBytes([]byte(text), configloader.WithNoValidation())
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", what, err)
	}

	return provider, nil
}
