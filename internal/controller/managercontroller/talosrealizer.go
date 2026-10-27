package managercontroller

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configdiff"
	"github.com/siderolabs/talos/pkg/machinery/config/configloader"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	KindMachineConfig    = "machine-config"
	KindClusterBootstrap = "cluster-bootstrap"
	KindSecretBundle     = "secret-bundle"

	talosconfigSource = "the variable spec.talosconfigEnv names"
	recoverySource    = "the variable spec.recoveryDirEnv names"

	recoveryDirMode  os.FileMode = 0o700
	recoveryFileMode os.FileMode = 0o600
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
	SaveTalosconfig(talosconfig, bundle citypes.Secret) (citypes.Secret, error)
	StoredBundle(ctx context.Context, talosconfig citypes.Secret) (citypes.Secret, bool, error)
	StoreBundle(ctx context.Context, talosconfig, bundle citypes.Secret) error
}

type TalosRealizer struct {
	ctx   context.Context
	talos Talos
	fs    fsadapter.FS
}

var _ Realizer = TalosRealizer{}

func NewTalosRealizer(ctx context.Context, talos Talos, files fsadapter.FS) TalosRealizer {
	return TalosRealizer{ctx: ctx, talos: talos, fs: files}
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
	case KindSecretBundle:
		return r.realizeSecretBundle(res, opts)
	default:
		return Action{}, fmt.Errorf(
			"the talos manager cannot realize kind %q, it knows %s, %s and %s",
			res.Kind, KindMachineConfig, KindClusterBootstrap, KindSecretBundle)
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

	bundle, err := bundleFor(res.Spec)
	if err != nil {
		return Action{}, err
	}

	recoveryDirEnv, err := citypes.SpecString(res.Spec, "recoveryDirEnv")
	if err != nil {
		return Action{}, err
	}

	if recoveryDirEnv == "" {
		return Action{}, errors.New(
			"spec.recoveryDirEnv is required: the resource names the variable holding the recovery directory, " +
				"and the engine that declares the resource writes it")
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

	firstRun := running == ""

	var recoveryDir string

	if firstRun {
		if recoveryDir, err = r.recoveryDirHoldingNoBundle(res.Name, recoveryDirEnv); err != nil {
			return Action{}, err
		}
	}

	if opts.DryRun {
		return Kept(opts.would("apply machine config " + res.Name)), nil
	}

	if firstRun {
		if err := r.prepareFirstRun(res.Name, talosconfig, bundle, recoveryDir); err != nil {
			return Action{}, err
		}
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

func (r TalosRealizer) recoveryDirHoldingNoBundle(name, recoveryDirEnv string) (string, error) {
	dir := string(citypes.SecretFromEnv(recoveryDirEnv))
	if dir == "" {
		return "", fmt.Errorf(
			"writing the recovery directory of %s: nothing is set in %s. export it before a first run",
			name, recoverySource)
	}

	held, err := r.fs.Exists(filepath.Join(dir, citypes.TalosRecoveryBundleFile))
	if err != nil {
		return "", fmt.Errorf("reading the recovery directory of %s: %w", name, withoutThePath(err))
	}

	if held {
		return "", fmt.Errorf(
			"writing the recovery directory of %s: the directory %s already holds %s, "+
				"and a first run never overwrites a bundle",
			name, recoverySource, citypes.TalosRecoveryBundleFile)
	}

	return dir, nil
}

func (r TalosRealizer) prepareFirstRun(name string, talosconfig, bundle citypes.Secret, dir string) error {
	if err := r.writeRecovery(dir, citypes.TalosRecoveryBundleFile, bundle); err != nil {
		return fmt.Errorf("writing the secret bundle of %s into the recovery directory: %w", name, err)
	}

	saved, err := r.talos.SaveTalosconfig(talosconfig, bundle)
	if err != nil {
		return fmt.Errorf(
			"saving the admin credential of %s into the client configuration from %s: %w",
			name, talosconfigSource, err)
	}

	if err := r.writeRecovery(dir, citypes.TalosRecoveryTalosconfigFile, saved); err != nil {
		return fmt.Errorf("writing the talosconfig of %s into the recovery directory: %w", name, err)
	}

	return nil
}

func (r TalosRealizer) writeRecovery(dir, file string, content citypes.Secret) error {
	if err := r.fs.MkdirAll(dir); err != nil {
		return withoutThePath(err)
	}

	if err := r.fs.Chmod(dir, recoveryDirMode); err != nil {
		return withoutThePath(err)
	}

	path := filepath.Join(dir, file)

	if err := r.fs.WriteFile(path, []byte(content)); err != nil {
		return withoutThePath(err)
	}

	if err := r.fs.Chmod(path, recoveryFileMode); err != nil {
		return withoutThePath(err)
	}

	return nil
}

func withoutThePath(err error) error {
	var pathError *fs.PathError
	if errors.As(err, &pathError) {
		return pathError.Err
	}

	return err
}

func (r TalosRealizer) realizeSecretBundle(res citypes.Resource, opts Options) (Action, error) {
	bundle, err := bundleFor(res.Spec)
	if err != nil {
		return Action{}, err
	}

	talosconfig, err := talosconfigFor(res.Spec)
	if err != nil {
		return Action{}, err
	}

	if r.talos == nil {
		return Action{}, fmt.Errorf(
			"reading the secret bundle of %s: this manager carries no node client yet", res.Name)
	}

	stored, found, err := r.talos.StoredBundle(r.ctx, talosconfig)
	if err != nil {
		return Action{}, fmt.Errorf(
			"reading the secret bundle of %s using the client configuration from %s: %w",
			res.Name, talosconfigSource, err)
	}

	if found && stored == bundle {
		return Kept("kept secret bundle " + res.Name), nil
	}

	if found {
		return Action{}, fmt.Errorf(
			"reading the secret bundle of %s: the cluster holds a different bundle in secret %s/%s, "+
				"and this manager never overwrites one", res.Name, citypes.TalosBundleNamespace, citypes.TalosBundleSecret)
	}

	if opts.DryRun {
		return Kept(opts.would("store secret bundle " + res.Name)), nil
	}

	if err := r.talos.StoreBundle(r.ctx, talosconfig, bundle); err != nil {
		return Action{}, fmt.Errorf(
			"storing the secret bundle of %s using the client configuration from %s: %w",
			res.Name, talosconfigSource, err)
	}

	return Did("stored secret bundle " + res.Name), nil
}

func bundleFor(spec map[string]any) (citypes.Secret, error) {
	bundle, err := citypes.SpecString(spec, "bundle")
	if err != nil {
		return "", err
	}

	if bundle == "" {
		return "", errors.New(
			"spec.bundle is required: the engine that declares the resource writes the cluster's secret bundle into it")
	}

	return citypes.Secret(bundle), nil
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
