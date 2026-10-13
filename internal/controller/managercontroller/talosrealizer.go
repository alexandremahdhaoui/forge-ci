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
	KindMachineConfig    = "machine-config"
	KindClusterBootstrap = "cluster-bootstrap"

	talosconfigSource = "the variable spec.talosconfigEnv names"
)

var retiredResourceKeys = []struct{ key, because string }{
	{"node", "the node comes from the talosconfig " + talosconfigSource},
	{"talosconfig", "the talosconfig path comes from " + talosconfigSource},
}

type Talos interface {
	MachineConfig(ctx context.Context, talosconfig citypes.Secret) (string, error)
	ApplyMachineConfig(ctx context.Context, talosconfig citypes.Secret, config string) error
	Bootstrap(ctx context.Context, talosconfig citypes.Secret) error
	Bootstrapped(ctx context.Context, talosconfig citypes.Secret) (bool, error)
	WaitNodeRegistered(ctx context.Context, talosconfig citypes.Secret) error
}

type TalosRealizer struct {
	ctx   context.Context
	talos Talos
}

var _ Realizer = TalosRealizer{}

func NewTalosRealizer(ctx context.Context, talos Talos) TalosRealizer {
	return TalosRealizer{ctx: ctx, talos: talos}
}

func (TalosRealizer) Kind() string {
	return "talos"
}

func (r TalosRealizer) Realize(res citypes.Resource, opts Options) (Action, error) {
	if err := refuseRetiredKeys(res); err != nil {
		return Action{}, err
	}

	switch res.Kind {
	case KindMachineConfig:
		return r.realizeMachineConfig(res, opts)
	case KindClusterBootstrap:
		return r.realizeClusterBootstrap(res, opts)
	default:
		return Action{}, fmt.Errorf(
			"the talos manager cannot realize kind %q, it knows %s and %s",
			res.Kind, KindMachineConfig, KindClusterBootstrap)
	}
}

func (r TalosRealizer) realizeMachineConfig(res citypes.Resource, opts Options) (Action, error) {
	declared, err := citypes.SpecString(res.Spec, "config")
	if err != nil {
		return Action{}, err
	}

	if declared == "" {
		return Action{}, errors.New("spec.config is required")
	}

	talosconfig, err := talosconfigFor(res.Spec)
	if err != nil {
		return Action{}, err
	}

	if r.talos == nil {
		return Action{}, fmt.Errorf(
			"reading the machine config of %s: this manager carries no node client yet", res.Name)
	}

	running, err := r.talos.MachineConfig(r.ctx, talosconfig)
	if err != nil {
		return Action{}, fmt.Errorf(
			"reading the machine config of %s using the client configuration from %s: %w",
			res.Name, talosconfigSource, err)
	}

	same, err := sameMachineConfig(running, declared)
	if err != nil {
		return Action{}, fmt.Errorf("comparing the machine config of %s: %w", res.Name, err)
	}

	if same {
		return Kept("kept machine config " + res.Name), nil
	}

	if opts.DryRun {
		return Kept(opts.would("apply machine config " + res.Name)), nil
	}

	if err := r.talos.ApplyMachineConfig(r.ctx, talosconfig, declared); err != nil {
		return Action{}, fmt.Errorf(
			"applying the machine config of %s using the client configuration from %s: %w",
			res.Name, talosconfigSource, err)
	}

	return Did("applied machine config " + res.Name), nil
}

func (r TalosRealizer) realizeClusterBootstrap(res citypes.Resource, opts Options) (Action, error) {
	talosconfig, err := talosconfigFor(res.Spec)
	if err != nil {
		return Action{}, err
	}

	if r.talos == nil {
		return Action{}, fmt.Errorf(
			"reading the bootstrap state of %s: this manager carries no node client yet", res.Name)
	}

	bootstrapped, err := r.talos.Bootstrapped(r.ctx, talosconfig)
	if err != nil {
		return Action{}, fmt.Errorf(
			"reading the bootstrap state of %s using the client configuration from %s: %w",
			res.Name, talosconfigSource, err)
	}

	if bootstrapped {
		if err := r.talos.WaitNodeRegistered(r.ctx, talosconfig); err != nil {
			return Action{}, fmt.Errorf("waiting for the node of %s to register: %w", res.Name, err)
		}

		return Kept("kept cluster bootstrap " + res.Name), nil
	}

	if opts.DryRun {
		return Kept(opts.would("bootstrap cluster " + res.Name)), nil
	}

	if err := r.talos.Bootstrap(r.ctx, talosconfig); err != nil {
		return Action{}, fmt.Errorf(
			"bootstrapping %s using the client configuration from %s: %w", res.Name, talosconfigSource, err)
	}

	if err := r.talos.WaitNodeRegistered(r.ctx, talosconfig); err != nil {
		return Action{}, fmt.Errorf("waiting for the node of %s to register: %w", res.Name, err)
	}

	return Did("bootstrapped cluster " + res.Name), nil
}

func refuseRetiredKeys(res citypes.Resource) error {
	for _, retired := range retiredResourceKeys {
		if _, declared := res.Spec[retired.key]; declared {
			return fmt.Errorf("the talos manager refuses spec.%s on %s %s: %s",
				retired.key, res.Kind, res.Name, retired.because)
		}
	}

	return nil
}

func talosconfigFor(spec map[string]any) (citypes.Secret, error) {
	name, err := citypes.SpecString(spec, "talosconfigEnv")
	if err != nil {
		return "", err
	}

	if name == "" {
		return "", errors.New(
			"spec.talosconfigEnv is required: the resource names the variable holding the talosconfig path, " +
				"and the engine that declares the resource writes it")
	}

	fromEnv := citypes.SecretFromEnv(name)
	if fromEnv == "" {
		return "", fmt.Errorf(
			"reading the client configuration path: nothing is set in %s. export it before bootstrapping",
			talosconfigSource)
	}

	return fromEnv, nil
}

func sameMachineConfig(running, declared string) (bool, error) {
	want, err := loadMachineConfig(declared, "the declared machine config")
	if err != nil {
		return false, err
	}

	if running == "" {
		return false, nil
	}

	have, err := loadMachineConfig(running, "the machine config the node holds")
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
