package computecontroller_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

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
	since  time.Time
}

func (s *stubHarvester) Harvest(dir string, since time.Time) (*citypes.ForgeResult, error) {
	s.dirs = append(s.dirs, dir)
	s.since = since

	return s.result, s.err
}

func passing() execadapter.Result { return execadapter.Result{Stdout: "ok\n"} }

func TestForgeTargetRunsInTheNamedRepo(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().
		RunEnv(mock.Anything, "/work/golden-rust", mock.Anything, "forge", "test-all").
		Return(passing(), nil).Once()

	out, err := computecontroller.New(runner, nil, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "build", Forge: "test-all", In: []string{"golden-rust"}}},
	})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPassed, out.Status)
	require.Contains(t, out.Output, "$ forge test-all (in /work/golden-rust)")
}

func TestNoRepoMeansTheRoot(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, "/work", mock.Anything, "forge-ci", "apply").Return(passing(), nil).Once()

	out, err := computecontroller.New(runner, nil, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "self", ForgeCI: "apply"}},
	})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPassed, out.Status)
}

func TestACheckoutPathWins(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, "/checkouts/abc/golden-rust", mock.Anything, "forge", "test-all").
		Return(passing(), nil).Once()

	_, err := computecontroller.New(runner, nil, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Repos:   []citypes.RepoCheckout{{Name: "golden-rust", Path: "/checkouts/abc/golden-rust"}},
		Targets: []citypes.Target{{Alias: "build", Forge: "test-all", In: []string{"golden-rust"}}},
	})
	require.NoError(t, err)
}

func TestAFailingTestIsNotAnError(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge", "test-all").
		Return(execadapter.Result{ExitCode: 1, Stderr: "two tests failed\n"}, nil).Once()

	out, err := computecontroller.New(runner, nil, nil).Run(context.Background(), citypes.RunInput{
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
	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(execadapter.Result{}, errBoom).Once()

	_, err := computecontroller.New(runner, nil, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "build", Forge: "test-all"}},
	})
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), `running target "build"`)
}

func TestAFailedTargetStopsTheRest(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge", "one").
		Return(execadapter.Result{ExitCode: 2}, nil).Once()

	out, err := computecontroller.New(runner, nil, nil).Run(context.Background(), citypes.RunInput{
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
		RunEnv(mock.Anything, "/work", mock.Anything, "forge", "run", "deploy", "--region", "eu-west-1", "--cell", "a").
		Return(passing(), nil).Once()

	_, err := computecontroller.New(runner, nil, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Params:  map[string]string{"region": "eu-west-1", "cell": "a"},
		Targets: []citypes.Target{{Alias: "deploy", Forge: "run deploy --region {{.Params.region}} --cell {{.Params.cell}}"}},
	})
	require.NoError(t, err)
}

func TestAMissingParamIsAnError(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)

	_, err := computecontroller.New(runner, nil, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "deploy", Forge: "run deploy --cell {{.Params.cell}}"}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), `expanding target "deploy"`)
}

func TestABrokenTemplateIsAnError(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)

	_, err := computecontroller.New(runner, nil, nil).Run(context.Background(), citypes.RunInput{
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
		_, err := computecontroller.New(runner, nil, nil).Run(context.Background(), citypes.RunInput{
			Root: "/work", Targets: []citypes.Target{target},
		})
		require.ErrorIs(t, err, computecontroller.ErrTarget)
	}
}

func TestNoTargetsIsAnError(t *testing.T) {
	_, err := computecontroller.New(execadaptermock.NewMockRunner(t), nil, nil).
		Run(context.Background(), citypes.RunInput{Root: "/work"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no targets given")
}

func TestForgeResultsAreHarvestedAndMerged(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge", "test-all").
		Return(passing(), nil).Twice()

	h := &stubHarvester{result: &citypes.ForgeResult{
		Artifacts:   []forge.Artifact{{Name: "bin"}},
		TestReports: []forge.TestReport{{Stage: "unit", Status: "passed"}},
	}}

	out, err := computecontroller.New(runner, h, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "build", Forge: "test-all", In: []string{"a", "b"}}},
	})
	require.NoError(t, err)
	require.NotNil(t, out.Forge)
	require.Len(t, out.Forge.Artifacts, 2)
	require.Len(t, out.Forge.TestReports, 2)
	require.Equal(t, []string{filepath.Join("/work", "a"), filepath.Join("/work", "b")}, h.dirs)
}

// The revision reaches every target as FORGE_CI_REVISION, so an instance's
// build can stamp the tuple it was proven with.
func TestTheRevisionReachesTheTargetEnvironment(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().
		RunEnv(mock.Anything, mock.Anything,
			map[string]string{"FORGE_CI_REVISION": "abc123def456"}, "forge", "test-all").
		Return(passing(), nil).Once()

	_, err := computecontroller.New(runner, nil, nil).Run(context.Background(), citypes.RunInput{
		Root:     "/work",
		Revision: "abc123def456",
		Targets:  []citypes.Target{{Alias: "build", Forge: "test-all"}},
	})
	require.NoError(t, err)
}

// A store records locations relative to its own repo, which means nothing
// to the release side; harvested artifacts are rebased onto the root.
func TestHarvestedArtifactLocationsAreRebasedOntoTheRoot(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge", "test-all").
		Return(passing(), nil).Once()

	h := &stubHarvester{result: &citypes.ForgeResult{
		Artifacts: []forge.Artifact{
			{Name: "bin", Location: "build/dist/bin_linux_amd64"},
			{Name: "img", Location: "ghcr.io/x/img:v1"},
			{Name: "empty", Location: ""},
		},
	}}

	out, err := computecontroller.New(runner, h, nil).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "build", Forge: "test-all", In: []string{"member"}}},
	})
	require.NoError(t, err)
	require.Equal(t, "member/build/dist/bin_linux_amd64", out.Forge.Artifacts[0].Location)
	require.Equal(t, "ghcr.io/x/img:v1", out.Forge.Artifacts[1].Location, "URLs pass through")
	require.Equal(t, "", out.Forge.Artifacts[2].Location, "nothing is invented for an empty location")
}

func TestNothingToHarvestIsFine(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge", "test-all").Return(passing(), nil).Once()

	out, err := computecontroller.New(runner, &stubHarvester{}, nil).Run(context.Background(), citypes.RunInput{
		Root: "/work", Targets: []citypes.Target{{Alias: "build", Forge: "test-all"}},
	})
	require.NoError(t, err)
	require.Nil(t, out.Forge)
}

func TestAHarvestFailureIsAnError(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge", "test-all").Return(passing(), nil).Once()

	_, err := computecontroller.New(runner, &stubHarvester{err: errBoom}, nil).
		Run(context.Background(), citypes.RunInput{
			Root: "/work", Targets: []citypes.Target{{Alias: "build", Forge: "test-all"}},
		})
	require.ErrorIs(t, err, errBoom)
}

func TestAForgeCITargetIsNotHarvested(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge-ci", "apply").Return(passing(), nil).Once()

	h := &stubHarvester{result: &citypes.ForgeResult{Artifacts: []forge.Artifact{{Name: "x"}}}}

	out, err := computecontroller.New(runner, h, nil).Run(context.Background(), citypes.RunInput{
		Root: "/work", Targets: []citypes.Target{{Alias: "self", ForgeCI: "apply"}},
	})
	require.NoError(t, err)
	require.Nil(t, out.Forge)
	require.Empty(t, h.dirs)
}

func TestComputeDeclaresNoResources(t *testing.T) {
	out, err := computecontroller.New(nil, nil, nil).Declare(nil)
	require.NoError(t, err)
	require.Empty(t, out.Resources)
}

func TestTheHarvesterIsToldWhenTheRunStarted(t *testing.T) {
	started := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge", "test-all").Return(passing(), nil).Once()

	h := &stubHarvester{result: &citypes.ForgeResult{}}

	_, err := computecontroller.New(runner, h, func() time.Time { return started }).
		Run(context.Background(), citypes.RunInput{
			Root: "/work", Targets: []citypes.Target{{Alias: "build", Forge: "test-all"}},
		})
	require.NoError(t, err)
	require.Equal(t, started, h.since)
}
