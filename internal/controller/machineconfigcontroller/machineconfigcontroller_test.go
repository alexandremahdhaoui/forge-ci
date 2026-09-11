package machineconfigcontroller_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/machineconfigcontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/fsadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

func nodeSpec(name, address, configFile string) map[string]any {
	return map[string]any{"name": name, "address": address, "configFile": configFile}
}

func TestOneDeclaredNodeBecomesOneMachineConfigResourceNamedAfterTheNode(t *testing.T) {
	t.Parallel()

	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().
		ReadFile(filepath.Join("/w", "config", "controlplane.yaml")).
		Return([]byte("version: v1alpha1\n"), nil)

	out, err := machineconfigcontroller.New(fs).Declare(citypes.DeclareInput{
		Root: "/w",
		Spec: map[string]any{"nodes": []any{
			nodeSpec("t0-controlplane", "192.168.1.10", "config/controlplane.yaml"),
		}},
	})

	require.NoError(t, err)
	require.Len(t, out.Resources, 1)
	assert.Equal(t, citypes.Resource{
		Kind: "machine-config",
		Name: "t0-controlplane",
		Spec: map[string]any{"node": "192.168.1.10", "config": "version: v1alpha1\n"},
	}, out.Resources[0])
}

func TestEveryDeclaredNodeBecomesItsOwnResourceInTheOrderDeclared(t *testing.T) {
	t.Parallel()

	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().ReadFile(filepath.Join("/w", "a.yaml")).Return([]byte("a"), nil)
	fs.EXPECT().ReadFile(filepath.Join("/w", "b.yaml")).Return([]byte("b"), nil)

	out, err := machineconfigcontroller.New(fs).Declare(citypes.DeclareInput{
		Root: "/w",
		Spec: map[string]any{"nodes": []any{
			nodeSpec("one", "10.0.0.1", "a.yaml"),
			nodeSpec("two", "10.0.0.2", "b.yaml"),
		}},
	})

	require.NoError(t, err)
	require.Len(t, out.Resources, 2)
	assert.Equal(t, "one", out.Resources[0].Name)
	assert.Equal(t, "a", out.Resources[0].Spec["config"])
	assert.Equal(t, "two", out.Resources[1].Name)
	assert.Equal(t, "10.0.0.2", out.Resources[1].Spec["node"])
}

func TestAConfigFileThatCannotBeReadIsAnErrorNamingTheNodeAndTheFile(t *testing.T) {
	t.Parallel()

	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().
		ReadFile(filepath.Join("/w", "missing.yaml")).
		Return(nil, errors.New("no such file"))

	_, err := machineconfigcontroller.New(fs).Declare(citypes.DeclareInput{
		Root: "/w",
		Spec: map[string]any{"nodes": []any{nodeSpec("one", "10.0.0.1", "missing.yaml")}},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "one")
	assert.Contains(t, err.Error(), "missing.yaml")
	assert.Contains(t, err.Error(), "no such file")
}

func TestASpecWithNoNodesIsRefusedByName(t *testing.T) {
	t.Parallel()

	for name, spec := range map[string]map[string]any{
		"no nodes key":   {},
		"an empty list":  {"nodes": []any{}},
		"not a list":     {"nodes": "controlplane"},
		"a nil declared": {"nodes": nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := machineconfigcontroller.New(fsadaptermock.NewMockFS(t)).
				Declare(citypes.DeclareInput{Spec: spec})

			require.ErrorIs(t, err, machineconfigcontroller.ErrNodes)
		})
	}
}

func TestANodeMissingItsNameItsAddressOrItsConfigFileIsRefusedByName(t *testing.T) {
	t.Parallel()

	for want, entry := range map[string]any{
		"name is required":       nodeSpec("", "10.0.0.1", "a.yaml"),
		"address is required":    nodeSpec("one", "", "a.yaml"),
		"configFile is required": nodeSpec("one", "10.0.0.1", ""),
	} {
		t.Run(want, func(t *testing.T) {
			t.Parallel()

			_, err := machineconfigcontroller.New(fsadaptermock.NewMockFS(t)).
				Declare(citypes.DeclareInput{Spec: map[string]any{"nodes": []any{entry}}})

			require.Error(t, err)
			assert.Contains(t, err.Error(), want)
		})
	}
}

func TestANodeThatIsNotAnObjectIsRefusedByItsPosition(t *testing.T) {
	t.Parallel()

	_, err := machineconfigcontroller.New(fsadaptermock.NewMockFS(t)).
		Declare(citypes.DeclareInput{Spec: map[string]any{"nodes": []any{"controlplane"}}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.nodes[0]")
}

func TestANodeFieldThatIsNotAStringIsRefusedByName(t *testing.T) {
	t.Parallel()

	_, err := machineconfigcontroller.New(fsadaptermock.NewMockFS(t)).
		Declare(citypes.DeclareInput{Spec: map[string]any{"nodes": []any{
			map[string]any{"name": "one", "address": 10, "configFile": "a.yaml"},
		}}})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "one")
	assert.Contains(t, err.Error(), "address")
}

func TestPublishRefusesByNameBecauseThisEngineOnlyDeclares(t *testing.T) {
	t.Parallel()

	err := machineconfigcontroller.New(fsadaptermock.NewMockFS(t)).Publish()

	require.ErrorIs(t, err, machineconfigcontroller.ErrNeverPublishes)
	assert.Equal(t,
		"this engine declares machine configs and never publishes: no substage may name it",
		err.Error())
}
