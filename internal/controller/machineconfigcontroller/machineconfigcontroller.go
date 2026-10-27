package machineconfigcontroller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/talossecretsadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	kindMachineConfig    = "machine-config"
	kindClusterBootstrap = "cluster-bootstrap"
	kindSecretBundle     = "secret-bundle"

	apiServerPort = "6443"

	nodeFromTheTalosconfig = "the node and the endpoint come from the talosconfig " +
		"the variable spec.talosconfigEnv names"
)

var (
	ErrNeverPublishes = errors.New(
		"this engine declares machine configs and never publishes: no substage may name it")

	retiredKeys = []struct{ key, because string }{
		{"endpoint", nodeFromTheTalosconfig},
		{"nodes", nodeFromTheTalosconfig},
		{"bundleFile", "the bundle is minted on a node in maintenance and read from the cluster after, " +
			"and no bundle file is ever read"},
	}
)

type Renderer interface {
	Mint() (citypes.Secret, error)
	Load(bundle citypes.Secret) (citypes.Secret, error)
	MachineConfig(
		bundle citypes.Secret, cluster talossecretsadapter.Cluster, patch string,
	) (citypes.Secret, error)
	InstallerImage(rendered citypes.Secret) (string, error)
}

type Registry interface {
	Digest(ctx context.Context, tag string) (string, error)
}

type Talosconfig interface {
	Context(talosconfig citypes.Secret) (citypes.TalosContext, error)
	InMaintenance(ctx context.Context, talosconfig citypes.Secret) (bool, error)
	Bootstrapped(ctx context.Context, talosconfig citypes.Secret) (bool, error)
	StoredBundle(ctx context.Context, talosconfig citypes.Secret) (citypes.Secret, bool, error)
}

type Controller struct {
	fs          fsadapter.FS
	renderer    Renderer
	talosconfig Talosconfig
	registry    Registry
}

func New(fs fsadapter.FS, renderer Renderer, talosconfig Talosconfig, registry Registry) *Controller {
	return &Controller{fs: fs, renderer: renderer, talosconfig: talosconfig, registry: registry}
}

type declaration struct {
	clusterName       string
	kubernetesVersion string
	talosconfigEnv    string
	recoveryDirEnv    string
	patchFile         string
}

func (c *Controller) Declare(ctx context.Context, in citypes.DeclareInput) (citypes.DeclareOutput, error) {
	declared, err := readDeclaration(in.Spec)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	talosconfig, target, err := c.declaredContext(declared)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	cluster := talossecretsadapter.Cluster{
		Name:              declared.clusterName,
		Endpoint:          "https://" + net.JoinHostPort(hostOf(target.Endpoints[0]), apiServerPort),
		KubernetesVersion: declared.kubernetesVersion,
	}

	patch, err := c.fs.ReadFile(filepath.Join(in.Root, filepath.FromSlash(declared.patchFile)))
	if err != nil {
		return citypes.DeclareOutput{}, fmt.Errorf(
			"reading the patch document of cluster %s from %s: %w", cluster.Name, declared.patchFile, err)
	}

	bundle, err := c.bundle(ctx, talosconfig, declared)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	config, err := c.render(bundle, cluster, string(patch))
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	if err := c.refuseAnInstallerImageNotPinnedByDigest(ctx, cluster.Name, config); err != nil {
		return citypes.DeclareOutput{}, err
	}

	return citypes.DeclareOutput{Resources: []citypes.Resource{
		{
			Kind: kindMachineConfig,
			Name: cluster.Name,
			Spec: map[string]any{
				"config":         config,
				"bundle":         bundle,
				"talosconfigEnv": declared.talosconfigEnv,
				"recoveryDirEnv": declared.recoveryDirEnv,
			},
		},
		{
			Kind: kindClusterBootstrap,
			Name: cluster.Name,
			Spec: map[string]any{"talosconfigEnv": declared.talosconfigEnv},
		},
		{
			Kind: kindSecretBundle,
			Name: cluster.Name,
			Spec: map[string]any{"bundle": bundle, "talosconfigEnv": declared.talosconfigEnv},
		},
	}}, nil
}

func (c *Controller) Publish() error {
	return ErrNeverPublishes
}

func readDeclaration(spec map[string]any) (declaration, error) {
	for _, retired := range retiredKeys {
		if _, declared := spec[retired.key]; declared {
			return declaration{}, fmt.Errorf("this engine refuses spec.%s: %s", retired.key, retired.because)
		}
	}

	var (
		declared declaration
		err      error
	)

	for _, field := range []struct {
		key  string
		into *string
	}{
		{"clusterName", &declared.clusterName},
		{"kubernetesVersion", &declared.kubernetesVersion},
		{"talosconfigEnv", &declared.talosconfigEnv},
		{"recoveryDirEnv", &declared.recoveryDirEnv},
		{"patchFile", &declared.patchFile},
	} {
		if *field.into, err = requiredString(spec, field.key); err != nil {
			return declaration{}, err
		}
	}

	return declared, nil
}

func (c *Controller) declaredContext(declared declaration) (citypes.Secret, citypes.TalosContext, error) {
	talosconfig := citypes.SecretFromEnv(declared.talosconfigEnv)
	if talosconfig == "" {
		return "", citypes.TalosContext{}, fmt.Errorf(
			"reading the client configuration of cluster %s: nothing is set in %s, "+
				"and spec.talosconfigEnv names it. export it before declaring",
			declared.clusterName, declared.talosconfigEnv)
	}

	target, err := c.talosconfig.Context(talosconfig)
	if err != nil {
		return "", citypes.TalosContext{}, fmt.Errorf(
			"reading the client configuration of cluster %s: %w", declared.clusterName, err)
	}

	return talosconfig, target, nil
}

func (c *Controller) bundle(
	ctx context.Context, talosconfig citypes.Secret, declared declaration,
) (citypes.Secret, error) {
	maintenance, err := c.talosconfig.InMaintenance(ctx, talosconfig)
	if err != nil {
		return "", fmt.Errorf("probing the node of cluster %s: %w", declared.clusterName, err)
	}

	if !maintenance {
		return c.storedBundle(ctx, talosconfig, declared)
	}

	minted, err := c.renderer.Mint()
	if err != nil {
		return "", fmt.Errorf("minting the secret bundle of cluster %s: %w", declared.clusterName, err)
	}

	return minted, nil
}

func (c *Controller) storedBundle(
	ctx context.Context, talosconfig citypes.Secret, declared declaration,
) (citypes.Secret, error) {
	bootstrapped, err := c.talosconfig.Bootstrapped(ctx, talosconfig)
	if err != nil {
		return "", fmt.Errorf("reading the bootstrap state of cluster %s: %w", declared.clusterName, err)
	}

	if !bootstrapped {
		return "", fmt.Errorf(
			"reading the secret bundle of cluster %s: its node holds a machine config and runs no cluster yet, "+
				"so no cluster holds the bundle. a first run stopped between the apply and the bootstrap, "+
				"and the bundle it minted is %s in the recovery directory the variable %s names",
			declared.clusterName, citypes.TalosRecoveryBundleFile, declared.recoveryDirEnv)
	}

	stored, found, err := c.talosconfig.StoredBundle(ctx, talosconfig)
	if err != nil {
		return "", fmt.Errorf("reading the secret bundle of cluster %s from the cluster: %w", declared.clusterName, err)
	}

	if !found {
		return "", fmt.Errorf(
			"reading the secret bundle of cluster %s: the cluster holds no secret %s/%s. "+
				"write it once from the recovery directory: %s",
			declared.clusterName, citypes.TalosBundleNamespace, citypes.TalosBundleSecret,
			restoreCommand(declared.recoveryDirEnv))
	}

	return stored, nil
}

func restoreCommand(recoveryDirEnv string) string {
	return "kubectl --namespace " + citypes.TalosBundleNamespace +
		" create secret generic " + citypes.TalosBundleSecret +
		" --from-file=" + citypes.TalosBundleKey + "=\"$" + recoveryDirEnv + "/" + citypes.TalosRecoveryBundleFile + "\""
}

func hostOf(endpoint string) string {
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return endpoint
	}

	return host
}

func (c *Controller) render(
	bundle citypes.Secret, cluster talossecretsadapter.Cluster, patch string,
) (citypes.Secret, error) {
	loaded, err := c.renderer.Load(bundle)
	if err != nil {
		return "", fmt.Errorf("loading the secret bundle of cluster %s: %w", cluster.Name, err)
	}

	config, err := c.renderer.MachineConfig(loaded, cluster, patch)
	if err != nil {
		return "", fmt.Errorf("rendering the machine config of cluster %s: %w", cluster.Name, err)
	}

	return config, nil
}

func (c *Controller) refuseAnInstallerImageNotPinnedByDigest(
	ctx context.Context, clusterName string, config citypes.Secret,
) error {
	reference, err := c.renderer.InstallerImage(config)
	if err != nil {
		return fmt.Errorf("reading the installer image of cluster %s: %w", clusterName, err)
	}

	if reference == "" {
		return fmt.Errorf(
			"reading the installer image of cluster %s: the rendered config holds no installer image "+
				"in an UnattendedInstallConfig document. declare installer.image there, pinned by digest",
			clusterName)
	}

	tagged, pinned, digested := strings.Cut(reference, "@")
	if !digested {
		return fmt.Errorf(
			"checking the installer image %s of cluster %s: it names no digest, so a rebuild behind its tag "+
				"would change what installs. pin it by digest",
			reference, clusterName)
	}

	tag, named := tagOf(tagged)
	if !named {
		return nil
	}

	resolved, err := c.registry.Digest(ctx, tagged)
	if err != nil {
		return fmt.Errorf("resolving the installer image %s of cluster %s: %w", reference, clusterName, err)
	}

	if resolved != pinned {
		return fmt.Errorf(
			"checking the installer image %s of cluster %s: the tag %s resolves to %s, and the reference pins %s",
			reference, clusterName, tag, resolved, pinned)
	}

	return nil
}

func tagOf(repository string) (string, bool) {
	colon := strings.LastIndex(repository, ":")
	if colon <= strings.LastIndex(repository, "/") {
		return "", false
	}

	return repository[colon+1:], true
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
