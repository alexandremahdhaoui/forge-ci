package managercontroller_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/execadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/managercontrollermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	theNode          = "10.0.0.1"
	theEndpoint      = "10.0.0.1"
	theKindCluster   = "a-cluster"
	theVariable      = "A_TALOSCONFIG_THIS_TEST_OWNS"
	theKubeconfigDoc = "apiVersion: v1\nkind: Config\n"
)

func talosSpec(block map[string]any) map[string]any {
	return map[string]any{"kubeconfig": map[string]any{"talos": block}}
}

func TestAPathSourceIsReadBackWithTheFileItNames(t *testing.T) {
	t.Parallel()

	declared, err := managercontroller.Kubeconfig(
		map[string]any{"kubeconfig": map[string]any{"path": "/etc/kubeconfig"}})
	require.NoError(t, err)
	assert.Equal(t, managercontroller.DeclaredKubeconfig{
		Source: managercontroller.SourcePath, Path: "/etc/kubeconfig",
	}, declared)
}

func TestATalosSourceIsReadBackWithItsNodeItsEndpointAndTheVariableItNames(t *testing.T) {
	t.Parallel()

	declared, err := managercontroller.Kubeconfig(talosSpec(map[string]any{
		"node": theNode, "endpoint": theEndpoint, "talosconfigEnv": theVariable,
	}))
	require.NoError(t, err)
	assert.Equal(t, managercontroller.DeclaredKubeconfig{
		Source:         managercontroller.SourceTalos,
		Node:           theNode,
		Endpoint:       theEndpoint,
		TalosconfigEnv: theVariable,
	}, declared)
}

func TestAKindSourceIsReadBackWithTheClusterItNames(t *testing.T) {
	t.Parallel()

	declared, err := managercontroller.Kubeconfig(
		map[string]any{"kubeconfig": map[string]any{"kind": map[string]any{"cluster": theKindCluster}}})
	require.NoError(t, err)
	assert.Equal(t, managercontroller.DeclaredKubeconfig{
		Source: managercontroller.SourceKind, Cluster: theKindCluster,
	}, declared)
}

func TestAManagerDeclaringNoKubeconfigIsRefusedNamingEverySourceItCouldHaveDeclared(t *testing.T) {
	t.Parallel()

	for _, spec := range []map[string]any{nil, {}, {"kubeconfig": map[string]any{}}} {
		_, err := managercontroller.Kubeconfig(spec)
		require.Error(t, err)
		assert.Equal(t,
			"reading spec.kubeconfig: it declares no source, "+
				"and the cluster credential comes from path or talos or kind", err.Error())
	}
}

func TestAManagerDeclaringTwoSourcesAtOnceIsRefusedNamingTheOnesItFound(t *testing.T) {
	t.Parallel()

	_, err := managercontroller.Kubeconfig(map[string]any{"kubeconfig": map[string]any{
		"path": "/etc/kubeconfig",
		"kind": map[string]any{"cluster": theKindCluster},
	}})
	require.Error(t, err)
	assert.Equal(t,
		"reading spec.kubeconfig: it declares path and kind, "+
			"and exactly one of path or talos or kind is declared", err.Error())
}

func TestAManagerDeclaringAllThreeSourcesAtOnceIsRefusedNamingAllThree(t *testing.T) {
	t.Parallel()

	_, err := managercontroller.Kubeconfig(map[string]any{"kubeconfig": map[string]any{
		"path":  "/etc/kubeconfig",
		"talos": map[string]any{"node": theNode},
		"kind":  map[string]any{"cluster": theKindCluster},
	}})
	require.Error(t, err)
	assert.Equal(t,
		"reading spec.kubeconfig: it declares path and talos and kind, "+
			"and exactly one of path or talos or kind is declared", err.Error())
}

func TestAKubeconfigKeyThatIsNoneOfTheThreeSourcesIsRefusedNamingItAndTheThreeItCouldHaveBeen(t *testing.T) {
	t.Parallel()

	_, err := managercontroller.Kubeconfig(
		map[string]any{"kubeconfig": map[string]any{"serviceAccount": "in-cluster"}})
	require.Error(t, err)
	assert.Equal(t,
		`reading spec.kubeconfig: it declares "serviceAccount", `+
			"and the cluster credential comes from path or talos or kind", err.Error())
}

func TestAKubeconfigThatIsNotAMapIsRefusedByTheHelperThatReadsTheSpec(t *testing.T) {
	t.Parallel()

	_, err := managercontroller.Kubeconfig(map[string]any{"kubeconfig": "/etc/kubeconfig"})
	require.Error(t, err)
	assert.Equal(t,
		"reading spec.kubeconfig: a map is required, the spec holds a string", err.Error())
}

func TestAPathSourceNamingNoFileIsRefusedByName(t *testing.T) {
	t.Parallel()

	for _, path := range []any{"", "   "} {
		_, err := managercontroller.Kubeconfig(
			map[string]any{"kubeconfig": map[string]any{"path": path}})
		require.Error(t, err)
		assert.Equal(t,
			"reading spec.kubeconfig.path: it names no file, "+
				"and a path source names a kubeconfig file", err.Error())
	}
}

func TestATalosSourceMissingEveryKeyIsRefusedNamingEveryKeyItLacks(t *testing.T) {
	t.Parallel()

	_, err := managercontroller.Kubeconfig(talosSpec(nil))
	require.Error(t, err)
	assert.Equal(t,
		"reading spec.kubeconfig.talos: it declares no node and endpoint and talosconfigEnv, "+
			"and a talos source declares node, endpoint, talosconfigEnv", err.Error())
}

func TestATalosSourceMissingOnlyTheVariableIsRefusedNamingThatOneKeyAndNeverFallsBackToADefaultVariable(t *testing.T) {
	t.Parallel()

	_, err := managercontroller.Kubeconfig(
		talosSpec(map[string]any{"node": theNode, "endpoint": theEndpoint}))
	require.Error(t, err)
	assert.Equal(t,
		"reading spec.kubeconfig.talos: it declares no talosconfigEnv, "+
			"and a talos source declares node, endpoint, talosconfigEnv", err.Error())
}

func TestATalosKeyThatIsNotAStringIsRefusedByTheHelperThatReadsTheSpec(t *testing.T) {
	t.Parallel()

	_, err := managercontroller.Kubeconfig(talosSpec(map[string]any{"node": 7}))
	require.Error(t, err)
	assert.Equal(t,
		"reading spec.kubeconfig.talos.node: a string is required, the spec holds a int", err.Error())
}

func TestAPathThatIsNotAStringIsRefusedNamingTheKeyTheSchemaDeclares(t *testing.T) {
	t.Parallel()

	_, err := managercontroller.Kubeconfig(
		map[string]any{"kubeconfig": map[string]any{"path": 7}})
	require.Error(t, err)
	assert.Equal(t,
		"reading spec.kubeconfig.path: a string is required, the spec holds a int", err.Error())
}

func TestATalosBlockThatIsNotAMapIsRefusedNamingTheKeyTheSchemaDeclares(t *testing.T) {
	t.Parallel()

	_, err := managercontroller.Kubeconfig(
		map[string]any{"kubeconfig": map[string]any{"talos": "10.0.0.1"}})
	require.Error(t, err)
	assert.Equal(t,
		"reading spec.kubeconfig.talos: a map is required, the spec holds a string", err.Error())
}

func TestAKindBlockThatIsNotAMapIsRefusedNamingTheKeyTheSchemaDeclares(t *testing.T) {
	t.Parallel()

	_, err := managercontroller.Kubeconfig(
		map[string]any{"kubeconfig": map[string]any{"kind": theKindCluster}})
	require.Error(t, err)
	assert.Equal(t,
		"reading spec.kubeconfig.kind: a map is required, the spec holds a string", err.Error())
}

func TestAKindClusterThatIsNotAStringIsRefusedNamingTheKeyTheSchemaDeclares(t *testing.T) {
	t.Parallel()

	_, err := managercontroller.Kubeconfig(
		map[string]any{"kubeconfig": map[string]any{"kind": map[string]any{"cluster": 7}}})
	require.Error(t, err)
	assert.Equal(t,
		"reading spec.kubeconfig.kind.cluster: a string is required, the spec holds a int", err.Error())
}

func TestAKindSourceNamingNoClusterIsRefusedByName(t *testing.T) {
	t.Parallel()

	for _, block := range []any{map[string]any{}, map[string]any{"cluster": " "}} {
		_, err := managercontroller.Kubeconfig(
			map[string]any{"kubeconfig": map[string]any{"kind": block}})
		require.Error(t, err)
		assert.Equal(t,
			"reading spec.kubeconfig.kind: it declares no cluster, "+
				"and a kind source declares cluster", err.Error())
	}
}

func TestEverySourceNamesItselfSoARefusalSaysWhereTheCredentialWasComingFrom(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "path /etc/kubeconfig", managercontroller.DeclaredKubeconfig{
		Source: managercontroller.SourcePath, Path: "/etc/kubeconfig",
	}.Names())
	assert.Equal(t, "talos node "+theNode+" through endpoint "+theEndpoint,
		managercontroller.DeclaredKubeconfig{
			Source: managercontroller.SourceTalos, Node: theNode, Endpoint: theEndpoint,
		}.Names())
	assert.Equal(t, "kind cluster "+theKindCluster, managercontroller.DeclaredKubeconfig{
		Source: managercontroller.SourceKind, Cluster: theKindCluster,
	}.Names())
	assert.Equal(t, "", managercontroller.DeclaredKubeconfig{}.Names())
}

func TestAPathDeclarationPicksTheAdapterThatReadsThatFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(path, []byte(theKubeconfigDoc), 0o600))

	source, err := managercontroller.NewKubeconfigSource(
		managercontroller.DeclaredKubeconfig{Source: managercontroller.SourcePath, Path: path},
		nil, nil)
	require.NoError(t, err)

	raw, err := source.Kubeconfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, theKubeconfigDoc, string(raw))
}

func TestAKindDeclarationPicksTheAdapterThatAsksTheBinaryForThatCluster(t *testing.T) {
	t.Parallel()

	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().
		Run(context.Background(), "", "kind", "get", "kubeconfig", "--name", theKindCluster).
		Return(execadapter.Result{Stdout: theKubeconfigDoc}, nil)

	source, err := managercontroller.NewKubeconfigSource(
		managercontroller.DeclaredKubeconfig{
			Source: managercontroller.SourceKind, Cluster: theKindCluster,
		},
		nil, runner)
	require.NoError(t, err)

	raw, err := source.Kubeconfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, theKubeconfigDoc, string(raw))
}

func TestATalosDeclarationPicksTheNodeAndHandsItTheSecretTheDeclaredVariableHolds(t *testing.T) {
	t.Setenv(theVariable, "/home/a-person/talosconfig")

	talos := managercontrollermock.NewMockTalosKubeconfig(t)
	talos.EXPECT().
		Kubeconfig(context.Background(), theNode, theEndpoint,
			citypes.Secret("/home/a-person/talosconfig")).
		Return([]byte(theKubeconfigDoc), nil)

	source, err := managercontroller.NewKubeconfigSource(declaredTalosSource(), talos, nil)
	require.NoError(t, err)

	raw, err := source.Kubeconfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, theKubeconfigDoc, string(raw))
}

func TestATalosDeclarationWhoseVariableHoldsNothingIsRefusedNamingTheNodeAndTheVariable(t *testing.T) {
	t.Setenv(theVariable, "")

	source, err := managercontroller.NewKubeconfigSource(declaredTalosSource(), nil, nil)
	require.NoError(t, err)

	_, err = source.Kubeconfig(context.Background())
	require.Error(t, err)
	assert.Equal(t,
		"reading the client configuration of node "+theNode+": nothing is set in "+theVariable+
			", and spec.kubeconfig.talos.talosconfigEnv names it. export it before reconciling",
		err.Error())
}

func TestANodeThatRefusesToHandBackAKubeconfigIsReportedAsAnErrorNamingTheNode(t *testing.T) {
	t.Setenv(theVariable, "/home/a-person/talosconfig")

	talos := managercontrollermock.NewMockTalosKubeconfig(t)
	talos.EXPECT().
		Kubeconfig(context.Background(), theNode, theEndpoint,
			citypes.Secret("/home/a-person/talosconfig")).
		Return(nil, errors.New("connection refused"))

	source, err := managercontroller.NewKubeconfigSource(declaredTalosSource(), talos, nil)
	require.NoError(t, err)

	_, err = source.Kubeconfig(context.Background())
	require.Error(t, err)
	assert.Equal(t,
		"reading the kubeconfig of node "+theNode+": connection refused", err.Error())
}

func TestADeclarationNamingNoSourceIsRefusedByThePickerRatherThanHandingBackANilSource(t *testing.T) {
	t.Parallel()

	_, err := managercontroller.NewKubeconfigSource(managercontroller.DeclaredKubeconfig{}, nil, nil)
	require.Error(t, err)
	assert.Equal(t,
		`building the cluster credential source: the declaration names source "", `+
			"and the cluster credential comes from path or talos or kind", err.Error())
}

func declaredTalosSource() managercontroller.DeclaredKubeconfig {
	return managercontroller.DeclaredKubeconfig{
		Source:         managercontroller.SourceTalos,
		Node:           theNode,
		Endpoint:       theEndpoint,
		TalosconfigEnv: theVariable,
	}
}
