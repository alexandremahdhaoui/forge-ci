package talosadapter

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	stdx509 "crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	machineapi "github.com/siderolabs/talos/pkg/machinery/api/machine"
	clientconfig "github.com/siderolabs/talos/pkg/machinery/client/config"
	"github.com/siderolabs/talos/pkg/machinery/config"
	"github.com/siderolabs/talos/pkg/machinery/config/generate/secrets"
	configresource "github.com/siderolabs/talos/pkg/machinery/resources/config"
	"github.com/siderolabs/talos/pkg/machinery/role"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"sigs.k8s.io/yaml"

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
      - node-a.example.invalid
      - node-b.example.invalid:50000
    nodes:
      - node-a.example.invalid
`))
	require.NoError(t, err)
	assert.Equal(t, citypes.TalosContext{Endpoints: []string{"node-a.example.invalid", "node-b.example.invalid:50000"}, Node: "node-a.example.invalid"}, target)
}

func TestTheContextReaderRefusesAnEndpointCarryingASchemeAtEveryPositionNamingThePosition(t *testing.T) {
	t.Parallel()

	for position, endpoints := range map[string]string{
		"1": "[https://talos.example:50000, talos.example]",
		"2": "[talos.example, https://talos.example:50000]",
		"3": "[talos.example, talos.example:50000, tcp://talos.example]",
	} {
		t.Run("endpoint "+position, func(t *testing.T) {
			t.Parallel()

			_, err := NewTalosconfig().Context(writeTalosconfig(t,
				"context: t0\ncontexts:\n  t0:\n    endpoints: "+endpoints+"\n    nodes: [talos.example]\n"))
			require.Error(t, err)
			assert.Equal(t,
				"reading the client configuration: endpoint "+position+` of context "t0" carries a scheme, `+
					"and a talosconfig endpoint is a host or a host and port",
				err.Error())
		})
	}
}

func TestEveryManagerDialRefusesAnEndpointCarryingASchemeBeforeReachingAnything(t *testing.T) {
	t.Parallel()

	talosconfig := writeTalosconfig(t, `context: t0
contexts:
  t0:
    endpoints:
      - `+theClosedPort+`
      - https://`+theClosedPort+`
    nodes:
      - 127.0.0.1
`)

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	_, err = node.MachineConfig(t.Context(), talosconfig)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `endpoint 2 of context "t0" carries a scheme`)
	assert.NotContains(t, err.Error(), "connection refused")
}

func TestTheContextReaderRefusesAContextNamingNoEndpointByName(t *testing.T) {
	t.Parallel()

	_, err := NewTalosconfig().Context(writeTalosconfig(t, "context: t0\ncontexts:\n  t0:\n    nodes: [node-a.example.invalid]\n"))
	require.Error(t, err)
	assert.Equal(t,
		`reading the client configuration: context "t0" names no endpoint, and every dial goes through its endpoints`,
		err.Error())
}

func TestTheContextReaderRefusesAContextThatDoesNotDriveExactlyOneNodeNamingTheCount(t *testing.T) {
	t.Parallel()

	for count, nodes := range map[string]string{"0": "", "2": "\n    nodes: [node-a.example.invalid, node-b.example.invalid]"} {
		t.Run(count+" nodes", func(t *testing.T) {
			t.Parallel()

			_, err := NewTalosconfig().Context(writeTalosconfig(t,
				"context: t0\ncontexts:\n  t0:\n    endpoints: [node-a.example.invalid]"+nodes+"\n"))
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

	_, err := NewTalosconfig().Context(writeTalosconfig(t, "context: t1\ncontexts:\n  t0:\n    endpoints: [node-a.example.invalid]\n"))
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
			"dialing node node-a.example.invalid: %w", errors.New("x509: certificate signed by unknown authority")),
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

func TestACertificateErrorAgainstANodeThatAnswersNoProbeReturnsTheSecureErrorAndTheProbeFailureNamingTheNode(t *testing.T) {
	t.Parallel()

	secureErr := errors.New("tls: handshake failure")

	err := unlessInMaintenance(t.Context(), theClosedPort, secureErr)
	require.ErrorIs(t, err, secureErr)
	assert.Contains(t, err.Error(), "probing node "+theClosedPort+" for maintenance")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestAnyOtherSecureErrorIsReturnedAsItIsWithoutAProbe(t *testing.T) {
	t.Parallel()

	secureErr := errors.New("connection refused")

	assert.Same(t, secureErr, unlessInMaintenance(t.Context(), theClosedPort, secureErr))
}

func aTLSListenerRefusingEveryClientWithoutACertificate(t *testing.T) string {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &stdx509.Certificate{SerialNumber: big.NewInt(1), NotAfter: time.Now().Add(time.Hour)}
	der, err := stdx509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		ClientAuth:   tls.RequireAnyClientCert,
		NextProtos:   []string{"h2"},
		MinVersion:   tls.VersionTLS13,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}

			refused := conn.(*tls.Conn)
			_ = refused.Handshake()
			_, _ = io.Copy(io.Discard, refused.NetConn())
			_ = refused.Close()
		}
	}()

	return listener.Addr().String()
}

func TestANodeThatRefusesTheProbeAtTLSReadsAsAConfiguredNodeAndNeverAsAnError(t *testing.T) {
	t.Parallel()

	configured := aTLSListenerRefusingEveryClientWithoutACertificate(t)

	maintenance, err := NewTalosconfig().InMaintenance(t.Context(), writeTalosconfig(t, talosconfigFor(theClosedPort, configured)))
	require.NoError(t, err)
	assert.False(t, maintenance)

	secureErr := errors.New("x509: certificate signed by unknown authority")
	assert.Same(t, secureErr, unlessInMaintenance(t.Context(), configured, secureErr))
}

func talosconfigFor(endpoint, node string) string {
	return `context: t0
contexts:
  t0:
    endpoints:
      - ` + endpoint + `
    nodes:
      - ` + node + `
`
}

func TestEverySecureCallFailingOnACertificateAgainstANodeThatAnswersNoProbeReturnsBothFailuresNamingTheNode(t *testing.T) {
	t.Parallel()

	talosconfig := writeTalosconfig(t, talosconfigFor(aTLSListenerRefusingEveryClientWithoutACertificate(t), theClosedPort))

	for name, call := range map[string]func(*Node) error{
		"reading the machine config": func(node *Node) error {
			_, err := node.MachineConfig(t.Context(), talosconfig)

			return err
		},
		"applying the machine config": func(node *Node) error {
			return node.ApplyMachineConfig(t.Context(), talosconfig, "version: v1alpha1\n")
		},
		"reading the bootstrap state": func(node *Node) error {
			_, err := node.Bootstrapped(t.Context(), talosconfig)

			return err
		},
		"reading the stored bundle": func(node *Node) error {
			_, _, err := node.StoredBundle(t.Context(), talosconfig)

			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			node, err := New(ApplyModeAuto)
			require.NoError(t, err)

			err = call(node)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "x509:")
			assert.Contains(t, err.Error(), "probing node "+theClosedPort+" for maintenance")
			assert.Contains(t, err.Error(), "connection refused")
			assert.False(t, node.rebooting)
		})
	}
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

	talosconfig := writeTalosconfig(t, talosconfigReachingNothing+"      - node-b.example.invalid\n")

	node, err := New(ApplyModeAuto)
	require.NoError(t, err)

	_, err = node.MachineConfig(t.Context(), talosconfig)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "names 2 nodes")
	assert.NotContains(t, err.Error(), "connection refused")
}

func TestTheWaitStopsWhenTheContextEndsAndNamesTheLastCause(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	attempts := 0

	err := untilCeiling(ctx, "waiting for node node-a.example.invalid to register", func() error {
		attempts++

		return errors.New("no node has posted a status yet")
	})
	require.Error(t, err)
	assert.Equal(t, 1, attempts)
	assert.Contains(t, err.Error(), "waiting for node node-a.example.invalid to register")
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

const talosconfigWithTwoContexts = `context: t0
contexts:
  t0:
    endpoints:
      - node-a.example.invalid
      - node-b.example.invalid:50000
    nodes:
      - node-a.example.invalid
  other:
    endpoints:
      - other.example.invalid
    nodes:
      - other.example.invalid
    ca: dGhlIG90aGVyIGF1dGhvcml0eQ==
`

func throwawayBundle(t *testing.T) (citypes.Secret, *secrets.Bundle) {
	t.Helper()

	minted, err := secrets.NewBundle(secrets.NewFixedClock(time.Now()), config.TalosVersionCurrent)
	require.NoError(t, err)

	raw, err := yaml.Marshal(minted)
	require.NoError(t, err)

	return citypes.Secret(raw), minted
}

func TestSavingTheAdminCredentialKeepsTheEndpointsTheOneNodeAndEveryOtherContextAndTrustsTheBundleAuthority(t *testing.T) {
	t.Parallel()

	path := writeTalosconfig(t, talosconfigWithTwoContexts)
	bundle, minted := throwawayBundle(t)

	saved, err := (&Node{}).SaveTalosconfig(path, bundle)
	require.NoError(t, err)

	onDisk, err := os.ReadFile(string(path))
	require.NoError(t, err)
	assert.Equal(t, string(saved), string(onDisk))

	info, err := os.Stat(string(path))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	held, err := clientconfig.FromBytes(onDisk)
	require.NoError(t, err)
	assert.Equal(t, "t0", held.Context)

	current := held.Contexts["t0"]
	assert.Equal(t, []string{"node-a.example.invalid", "node-b.example.invalid:50000"}, current.Endpoints)
	assert.Equal(t, []string{"node-a.example.invalid"}, current.Nodes)
	assert.Equal(t, base64.StdEncoding.EncodeToString(minted.Certs.OS.Crt), current.CA)
	assert.Equal(t, "dGhlIG90aGVyIGF1dGhvcml0eQ==", held.Contexts["other"].CA)

	certificate := decodedCertificate(t, current.Crt)
	authority := decodedCertificate(t, current.CA)

	pool := stdx509.NewCertPool()
	pool.AddCert(authority)

	_, err = certificate.Verify(stdx509.VerifyOptions{
		Roots: pool, KeyUsages: []stdx509.ExtKeyUsage{stdx509.ExtKeyUsageClientAuth},
	})
	require.NoError(t, err, "the minted credential is signed by the bundle's os authority")
	assert.Contains(t, certificate.Subject.Organization, string(role.Admin))

	target, err := NewTalosconfig().Context(path)
	require.NoError(t, err)
	assert.Equal(t, "node-a.example.invalid", target.Node)
}

func decodedCertificate(t *testing.T, encoded string) *stdx509.Certificate {
	t.Helper()

	raw, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)

	block, _ := pem.Decode(raw)
	require.NotNil(t, block)

	parsed, err := stdx509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	return parsed
}

func TestSavingRefusesSomethingThatIsNotABundleWithoutEchoingItAndLeavesTheTalosconfigAlone(t *testing.T) {
	t.Parallel()

	for name, bundle := range map[string]citypes.Secret{
		"not yaml":        citypes.Secret(theKeyMarker + ": ["),
		"an empty bundle": citypes.Secret("cluster:\n  id: " + theKeyMarker + "\n"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			path := writeTalosconfig(t, talosconfigWithTwoContexts)

			_, err := (&Node{}).SaveTalosconfig(path, bundle)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "minting the admin credential")
			assert.NotContains(t, err.Error(), theKeyMarker)

			onDisk, err := os.ReadFile(string(path))
			require.NoError(t, err)
			assert.Equal(t, talosconfigWithTwoContexts, string(onDisk))
		})
	}
}

func TestSavingIntoAMissingTalosconfigNamesTheReasonAndNotThePath(t *testing.T) {
	t.Parallel()

	bundle, _ := throwawayBundle(t)
	path := filepath.Join(t.TempDir(), "absent.yaml")

	_, err := (&Node{}).SaveTalosconfig(citypes.Secret(path), bundle)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the client configuration the admin credential is saved into")
	assert.Contains(t, err.Error(), "no such file or directory")
	assert.NotContains(t, err.Error(), path)
}

func TestSavingIntoATalosconfigThatCannotBeWrittenNamesTheReasonAndNotThePath(t *testing.T) {
	t.Parallel()

	if os.Geteuid() == 0 {
		t.Skip("root writes a read only file, so the refusal cannot be provoked")
	}

	bundle, _ := throwawayBundle(t)
	path := writeTalosconfig(t, talosconfigWithTwoContexts)
	require.NoError(t, os.Chmod(string(path), 0o400))

	_, err := (&Node{}).SaveTalosconfig(path, bundle)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "saving the admin credential into the client configuration")
	assert.Contains(t, err.Error(), "permission denied")
	assert.NotContains(t, err.Error(), string(path))
}

func TestTheBundleSecretIsAbsentUntilStoredThenReadsBackTheSameBytes(t *testing.T) {
	t.Parallel()

	cluster := fake.NewClientset()
	bundle, _ := throwawayBundle(t)

	_, found, err := storedBundle(t.Context(), cluster)
	require.NoError(t, err)
	assert.False(t, found)

	require.NoError(t, storeBundle(t.Context(), cluster, bundle))

	stored, found, err := storedBundle(t.Context(), cluster)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, bundle, stored)

	secret, err := cluster.CoreV1().Secrets("kube-system").Get(t.Context(), "talos-secret-bundle", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeOpaque, secret.Type)
	assert.Equal(t, []string{"bundle.yaml"}, slices.Collect(maps.Keys(secret.Data)))
}

func TestStoringABundleOverOneTheClusterHoldsIsAnErrorNamingTheSecretAndNeverTheBytes(t *testing.T) {
	t.Parallel()

	cluster := fake.NewClientset()
	bundle, _ := throwawayBundle(t)

	require.NoError(t, storeBundle(t.Context(), cluster, bundle))

	err := storeBundle(t.Context(), cluster, citypes.Secret(theKeyMarker))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating secret kube-system/talos-secret-bundle")
	assert.NotContains(t, err.Error(), theKeyMarker)
}

func TestABundleSecretHoldingNothingUnderItsKeyIsRefusedByName(t *testing.T) {
	t.Parallel()

	cluster := fake.NewClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "talos-secret-bundle"},
		Data:       map[string][]byte{"secrets.yaml": []byte(theKeyMarker)},
	})

	_, found, err := storedBundle(t.Context(), cluster)
	require.Error(t, err)
	assert.False(t, found)
	assert.Equal(t,
		"reading secret kube-system/talos-secret-bundle: it holds nothing under bundle.yaml, "+
			"and the talos secret bundle lives there", err.Error())
}

func TestReadingTheBundleSecretTheClusterRefusesIsAnErrorNamingTheSecret(t *testing.T) {
	t.Parallel()

	cluster := fake.NewClientset()
	cluster.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, errors.New("forbidden")
	})

	_, found, err := storedBundle(t.Context(), cluster)
	require.Error(t, err)
	assert.False(t, found)
	assert.Equal(t, "getting secret kube-system/talos-secret-bundle: forbidden", err.Error())
}

func TestReachingTheBundleOnAnUnreachableNodeFailsOnTheKubeconfigNamingTheNode(t *testing.T) {
	t.Parallel()

	talosconfig := writeTalosconfig(t, talosconfigReachingNothing)
	bundle, _ := throwawayBundle(t)

	_, found, err := (&Node{}).StoredBundle(t.Context(), talosconfig)
	require.Error(t, err)
	assert.False(t, found)
	assert.Contains(t, err.Error(), "getting the kubeconfig of node 127.0.0.1")
	assert.Contains(t, err.Error(), "connection refused")

	_, found, err = NewTalosconfig().StoredBundle(t.Context(), talosconfig)
	require.Error(t, err)
	assert.False(t, found)
	assert.Contains(t, err.Error(), "getting the kubeconfig of node 127.0.0.1")

	err = (&Node{}).StoreBundle(t.Context(), talosconfig, bundle)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "getting the kubeconfig of node 127.0.0.1")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestEveryBundleCallRefusesAMissingTalosconfigNamingTheReasonAndNotThePath(t *testing.T) {
	t.Parallel()

	path := citypes.Secret(filepath.Join(t.TempDir(), "absent.yaml"))

	_, _, err := (&Node{}).StoredBundle(t.Context(), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such file or directory")
	assert.NotContains(t, err.Error(), string(path))

	err = (&Node{}).StoreBundle(t.Context(), path, "bundle")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no such file or directory")
	assert.NotContains(t, err.Error(), string(path))

	_, err = NewTalosconfig().InMaintenance(t.Context(), path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "probing the node the client configuration names")
	assert.NotContains(t, err.Error(), string(path))
}

func TestAnUnreachableNodeIsAProbeErrorNamingTheNodeAndNeverReadsAsMaintenance(t *testing.T) {
	t.Parallel()

	maintenance, err := NewTalosconfig().InMaintenance(t.Context(), writeTalosconfig(t, talosconfigReachingNothing))
	require.Error(t, err)
	assert.False(t, maintenance)
	assert.Contains(t, err.Error(), "probing node 127.0.0.1 for maintenance")
	assert.Contains(t, err.Error(), "connection refused")
}

func TestTheEngineReadsTheBootstrapStateThroughTheSameSecureDialAsTheManager(t *testing.T) {
	t.Parallel()

	bootstrapped, err := NewTalosconfig().Bootstrapped(t.Context(), writeTalosconfig(t, talosconfigReachingNothing))
	require.Error(t, err)
	assert.False(t, bootstrapped)
	assert.Contains(t, err.Error(), "getting the etcd service of node 127.0.0.1")
	assert.Contains(t, err.Error(), "connection refused")
}
