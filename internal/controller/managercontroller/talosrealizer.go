package managercontroller

import (
	"context"
	"errors"
	"fmt"

	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configdiff"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	KindMachineConfig = "machine-config"

	defaultTalosconfigVariable = "TALOSCONFIG"
)

type Talos interface {
	MachineConfig(ctx context.Context, node string, talosconfig citypes.Secret) (string, error)
	ApplyMachineConfig(ctx context.Context, node string, talosconfig citypes.Secret, config string) error
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
	node, err := citypes.SpecString(res.Spec, "node")
	if err != nil {
		return Action{}, err
	}

	declared, err := citypes.SpecString(res.Spec, "config")
	if err != nil {
		return Action{}, err
	}

	if node == "" || declared == "" {
		return Action{}, errors.New("spec.node and spec.config are required")
	}

	talosconfig, source, err := r.talosconfigFor(res.Spec)
	if err != nil {
		return Action{}, err
	}

	if r.talos == nil {
		return Action{}, fmt.Errorf(
			"reading the machine config of node %s: this manager carries no node client yet", node)
	}

	running, err := r.talos.MachineConfig(r.ctx, node, talosconfig)
	if err != nil {
		return Action{}, fmt.Errorf(
			"reading the machine config of node %s using the client configuration from %s: %w",
			node, source, err)
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
		return Action{}, fmt.Errorf(
			"applying the machine config to node %s using the client configuration from %s: %w",
			node, source, err)
	}

	return Did("applied machine config to node " + node), nil
}

func (r TalosRealizer) talosconfigFor(spec map[string]any) (citypes.Secret, string, error) {
	declared, err := citypes.SpecString(spec, "talosconfig")
	if err != nil {
		return "", "", err
	}

	if declared != "" {
		return citypes.Secret(declared), "spec.talosconfig", nil
	}

	name, err := citypes.SpecString(spec, "talosconfigEnv")
	if err != nil {
		return "", "", err
	}

	source := "the variable spec.talosconfigEnv names"

	if name == "" {
		name = r.talosconfigEnv
		source = "the variable the manager's talosconfigEnv names"
	}

	if name == "" {
		name = defaultTalosconfigVariable
		source = defaultTalosconfigVariable + ", which is read when neither spec.talosconfigEnv " +
			"nor the manager's talosconfigEnv is declared"
	}

	fromEnv := citypes.SecretFromEnv(name)
	if fromEnv == "" {
		return "", "", fmt.Errorf(
			"reading the client configuration path: nothing is set in %s. export it before bootstrapping",
			source)
	}

	return fromEnv, source, nil
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
	provider, err := configloader.NewFromBytes([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", what, err)
	}

	return provider, nil
}
