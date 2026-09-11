package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/machineconfigcontroller"
)

func handlersOverTheRealFilesystem() Handlers {
	return newHandlers(machineconfigcontroller.New(fsadapter.New()))
}

func TestTheDeclareToolCarriesTheRootAndTheSpecIntoTheResourceItAnswers(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "controlplane.yaml"), []byte("version: v1alpha1\n"), 0o600))

	out, err := handlersOverTheRealFilesystem().Declare(context.Background(), DeclareInput{
		Root: root,
		Spec: Spec{"nodes": []any{map[string]any{
			"name": "t0-controlplane", "address": "192.168.1.10", "configFile": "controlplane.yaml",
		}}},
	})

	require.NoError(t, err)
	require.Len(t, out.Resources, 1)
	assert.Equal(t, "machine-config", out.Resources[0].Kind)
	assert.Equal(t, "t0-controlplane", out.Resources[0].Name)
	assert.Equal(t, "192.168.1.10", out.Resources[0].Spec["node"])
	assert.Equal(t, "version: v1alpha1\n", out.Resources[0].Spec["config"])
}

func TestTheDeclareToolAnswersTheControllersRefusalAndNoResources(t *testing.T) {
	t.Parallel()

	out, err := handlersOverTheRealFilesystem().
		Declare(context.Background(), DeclareInput{Spec: Spec{}})

	require.ErrorIs(t, err, machineconfigcontroller.ErrNodes)
	assert.Nil(t, out)
}

func TestThePublishToolRefusesByNameAndPublishesNothing(t *testing.T) {
	t.Parallel()

	out, err := handlersOverTheRealFilesystem().
		Publish(context.Background(), ArtifactInput{Revision: "3dd48e96ed7e", Version: "v0.1.0"})

	require.ErrorIs(t, err, machineconfigcontroller.ErrNeverPublishes)
	assert.Nil(t, out)
}
