package gatecontroller_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/gatecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/fsadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

func passingRun() citypes.Run { return citypes.Run{Status: citypes.StatusPassed} }

func TestAFailedSubstageIsNeverApproved(t *testing.T) {
	c := gatecontroller.New(fsadapter.New(), "approve")

	got, err := c.Evaluate(citypes.GateInput{Run: citypes.Run{Status: citypes.StatusFailed}})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusFailed, got.Status)
	require.Equal(t, "approve", got.Alias)
	require.Contains(t, got.Message, "nothing to approve")
}

func TestWithoutAnApprovalPathTheGateWaits(t *testing.T) {
	c := gatecontroller.New(fsadapter.New(), "approve")

	got, err := c.Evaluate(citypes.GateInput{Run: passingRun()})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPending, got.Status)
	require.Contains(t, got.Message, "set spec.approvalPath")
}

func TestAnExistingApprovalFilePasses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approved")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	c := gatecontroller.New(fsadapter.New(), "approve")

	got, err := c.Evaluate(citypes.GateInput{
		Run: passingRun(), Spec: map[string]any{"approvalPath": path},
	})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPassed, got.Status)
	require.Contains(t, got.Message, path)
}

func TestAMissingApprovalFileWaitsAndSaysHow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approved")

	c := gatecontroller.New(fsadapter.New(), "approve")

	got, err := c.Evaluate(citypes.GateInput{
		Run: passingRun(), Spec: map[string]any{"approvalPath": path},
	})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPending, got.Status)
	require.Contains(t, got.Message, "create "+path)
}

func TestAFilesystemFailureIsAnError(t *testing.T) {
	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().Exists(mock.Anything).Return(false, errBoom).Once()

	_, err := gatecontroller.New(fs, "approve").Evaluate(citypes.GateInput{
		Run: passingRun(), Spec: map[string]any{"approvalPath": "/x"},
	})
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), "checking approval at /x")
}

func TestGateDeclaresNoResources(t *testing.T) {
	out, err := gatecontroller.New(fsadapter.New(), "approve").Declare(nil)
	require.NoError(t, err)
	require.Empty(t, out.Resources)
}
