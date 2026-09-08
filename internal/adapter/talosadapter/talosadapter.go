package talosadapter

import (
	"context"
	"fmt"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/client"
	configresource "github.com/siderolabs/talos/pkg/machinery/resources/config"
)

const (
	ApplyModeAuto   = "auto"
	ApplyModeStaged = "staged"
)

type applyMode struct {
	request machineapi.ApplyConfigurationRequest_Mode
	read    resource.ID
}

var applyModes = map[string]applyMode{
	ApplyModeAuto: {
		request: machineapi.ApplyConfigurationRequest_AUTO,
		read:    configresource.ActiveID,
	},
	ApplyModeStaged: {
		request: machineapi.ApplyConfigurationRequest_STAGED,
		read:    configresource.PersistentID,
	},
}

type Node struct {
	mode applyMode
}

func New(mode string) (Node, error) {
	if mode == "" {
		mode = ApplyModeAuto
	}

	chosen, known := applyModes[mode]
	if !known {
		return Node{}, fmt.Errorf(
			"reading the apply mode %q: this adapter knows %s and %s", mode, ApplyModeAuto, ApplyModeStaged)
	}

	return Node{mode: chosen}, nil
}

func (n Node) MachineConfig(ctx context.Context, node, talosconfig string) (string, error) {
	nodeClient, err := n.dial(ctx, node, talosconfig)
	if err != nil {
		return "", err
	}

	defer func() { _ = nodeClient.Close() }()

	held, err := safe.StateGetByID[*configresource.MachineConfig](
		client.WithNode(ctx, node), nodeClient.COSI, n.mode.read)
	if err != nil {
		return "", fmt.Errorf(
			"getting the %s machine config resource of node %s: %w", n.mode.read, node, err)
	}

	raw, err := held.Provider().Bytes()
	if err != nil {
		return "", fmt.Errorf("encoding the machine config of node %s: %w", node, err)
	}

	return string(raw), nil
}

func (n Node) ApplyMachineConfig(ctx context.Context, node, talosconfig, machineConfig string) error {
	nodeClient, err := n.dial(ctx, node, talosconfig)
	if err != nil {
		return err
	}

	defer func() { _ = nodeClient.Close() }()

	_, err = nodeClient.ApplyConfiguration(client.WithNode(ctx, node), &machineapi.ApplyConfigurationRequest{
		Data:   []byte(machineConfig),
		Mode:   n.mode.request,
		DryRun: false,
	})
	if err != nil {
		return fmt.Errorf("applying the machine config to node %s: %w", node, err)
	}

	return nil
}

func (n Node) dial(ctx context.Context, node, talosconfig string) (*client.Client, error) {
	nodeClient, err := client.New(ctx, client.WithConfigFromFile(talosconfig))
	if err != nil {
		return nil, fmt.Errorf("dialing node %s with the client configuration %s: %w", node, talosconfig, err)
	}

	return nodeClient, nil
}
