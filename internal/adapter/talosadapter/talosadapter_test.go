package talosadapter

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	configresource "github.com/siderolabs/talos/pkg/machinery/resources/config"
	"github.com/siderolabs/talos/pkg/machinery/resources/runtime"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

func TestAnEmptyApplyModeMeansAuto(t *testing.T) {
	t.Parallel()

	node, err := New("")
	require.NoError(t, err)
	assert.Equal(t, machineapi.ApplyConfigurationRequest_AUTO, node.mode.request)
}

func TestAnAutoApplyComparesAgainstTheConfigTheNodeIsRunning(t *testing.T) {
	t.Parallel()

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)
	assert.Equal(t, configresource.ActiveID, node.mode.read)
}

func TestAStagedApplyComparesAgainstTheConfigTheNodeSavedAndNotTheRunningOne(t *testing.T) {
	t.Parallel()

	node, err := New(ApplyModeStaged)
	require.NoError(t, err)
	assert.Equal(t, machineapi.ApplyConfigurationRequest_STAGED, node.mode.request)
	assert.Equal(t, configresource.PersistentID, node.mode.read)
	assert.NotEqual(t, configresource.ActiveID, node.mode.read)
}

func TestAnUnknownApplyModeIsRefusedByName(t *testing.T) {
	t.Parallel()

	_, err := New("try")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `reading the apply mode "try"`)
	assert.Contains(t, err.Error(), "auto")
	assert.Contains(t, err.Error(), "staged")
}

const theKeyMarker = "ZZZTOPSECRETZZZclientprivatekey"

var thePastedClientConfiguration = citypes.Secret("-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAABlwAAAAdzc2gtcn\n" +
	theKeyMarker + "\n" +
	strings.Repeat("cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n", 24) +
	"-----END OPENSSH PRIVATE KEY-----\n")

func TestThePastedClientConfigurationFixtureMatchesARealKeyInSizeAndClearsThePathCeiling(t *testing.T) {
	t.Parallel()

	assert.Greater(t, len(thePastedClientConfiguration), 1400)
	assert.Greater(t, len(thePastedClientConfiguration), 255)
}

func assertNothingEchoesTheSlot(t *testing.T, err error) {
	t.Helper()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "dialing the node the client configuration names")
	assert.Contains(t, err.Error(), "must name a talosconfig file")
	assert.NotContains(t, err.Error(), theKeyMarker)
	assert.NotContains(t, err.Error(), "BEGIN OPENSSH PRIVATE KEY")
}

func TestReadingAMachineConfigNamesTheNodeAndNeverEchoesTheClientConfigurationSlot(t *testing.T) {
	t.Chdir(t.TempDir())

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	_, err = node.MachineConfig(t.Context(), thePastedClientConfiguration)
	assertNothingEchoesTheSlot(t, err)
}

func TestApplyingAMachineConfigNamesTheNodeAndNeverEchoesTheClientConfigurationSlot(t *testing.T) {
	t.Chdir(t.TempDir())

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	err = node.ApplyMachineConfig(
		t.Context(), thePastedClientConfiguration, "version: v1alpha1\n")
	assertNothingEchoesTheSlot(t, err)
}

func TestAPastedSlotShortEnoughToBeAFilenameIsNeverTurnedIntoAFileOnDisk(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	_, err = node.MachineConfig(t.Context(), citypes.Secret("swordfish"))
	require.Error(t, err)

	left, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, left)
}

func TestAMissingClientConfigurationFileNamesTheReasonAndNotThePath(t *testing.T) {
	t.Chdir(t.TempDir())

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	_, err = node.MachineConfig(t.Context(), "/etc/t0/absent.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such file or directory")
	assert.NotContains(t, err.Error(), "/etc/t0/absent.yaml")
}

func TestAClientConfigurationFileThatIsNotATalosconfigIsRefusedWithoutEchoingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "talosconfig")

	require.NoError(t, os.WriteFile(path, []byte(theKeyMarker+": ["), 0o600))

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	_, err = node.MachineConfig(t.Context(), citypes.Secret(path))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a talosconfig document")
	assert.NotContains(t, err.Error(), theKeyMarker)
}

const (
	theClosedPort = "127.0.0.1:1"

	talosconfigReachingNothing = `context: t0
contexts:
  t0:
    endpoints:
      - ` + theClosedPort + `
    nodes:
      - 127.0.0.1
`
)

func writeTalosconfig(t *testing.T, document string) citypes.Secret {
	t.Helper()

	path := filepath.Join(t.TempDir(), "talosconfig")
	require.NoError(t, os.WriteFile(path, []byte(document), 0o600))

	return citypes.Secret(path)
}

func TestTheContextReaderAnswersEveryEndpointAndTheOneNode(t *testing.T) {
	t.Parallel()

	target, err := NewTalosconfig().Context(writeTalosconfig(t, `context: t0
contexts:
  t0:
    endpoints:
      - box.lan
      - spare.lan:50000
    nodes:
      - box.lan
`))
	require.NoError(t, err)
	assert.Equal(t, citypes.TalosContext{Endpoints: []string{"box.lan", "spare.lan:50000"}, Node: "box.lan"}, target)
}

func TestTheContextReaderRefusesAContextNamingNoEndpointByName(t *testing.T) {
	t.Parallel()

	_, err := NewTalosconfig().Context(writeTalosconfig(t, "context: t0\ncontexts:\n  t0:\n    nodes: [box.lan]\n"))
	require.Error(t, err)
	assert.Equal(t,
		`reading the client configuration: context "t0" names no endpoint, and every dial goes through its endpoints`,
		err.Error())
}

func TestTheContextReaderRefusesAContextThatDoesNotDriveExactlyOneNodeNamingTheCount(t *testing.T) {
	t.Parallel()

	for count, nodes := range map[string]string{"0": "", "2": "\n    nodes: [box.lan, spare.lan]"} {
		t.Run(count+" nodes", func(t *testing.T) {
			t.Parallel()

			_, err := NewTalosconfig().Context(writeTalosconfig(t,
				"context: t0\ncontexts:\n  t0:\n    endpoints: [box.lan]"+nodes+"\n"))
			require.Error(t, err)
			assert.Equal(t,
				`reading the client configuration: context "t0" names `+count+
					" nodes, and this release drives one node per cluster",
				err.Error())
		})
	}
}

func TestTheContextReaderRefusesATalosconfigWhoseCurrentContextIsNotDeclared(t *testing.T) {
	t.Parallel()

	_, err := NewTalosconfig().Context(writeTalosconfig(t, "context: t1\ncontexts:\n  t0:\n    endpoints: [box.lan]\n"))
	require.Error(t, err)
	assert.Equal(t, `reading the client configuration: it names no context "t1"`, err.Error())
}

func TestTheContextReaderNamesTheReasonAndNotThePathWhenTheFileIsMissing(t *testing.T) {
	t.Parallel()

	_, err := NewTalosconfig().Context("/etc/t0/absent.yaml")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such file or directory")
	assert.NotContains(t, err.Error(), "/etc/t0/absent.yaml")
}

func TestACertificateErrorIsToldApartFromEveryOtherFailure(t *testing.T) {
	t.Parallel()

	for name, err := range map[string]error{
		"a handshake the node refused": errors.New(
			`rpc error: code = Unavailable desc = connection error: desc = "transport: authentication handshake failed: ` +
				`tls: failed to verify certificate: x509: certificate signed by unknown authority"`),
		"a bare tls failure":  errors.New("tls: handshake failure"),
		"a bare x509 failure": errors.New("x509: certificate has expired or is not yet valid"),
		"a wrapped x509 failure": fmt.Errorf(
			"dialing node box.lan: %w", errors.New("x509: certificate signed by unknown authority")),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.True(t, isCertificateError(err))
		})
	}

	for name, err := range map[string]error{
		"nothing":            nil,
		"connection refused": errors.New("dial tcp " + theClosedPort + ": connect: connection refused"),
		"a timeout":          errors.New("rpc error: code = DeadlineExceeded desc = context deadline exceeded"),
		"a refused document": errors.New("rpc error: code = InvalidArgument desc = duplicate document"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.False(t, isCertificateError(err))
		})
	}
}

func TestANodeThatRefusesTheConnectionIsReportedWithTheSecureErrorAndNeverTheProbes(t *testing.T) {
	t.Parallel()

	talosconfig := writeTalosconfig(t, talosconfigReachingNothing)

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	_, err = node.MachineConfig(t.Context(), talosconfig)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting the "+string(configresource.ActiveID)+" machine config resource of node 127.0.0.1")
	assert.Contains(t, err.Error(), "connection refused")
	assert.NotContains(t, err.Error(), "maintenance")

	err = node.ApplyMachineConfig(t.Context(), talosconfig, "version: v1alpha1\n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "applying the machine config to node 127.0.0.1")
	assert.Contains(t, err.Error(), "connection refused")
	assert.NotContains(t, err.Error(), "maintenance")
	assert.False(t, node.rebooting)
}

func TestACertificateErrorAgainstANodeThatAnswersNoProbeStaysTheSecureError(t *testing.T) {
	t.Parallel()

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	assert.False(t, node.inMaintenance(t.Context(), theClosedPort, errors.New("tls: handshake failure")))
	assert.False(t, node.inMaintenance(t.Context(), theClosedPort, errors.New("connection refused")))
}

func TestAnUnreachableNodeIsAnErrorAndNeverReadsAsNotBootstrapped(t *testing.T) {
	t.Parallel()

	talosconfig := writeTalosconfig(t, talosconfigReachingNothing)

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	bootstrapped, err := node.Bootstrapped(t.Context(), talosconfig)
	require.Error(t, err)
	assert.False(t, bootstrapped)
	assert.Contains(t, err.Error(), "getting the etcd service of node 127.0.0.1")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestABootstrapAgainstAnUnreachableNodeNamesTheNodeAndTheCause(t *testing.T) {
	t.Parallel()

	talosconfig := writeTalosconfig(t, talosconfigReachingNothing)

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	err = node.Bootstrap(t.Context(), talosconfig)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bootstrapping node 127.0.0.1")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestWaitingForAnUnreachableNodeFailsOnTheKubeconfigBeforeAnyKubernetesDial(t *testing.T) {
	t.Parallel()

	talosconfig := writeTalosconfig(t, talosconfigReachingNothing)

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	err = node.WaitNodeRegistered(t.Context(), talosconfig)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting the kubeconfig of node 127.0.0.1")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestTheCredentialReadsTheNodeAndTheEndpointsFromTheTalosconfigAndNamesTheNodeOnFailure(t *testing.T) {
	t.Parallel()

	_, err := NewKubeconfig().Kubeconfig(t.Context(), writeTalosconfig(t, talosconfigReachingNothing))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting the kubeconfig of node 127.0.0.1")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestTheCredentialNeverEchoesTheClientConfigurationSlot(t *testing.T) {
	t.Chdir(t.TempDir())

	_, err := NewKubeconfig().Kubeconfig(t.Context(), thePastedClientConfiguration)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must name a talosconfig file")
	assert.NotContains(t, err.Error(), theKeyMarker)
}

func TestEveryDialRefusesATalosconfigDrivingTwoNodesBeforeReachingAnything(t *testing.T) {
	t.Parallel()

	talosconfig := writeTalosconfig(t, talosconfigReachingNothing+"      - spare.lan\n")

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	_, err = node.MachineConfig(t.Context(), talosconfig)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "names 2 nodes")
	assert.NotContains(t, err.Error(), "connection refused")
}

func aNodeSpeakingWithAnUnknownCertificate(t *testing.T) citypes.Secret {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		MinVersion:   tls.VersionTLS12,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			_ = conn.(*tls.Conn).Handshake()
			_ = conn.Close()
		}
	}()

	return writeTalosconfig(t, "context: t0\ncontexts:\n  t0:\n    endpoints:\n      - "+
		listener.Addr().String()+"\n    nodes:\n      - 127.0.0.1\n")
}

func stageAnswering(stage runtime.MachineStage) func(context.Context, string) (runtime.MachineStage, error) {
	return func(context.Context, string) (runtime.MachineStage, error) { return stage, nil }
}

func TestANodeInMaintenanceHoldsNoClusterSoItsBootstrapStateReadsFalse(t *testing.T) {
	t.Parallel()

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	node.stage = stageAnswering(runtime.MachineStageMaintenance)

	bootstrapped, err := node.Bootstrapped(t.Context(), aNodeSpeakingWithAnUnknownCertificate(t))
	require.NoError(t, err)
	assert.False(t, bootstrapped)
}

func TestANodeInMaintenanceHoldsNoConfigSoItsMachineConfigReadsEmpty(t *testing.T) {
	t.Parallel()

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	node.stage = stageAnswering(runtime.MachineStageMaintenance)

	running, err := node.MachineConfig(t.Context(), aNodeSpeakingWithAnUnknownCertificate(t))
	require.NoError(t, err)
	assert.Empty(t, running)
}

func TestACertificateErrorFromANodeInAnyOtherStageIsTheSecureErrorByName(t *testing.T) {
	t.Parallel()

	for name, stage := range map[string]func(context.Context, string) (runtime.MachineStage, error){
		"running":   stageAnswering(runtime.MachineStageRunning),
		"booting":   stageAnswering(runtime.MachineStageBooting),
		"unknown":   stageAnswering(runtime.MachineStageUnknown),
		"no answer": func(context.Context, string) (runtime.MachineStage, error) { return 0, errors.New("no probe") },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			node, err := New(ApplyModeAuto)
			require.NoError(t, err)

			node.stage = stage
			talosconfig := aNodeSpeakingWithAnUnknownCertificate(t)

			bootstrapped, err := node.Bootstrapped(t.Context(), talosconfig)
			require.Error(t, err)
			assert.False(t, bootstrapped)
			assert.Contains(t, err.Error(), "getting the etcd service of node 127.0.0.1")
			assert.Contains(t, err.Error(), "x509")
			assert.NotContains(t, err.Error(), "no probe")

			_, err = node.MachineConfig(t.Context(), talosconfig)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "x509")
			assert.NotContains(t, err.Error(), "no probe")

			err = node.ApplyMachineConfig(t.Context(), talosconfig, "version: v1alpha1\n")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "applying the machine config to node 127.0.0.1")
			assert.Contains(t, err.Error(), "x509")
			assert.NotContains(t, err.Error(), "maintenance")
			assert.False(t, node.rebooting)
		})
	}
}

func TestANewNodeProbesTheMachineStatusForItsStage(t *testing.T) {
	t.Parallel()

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	_, err = node.stage(t.Context(), theClosedPort)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "node "+theClosedPort)
}

func TestTheWaitStopsWhenTheContextEndsAndNamesTheLastCause(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	attempts := 0

	err := untilCeiling(ctx, "waiting for node box.lan to register", func() error {
		attempts++

		return errors.New("no node has posted a status yet")
	})
	require.Error(t, err)
	assert.Equal(t, 1, attempts)
	assert.Contains(t, err.Error(), "waiting for node box.lan to register")
	assert.Contains(t, err.Error(), "context canceled")
	assert.Contains(t, err.Error(), "the last cause: no node has posted a status yet")
}

func TestTheWaitReturnsAtOnceWhenTheFirstAttemptSucceeds(t *testing.T) {
	t.Parallel()

	require.NoError(t, untilCeiling(t.Context(), "waiting", func() error { return nil }))
}

func TestARebootingNodeMakesEverySecureDialWaitInsteadOfFailingAtOnce(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	node.rebooting = true

	_, err = node.MachineConfig(ctx, writeTalosconfig(t, talosconfigReachingNothing))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the machine config of node 127.0.0.1")
	assert.Contains(t, err.Error(), "the last cause")
}
