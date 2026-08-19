package computecontroller_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/computecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/execadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

type stubHarvester struct {
	result *citypes.ForgeResult
	err    error
	dirs   []string
}

func (s *stubHarvester) Harvest(dir string) (*citypes.ForgeResult, error) {
	s.dirs = append(s.dirs, dir)

	return s.result, s.err
}

func passing() execadapter.Result { return execadapter.Result{Stdout: "ok\n"} }

func TestForgeTargetRunsInTheNamedRepo(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().
		Run(mock.Anything, "/work/golden-rust", "forge", "test-all").
		Return(passing(), nil).Once()

	out, err := computecontroller.New(runner, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "build", Forge: "test-all", In: []string{"golden-rust"}}},
	})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPassed, out.Status)
	require.Contains(t, out.Output, "$ forge test-all (in /work/golden-rust)")
}

func TestNoRepoMeansTheRoot(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, "/work", "forge-ci", "apply").Return(passing(), nil).Once()

	out, err := computecontroller.New(runner, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "self", ForgeCI: "apply"}},
	})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPassed, out.Status)
}

func TestACheckoutPathWins(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, "/checkouts/abc/golden-rust", "forge", "test-all").
		Return(passing(), nil).Once()

	_, err := computecontroller.New(runner, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Repos:   []citypes.RepoCheckout{{Name: "golden-rust", Path: "/checkouts/abc/golden-rust"}},
		Targets: []citypes.Target{{Alias: "build", Forge: "test-all", In: []string{"golden-rust"}}},
	})
	require.NoError(t, err)
}

func TestAFailingTestIsNotAnError(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, mock.Anything, "forge", "test-all").
		Return(execadapter.Result{ExitCode: 1, Stderr: "two tests failed\n"}, nil).Once()

	out, err := computecontroller.New(runner, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "build", Forge: "test-all"}},
	})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusFailed, out.Status)
	require.Contains(t, out.Message, `target "build" exited 1`)
	require.Contains(t, out.Output, "two tests failed")
}

func TestABrokenRunnerIsAnError(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(execadapter.Result{}, errBoom).Once()

	_, err := computecontroller.New(runner, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "build", Forge: "test-all"}},
	})
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), `running target "build"`)
}

func TestAFailedTargetStopsTheRest(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, mock.Anything, "forge", "one").
		Return(execadapter.Result{ExitCode: 2}, nil).Once()

	out, err := computecontroller.New(runner, nil).Run(context.Background(), citypes.RunInput{
		Root: "/work",
		Targets: []citypes.Target{
			{Alias: "first", Forge: "one"},
			{Alias: "second", Forge: "two"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusFailed, out.Status)
}

func TestParamsAreTemplatedIntoTheTarget(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().
		Run(mock.Anything, "/work", "forge", "run", "deploy", "--region", "eu-west-1", "--cell", "a").
		Return(passing(), nil).Once()

	_, err := computecontroller.New(runner, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Params:  map[string]string{"region": "eu-west-1", "cell": "a"},
		Targets: []citypes.Target{{Alias: "deploy", Forge: "run deploy --region {{.Params.region}} --cell {{.Params.cell}}"}},
	})
	require.NoError(t, err)
}

func TestAMissingParamIsAnError(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)

	_, err := computecontroller.New(runner, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "deploy", Forge: "run deploy --cell {{.Params.cell}}"}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), `expanding target "deploy"`)
}

func TestABrokenTemplateIsAnError(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)

	_, err := computecontroller.New(runner, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "deploy", Forge: "run {{.Params."}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), `expanding target "deploy"`)
}

func TestATargetNeedsExactlyOneKindOfWork(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)

	for _, target := range []citypes.Target{
		{Alias: "neither"},
		{Alias: "both", Forge: "a", ForgeCI: "b"},
	} {
		_, err := computecontroller.New(runner, nil).Run(context.Background(), citypes.RunInput{
			Root: "/work", Targets: []citypes.Target{target},
		})
		require.ErrorIs(t, err, computecontroller.ErrTarget)
	}
}

func TestNoTargetsIsAnError(t *testing.T) {
	_, err := computecontroller.New(execadaptermock.NewMockRunner(t), nil).
		Run(context.Background(), citypes.RunInput{Root: "/work"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no targets given")
}

func TestForgeResultsAreHarvestedAndMerged(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, mock.Anything, "forge", "test-all").
		Return(passing(), nil).Twice()

	h := &stubHarvester{result: &citypes.ForgeResult{
		Artifacts:   []forge.Artifact{{Name: "bin"}},
		TestReports: []forge.TestReport{{Stage: "unit", Status: "passed"}},
	}}

	out, err := computecontroller.New(runner, h).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "build", Forge: "test-all", In: []string{"a", "b"}}},
	})
	require.NoError(t, err)
	require.NotNil(t, out.Forge)
	require.Len(t, out.Forge.Artifacts, 2)
	require.Len(t, out.Forge.TestReports, 2)
	require.Equal(t, []string{filepath.Join("/work", "a"), filepath.Join("/work", "b")}, h.dirs)
}

func TestNothingToHarvestIsFine(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, mock.Anything, "forge", "test-all").Return(passing(), nil).Once()

	out, err := computecontroller.New(runner, &stubHarvester{}).Run(context.Background(), citypes.RunInput{
		Root: "/work", Targets: []citypes.Target{{Alias: "build", Forge: "test-all"}},
	})
	require.NoError(t, err)
	require.Nil(t, out.Forge)
}

func TestAHarvestFailureIsAnError(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, mock.Anything, "forge", "test-all").Return(passing(), nil).Once()

	_, err := computecontroller.New(runner, &stubHarvester{err: errBoom}).
		Run(context.Background(), citypes.RunInput{
			Root: "/work", Targets: []citypes.Target{{Alias: "build", Forge: "test-all"}},
		})
	require.ErrorIs(t, err, errBoom)
}

func TestAForgeCITargetIsNotHarvested(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().Run(mock.Anything, mock.Anything, "forge-ci", "apply").Return(passing(), nil).Once()

	h := &stubHarvester{result: &citypes.ForgeResult{Artifacts: []forge.Artifact{{Name: "x"}}}}

	out, err := computecontroller.New(runner, h).Run(context.Background(), citypes.RunInput{
		Root: "/work", Targets: []citypes.Target{{Alias: "self", ForgeCI: "apply"}},
	})
	require.NoError(t, err)
	require.Nil(t, out.Forge)
	require.Empty(t, h.dirs)
}

func TestComputeDeclaresNoResources(t *testing.T) {
	out, err := computecontroller.New(nil, nil).Declare(nil)
	require.NoError(t, err)
	require.Empty(t, out.Resources)
}
