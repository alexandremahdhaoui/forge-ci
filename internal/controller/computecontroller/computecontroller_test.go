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

// The compatibility guarantee at the engine, not just in the graph: with no
// needs declared, the dirs run one at a time in the order given. This asserts
// the sequence rather than the set, because that is what changed.
func TestWithNoNeedsTheDirsRunOneAtATimeInOrder(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)

	var seen []string

	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge", "test-all").
		RunAndReturn(func(_ context.Context, dir string, _ map[string]string, _ string, _ ...string) (execadapter.Result, error) {
			seen = append(seen, dir)

			return passing(), nil
		}).Times(3)

	out, err := computecontroller.New(runner, nil, nil).Run(context.Background(), citypes.RunInput{
		Root: "/work",
		Repos: []citypes.RepoCheckout{
			{Name: "c"}, {Name: "a"}, {Name: "b"},
		},
		Targets: []citypes.Target{{Alias: "build", Forge: "test-all", In: []string{"c", "a", "b"}}},
	})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPassed, out.Status)
	require.Equal(t, []string{"/work/c", "/work/a", "/work/b"}, seen,
		"a pipeline that declares nothing must run exactly as it did before dependencies existed")
}

// One edge, and the rest of the wave overlaps. The runner blocks until every
// member of the wave has entered it, so the test passes only if they really
// are concurrent - a sequential implementation deadlocks and fails on the
// timeout rather than passing quietly.
func TestAWaveRunsItsMembersAtOnce(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)

	var (
		entered = make(chan string, 3)
		release = make(chan struct{})
		last    = make(chan string, 1)
	)

	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge", "test-all").
		RunAndReturn(func(_ context.Context, dir string, _ map[string]string, _ string, _ ...string) (execadapter.Result, error) {
			if dir == "/work/last" {
				last <- dir

				return passing(), nil
			}

			entered <- dir
			<-release

			return passing(), nil
		}).Times(4)

	done := make(chan citypes.RunOutput, 1)

	go func() {
		out, err := computecontroller.New(runner, nil, nil).Run(context.Background(), citypes.RunInput{
			Root: "/work",
			Repos: []citypes.RepoCheckout{
				{Name: "one"}, {Name: "two"}, {Name: "three"},
				{Name: "last", Needs: []string{"one", "two", "three"}},
			},
			Targets: []citypes.Target{{
				Alias: "build", Forge: "test-all",
				In: []string{"one", "two", "three", "last"},
			}},
		})
		require.NoError(t, err)
		done <- out
	}()

	// All three are inside the runner before any of them is let go, which no
	// sequential loop can manage.
	for range 3 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("the wave did not run its members at once")
		}
	}

	require.Empty(t, last, "the repo that needs the wave must not have started")
	close(release)

	select {
	case out := <-done:
		require.Equal(t, citypes.StatusPassed, out.Status)
		require.Len(t, last, 1, "the repo that needs the wave runs after it")
	case <-time.After(5 * time.Second):
		t.Fatal("the run did not finish")
	}
}

// A wave finishes even when one of its members fails, so a red run names
// every repo that broke rather than the one that broke first. The wave after
// it does not start.
func TestAFailedWaveFinishesAndStopsTheNext(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)

	runner.EXPECT().RunEnv(mock.Anything, "/work/one", mock.Anything, "forge", "test-all").
		Return(execadapter.Result{ExitCode: 1, Stdout: "one broke\n"}, nil).Once()
	runner.EXPECT().RunEnv(mock.Anything, "/work/two", mock.Anything, "forge", "test-all").
		Return(execadapter.Result{ExitCode: 1, Stdout: "two broke\n"}, nil).Once()

	out, err := computecontroller.New(runner, nil, nil).Run(context.Background(), citypes.RunInput{
		Root: "/work",
		Repos: []citypes.RepoCheckout{
			{Name: "one"}, {Name: "two"},
			{Name: "last", Needs: []string{"one", "two"}},
		},
		Targets: []citypes.Target{{
			Alias: "build", Forge: "test-all", In: []string{"one", "two", "last"},
		}},
	})
	require.NoError(t, err, "a failing build is a failed run and never an error")
	require.Equal(t, citypes.StatusFailed, out.Status)
	require.Contains(t, out.Output, "one broke")
	require.Contains(t, out.Output, "two broke", "the whole wave is reported, not just the first failure")
}

// A cycle among the repos a target names is machinery that cannot run, not a
// build that failed.
func TestACycleIsAnError(t *testing.T) {
	out, err := computecontroller.New(execadaptermock.NewMockRunner(t), nil, nil).
		Run(context.Background(), citypes.RunInput{
			Root: "/work",
			Repos: []citypes.RepoCheckout{
				{Name: "one", Needs: []string{"two"}},
				{Name: "two", Needs: []string{"one"}},
			},
			Targets: []citypes.Target{{Alias: "build", Forge: "test-all", In: []string{"one", "two"}}},
		})
	require.ErrorIs(t, err, citypes.ErrCycle)
	require.Empty(t, out.Status)
}
