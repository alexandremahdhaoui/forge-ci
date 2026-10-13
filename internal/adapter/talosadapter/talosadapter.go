package talosadapter

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/cosi-project/runtime/pkg/resource"
	"github.com/cosi-project/runtime/pkg/safe"
	"github.com/cosi-project/runtime/pkg/state"
	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	"github.com/siderolabs/talos/pkg/machinery/client"
	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	configresource "github.com/siderolabs/talos/pkg/machinery/resources/config"
	"github.com/siderolabs/talos/pkg/machinery/resources/runtime"
	"github.com/siderolabs/talos/pkg/machinery/resources/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	ApplyModeAuto   = "auto"
	ApplyModeStaged = "staged"

	waitCeiling  = 10 * time.Minute
	pollInterval = 5 * time.Second

	etcdServiceID = "etcd"
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
	mode      applyMode
	rebooting bool
}

func New(mode string) (*Node, error) {
	if mode == "" {
		mode = ApplyModeAuto
	}

	chosen, known := applyModes[mode]
	if !known {
		return nil, fmt.Errorf(
			"reading the apply mode %q: this adapter knows %s and %s", mode, ApplyModeAuto, ApplyModeStaged)
	}

	return &Node{mode: chosen}, nil
}

type Credential struct{}

func NewKubeconfig() Credential {
	return Credential{}
}

func (Credential) Kubeconfig(ctx context.Context, talosconfig citypes.Secret) ([]byte, error) {
	held, target, err := clientConfiguration(talosconfig)
	if err != nil {
		return nil, fmt.Errorf("dialing the node the client configuration names: %w", err)
	}

	nodeClient, err := dialSecure(ctx, held, target)
	if err != nil {
		return nil, fmt.Errorf("dialing node %s: %w", target.Node, err)
	}

	defer func() { _ = nodeClient.Close() }()

	raw, err := nodeClient.Kubeconfig(client.WithNode(ctx, target.Node))
	if err != nil {
		return nil, fmt.Errorf("getting the kubeconfig of node %s: %w", target.Node, err)
	}

	return raw, nil
}

type Talosconfig struct{}

func NewTalosconfig() Talosconfig {
	return Talosconfig{}
}

func (Talosconfig) Context(talosconfig citypes.Secret) (citypes.TalosContext, error) {
	_, target, err := clientConfiguration(talosconfig)

	return target, err
}

func (n *Node) MachineConfig(ctx context.Context, talosconfig citypes.Secret) (string, error) {
	held, target, err := clientConfiguration(talosconfig)
	if err != nil {
		return "", fmt.Errorf("dialing the node the client configuration names: %w", err)
	}

	node := target.Node

	var raw string

	err = n.secure(ctx, held, target, "reading the machine config of node "+node,
		func(nodeClient *client.Client) error {
			held, err := safe.StateGetByID[*configresource.MachineConfig](
				client.WithNode(ctx, node), nodeClient.COSI, n.mode.read)
			if err != nil {
				return fmt.Errorf(
					"getting the %s machine config resource of node %s: %w", n.mode.read, node, err)
			}

			encoded, err := held.Provider().Bytes()
			if err != nil {
				return fmt.Errorf("encoding the machine config of node %s: %w", node, err)
			}

			raw = string(encoded)

			return nil
		})
	if err == nil {
		return raw, nil
	}

	if !n.inMaintenance(ctx, node, err) {
		return "", err
	}

	return "", nil
}

func (n *Node) ApplyMachineConfig(ctx context.Context, talosconfig citypes.Secret, machineConfig string) error {
	held, target, err := clientConfiguration(talosconfig)
	if err != nil {
		return fmt.Errorf("dialing the node the client configuration names: %w", err)
	}

	node := target.Node

	request := &machineapi.ApplyConfigurationRequest{
		Data:   []byte(machineConfig),
		Mode:   n.mode.request,
		DryRun: false,
	}

	err = n.secure(ctx, held, target, "applying the machine config to node "+node,
		func(nodeClient *client.Client) error {
			_, err := nodeClient.ApplyConfiguration(client.WithNode(ctx, node), request)
			if err != nil {
				return fmt.Errorf("applying the machine config to node %s: %w", node, err)
			}

			return nil
		})
	if err == nil {
		return nil
	}

	if !n.inMaintenance(ctx, node, err) {
		return err
	}

	maintenanceClient, err := dialMaintenance(ctx, node)
	if err != nil {
		return fmt.Errorf("dialing node %s in maintenance: %w", node, err)
	}

	defer func() { _ = maintenanceClient.Close() }()

	if _, err := maintenanceClient.ApplyConfiguration(ctx, request); err != nil {
		return fmt.Errorf("applying the machine config to node %s in maintenance: %w", node, err)
	}

	n.rebooting = true

	return nil
}

func (n *Node) Bootstrap(ctx context.Context, talosconfig citypes.Secret) error {
	held, target, err := clientConfiguration(talosconfig)
	if err != nil {
		return fmt.Errorf("dialing the node the client configuration names: %w", err)
	}

	node := target.Node

	return n.secure(ctx, held, target, "bootstrapping node "+node,
		func(nodeClient *client.Client) error {
			if err := nodeClient.Bootstrap(client.WithNode(ctx, node), &machineapi.BootstrapRequest{}); err != nil {
				return fmt.Errorf("bootstrapping node %s: %w", node, err)
			}

			return nil
		})
}

func (n *Node) Bootstrapped(ctx context.Context, talosconfig citypes.Secret) (bool, error) {
	held, target, err := clientConfiguration(talosconfig)
	if err != nil {
		return false, fmt.Errorf("dialing the node the client configuration names: %w", err)
	}

	node := target.Node

	var running bool

	err = n.secure(ctx, held, target, "reading the etcd service of node "+node,
		func(nodeClient *client.Client) error {
			service, err := safe.StateGetByID[*v1alpha1.Service](
				client.WithNode(ctx, node), nodeClient.COSI, etcdServiceID)
			if state.IsNotFoundError(err) {
				running = false

				return nil
			}

			if err != nil {
				return fmt.Errorf("getting the etcd service of node %s: %w", node, err)
			}

			running = service.TypedSpec().Running

			return nil
		})
	if err == nil {
		return running, nil
	}

	if !n.inMaintenance(ctx, node, err) {
		return false, err
	}

	return false, nil
}

func (n *Node) WaitNodeRegistered(ctx context.Context, talosconfig citypes.Secret) error {
	held, target, err := clientConfiguration(talosconfig)
	if err != nil {
		return fmt.Errorf("dialing the node the client configuration names: %w", err)
	}

	node := target.Node

	var raw []byte

	err = n.secure(ctx, held, target, "getting the kubeconfig of node "+node,
		func(nodeClient *client.Client) error {
			fetched, err := nodeClient.Kubeconfig(client.WithNode(ctx, node))
			if err != nil {
				return fmt.Errorf("getting the kubeconfig of node %s: %w", node, err)
			}

			raw = fetched

			return nil
		})
	if err != nil {
		return err
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig(raw)
	if err != nil {
		return fmt.Errorf("reading the kubeconfig of node %s: %w", node, err)
	}

	cluster, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("building the kubernetes client of node %s: %w", node, err)
	}

	return untilCeiling(ctx, "waiting for node "+node+" to register with the kubernetes api", func() error {
		nodes, err := cluster.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("listing nodes: %w", err)
		}

		for _, registered := range nodes.Items {
			for _, condition := range registered.Status.Conditions {
				if !condition.LastHeartbeatTime.IsZero() {
					return nil
				}
			}
		}

		return errors.New("no node has posted a status yet")
	})
}

func (n *Node) secure(
	ctx context.Context, held *clientconfig.Config, target citypes.TalosContext, what string,
	call func(*client.Client) error,
) error {
	attempt := func() error {
		nodeClient, err := dialSecure(ctx, held, target)
		if err != nil {
			return fmt.Errorf("dialing node %s: %w", target.Node, err)
		}

		defer func() { _ = nodeClient.Close() }()

		return call(nodeClient)
	}

	if !n.rebooting {
		return attempt()
	}

	return untilCeiling(ctx, what, attempt)
}

func (n *Node) inMaintenance(ctx context.Context, node string, secureErr error) bool {
	if !isCertificateError(secureErr) {
		return false
	}

	stage, err := probeStage(ctx, node)
	if err != nil {
		return false
	}

	return stage == runtime.MachineStageMaintenance
}

func probeStage(ctx context.Context, node string) (runtime.MachineStage, error) {
	maintenanceClient, err := dialMaintenance(ctx, node)
	if err != nil {
		return runtime.MachineStageUnknown, fmt.Errorf("dialing node %s in maintenance: %w", node, err)
	}

	defer func() { _ = maintenanceClient.Close() }()

	status, err := safe.StateGetByID[*runtime.MachineStatus](ctx, maintenanceClient.COSI, runtime.MachineStatusID)
	if err != nil {
		return runtime.MachineStageUnknown, fmt.Errorf("getting the machine status of node %s: %w", node, err)
	}

	return status.TypedSpec().Stage, nil
}

func isCertificateError(err error) bool {
	if err == nil {
		return false
	}

	text := err.Error()

	return strings.Contains(text, "x509:") || strings.Contains(text, "tls:")
}

func untilCeiling(ctx context.Context, what string, attempt func() error) error {
	deadline := time.Now().Add(waitCeiling)

	for {
		err := attempt()
		if err == nil {
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("%s: the ceiling of %s passed, the last cause: %w", what, waitCeiling, err)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w, the last cause: %w", what, ctx.Err(), err)
		case <-time.After(pollInterval):
		}
	}
}

func dialSecure(
	ctx context.Context, held *clientconfig.Config, target citypes.TalosContext,
) (*client.Client, error) {
	return client.New(ctx, client.WithConfig(held), client.WithEndpoints(target.Endpoints...))
}

func dialMaintenance(ctx context.Context, node string) (*client.Client, error) {
	return client.New(ctx, client.WithMaintenanceMode(node, nil))
}

func clientConfiguration(talosconfig citypes.Secret) (*clientconfig.Config, citypes.TalosContext, error) {
	raw, err := os.ReadFile(string(talosconfig))
	if err != nil {
		return nil, citypes.TalosContext{}, fmt.Errorf(
			"opening the client configuration file the manager resolved, "+
				"which must name a talosconfig file: %w", causeWithoutThePath(err))
	}

	held, err := clientconfig.FromBytes(raw)
	if err != nil {
		return nil, citypes.TalosContext{}, errors.New(
			"parsing the client configuration file the manager resolved: it is not a talosconfig document")
	}

	target, err := contextOf(held)
	if err != nil {
		return nil, citypes.TalosContext{}, err
	}

	return held, target, nil
}

func contextOf(held *clientconfig.Config) (citypes.TalosContext, error) {
	current, declared := held.Contexts[held.Context]
	if !declared || current == nil {
		return citypes.TalosContext{}, fmt.Errorf(
			"reading the client configuration: it names no context %q", held.Context)
	}

	if len(current.Endpoints) == 0 {
		return citypes.TalosContext{}, fmt.Errorf(
			"reading the client configuration: context %q names no endpoint, and every dial goes through its endpoints",
			held.Context)
	}

	if len(current.Nodes) != 1 {
		return citypes.TalosContext{}, fmt.Errorf(
			"reading the client configuration: context %q names %d nodes, and this release drives one node per cluster",
			held.Context, len(current.Nodes))
	}

	if err := refuseSchemes(held.Context, current.Endpoints); err != nil {
		return citypes.TalosContext{}, err
	}

	return citypes.TalosContext{Endpoints: current.Endpoints, Node: current.Nodes[0]}, nil
}

func refuseSchemes(contextName string, endpoints []string) error {
	for i, endpoint := range endpoints {
		if strings.Contains(endpoint, "://") {
			return fmt.Errorf(
				"reading the client configuration: endpoint %d of context %q carries a scheme, "+
					"and a talosconfig endpoint is a host or a host and port", i+1, contextName)
		}
	}

	return nil
}

func causeWithoutThePath(err error) error {
	var pathError *fs.PathError
	if errors.As(err, &pathError) {
		return pathError.Err
	}

	return err
}
