package kubeconfigadapter_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/kubeconfigadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/execadaptermock"
)

const (
	theCluster = "a-cluster"

	theDocument = "apiVersion: v1\nkind: Config\n"
)

func runnerAnswering(t *testing.T, res execadapter.Result, err error) *execadaptermock.MockRunner {
	t.Helper()

	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().
		Run(context.Background(), "", "kind", "get", "kubeconfig", "--name", theCluster).
		Return(res, err)

	return runner
}

func TestAFileSourceHandsBackEveryByteTheKubeconfigFileHolds(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(path, []byte(theDocument), 0o600))

	raw, err := kubeconfigadapter.NewFile(path).Kubeconfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, theDocument, string(raw))
}

func TestAFileSourceNamingNoFileOnDiskRefusesByNameAndNamesThePath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nothing-is-here")

	_, err := kubeconfigadapter.NewFile(path).Kubeconfig(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the kubeconfig file at "+path)
}

func TestAKindSourceHandsBackWhatTheBinaryWroteOnStdout(t *testing.T) {
	t.Parallel()

	runner := runnerAnswering(t, execadapter.Result{Stdout: theDocument}, nil)

	raw, err := kubeconfigadapter.NewKind(runner, theCluster).Kubeconfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, theDocument, string(raw))
}

func TestAKindSourceWhoseBinaryExitedNonZeroRefusesNamingTheClusterAndCarryingItsStderr(t *testing.T) {
	t.Parallel()

	runner := runnerAnswering(t,
		execadapter.Result{ExitCode: 1, Stderr: "no kind clusters found\n"}, nil)

	_, err := kubeconfigadapter.NewKind(runner, theCluster).Kubeconfig(context.Background())
	require.Error(t, err)
	assert.Equal(t,
		`exporting the kubeconfig of cluster `+theCluster+
			`: it exited 1 saying "no kind clusters found"`, err.Error())
}

func TestAKindSourceThatWroteNothingRefusesNamingTheClusterInsteadOfHandingBackAnEmptyCredential(t *testing.T) {
	t.Parallel()

	runner := runnerAnswering(t, execadapter.Result{Stdout: "  \n"}, nil)

	_, err := kubeconfigadapter.NewKind(runner, theCluster).Kubeconfig(context.Background())
	require.Error(t, err)
	assert.Equal(t,
		"exporting the kubeconfig of cluster "+theCluster+": it wrote nothing", err.Error())
}

func TestAKindSourceWhoseBinaryNeverRanRefusesNamingTheClusterAndCarryingTheCause(t *testing.T) {
	t.Parallel()

	runner := runnerAnswering(t, execadapter.Result{}, errors.New("executable file not found in $PATH"))

	_, err := kubeconfigadapter.NewKind(runner, theCluster).Kubeconfig(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exporting the kubeconfig of cluster "+theCluster)
	assert.Contains(t, err.Error(), "executable file not found in $PATH")
}

func TestWritingTheCredentialPutsItBesideTheDirectoryUnderTheNameKubeconfigAndOnlyItsOwnerReadsIt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	path, err := kubeconfigadapter.Write(dir, []byte(theDocument))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "kubeconfig"), path)

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, theDocument, string(raw))

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestWritingTheCredentialIntoADirectoryThatIsNotThereRefusesNamingThePathItTried(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "nothing-is-here")

	_, err := kubeconfigadapter.Write(dir, []byte(theDocument))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "writing the kubeconfig to "+filepath.Join(dir, "kubeconfig"))
}
