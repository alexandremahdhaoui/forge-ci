package talossecretsadapter

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/configpatcher"
	"github.com/siderolabs/talos/pkg/machinery/config/generate"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	"github.com/siderolabs/talos/pkg/machinery/config/machine"
	"sigs.k8s.io/yaml"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

type Cluster struct {
	Name              string
	Endpoint          string
	KubernetesVersion string
}

type Secrets struct{}

func New() Secrets { return Secrets{} }

func (Secrets) Load(bundle citypes.Secret) (citypes.Secret, error) {
	held, err := unmarshalBundle(bundle)
	if err != nil {
		return "", err
	}

	return marshalBundle(held)
}

func (Secrets) MachineConfig(bundle citypes.Secret, cluster Cluster, patch string) (citypes.Secret, error) {
	in, err := newInput(bundle, cluster)
	if err != nil {
		return "", err
	}

	generated, err := in.Config(machine.TypeControlPlane)
	if err != nil {
		return "", fmt.Errorf(
			"rendering the control plane machine config of cluster %q: %w", cluster.Name, err)
	}

	patched, err := applyPatch(generated, patch, cluster.Name)
	if err != nil {
		return "", err
	}

	raw, err := patched.Bytes()
	if err != nil {
		return "", fmt.Errorf("encoding the machine config of cluster %q: %w", cluster.Name, err)
	}

	return citypes.Secret(raw), nil
}

func newInput(bundle citypes.Secret, cluster Cluster) (*generate.Input, error) {
	held, err := unmarshalBundle(bundle)
	if err != nil {
		return nil, err
	}

	endpoint, err := endpointHost(cluster)
	if err != nil {
		return nil, err
	}

	in, err := generate.NewInput(
		cluster.Name, cluster.Endpoint, cluster.KubernetesVersion,
		generate.WithVersionContract(config.TalosVersionCurrent),
		generate.WithSecretsBundle(held),
		generate.WithEndpointList([]string{endpoint}),
	)
	if err != nil {
		return nil, fmt.Errorf("reading the declaration of cluster %q: %w", cluster.Name, err)
	}

	return in, nil
}

func endpointHost(cluster Cluster) (string, error) {
	parsed, err := url.Parse(cluster.Endpoint)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf(
			"reading the control plane endpoint %q of cluster %q: it must be a url naming a host",
			cluster.Endpoint, cluster.Name)
	}

	return parsed.Hostname(), nil
}

func applyPatch(
	generated config.Provider, patch, clusterName string,
) (configpatcher.Output, error) {
	if patch == "" {
		return nil, fmt.Errorf(
			"patching the machine config of cluster %q: this adapter needs a patch document and got none",
			clusterName)
	}

	loaded, err := configpatcher.LoadPatch([]byte(patch))
	if err != nil {
		return nil, fmt.Errorf("reading the patch document of cluster %q: %w", clusterName, err)
	}

	patched, err := configpatcher.Apply(
		configpatcher.WithConfig(generated), []configpatcher.Patch{loaded})
	if err != nil {
		return nil, fmt.Errorf("patching the machine config of cluster %q: %w", clusterName, err)
	}

	return patched, nil
}

func unmarshalBundle(bundle citypes.Secret) (*secrets.Bundle, error) {
	held := &secrets.Bundle{Clock: secrets.NewClock()}

	if err := yaml.Unmarshal([]byte(bundle), held); err != nil {
		return nil, errors.New(
			"reading the stored talos secret bundle: it is not a secret bundle document")
	}

	if err := held.Validate(config.TalosVersionCurrent); err != nil {
		return nil, fmt.Errorf("validating the stored talos secret bundle: %w", err)
	}

	return held, nil
}

func marshalBundle(held *secrets.Bundle) (citypes.Secret, error) {
	raw, err := yaml.Marshal(held)
	if err != nil {
		return "", errors.New("encoding the talos secret bundle: it does not marshal to yaml")
	}

	return citypes.Secret(raw), nil
}
