package computecontroller_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/forgeadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/computecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/execadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/fsadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/gitadaptermock"
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

func compute(
	t *testing.T, runner execadapter.Runner, harvester forgeadapter.Harvester, fs fsadapter.FS, git gitadapter.Git,
) *computecontroller.Controller {
	t.Helper()

	ctrl, err := computecontroller.New(runner, harvester, fs, git, time.Now)
	require.NoError(t, err)

	return ctrl
}

func passing() execadapter.Result { return execadapter.Result{Stdout: "ok\n"} }

func everyCheckoutHasAnEnvrc(t *testing.T) *fsadaptermock.MockFS {
	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().IsDir(mock.Anything).Return(true, nil).Maybe()
	fs.EXPECT().Exists(mock.Anything).Return(true, nil).Maybe()

	return fs
}

func realGit() gitadapter.Git { return gitadapter.New(execadapter.New()) }

func aGitCheckout(t *testing.T, gitignore string) string {
	t.Helper()

	dir := t.TempDir()
	res, err := execadapter.New().Run(context.Background(), dir, "git", "init", "-q")
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, res.Stderr)

	if gitignore != "" {
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(gitignore), 0o600))
	}

	return dir
}

func runningBuildIn(t *testing.T, dir string) (*execadaptermock.MockRunner, citypes.RunInput) {
	t.Helper()

	return execadaptermock.NewMockRunner(t), citypes.RunInput{
		Root:    dir,
		Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}}},
	}
}

func TestACheckoutWhoseGitIgnoresEnvrcGetsAnEmptyOneBeforeItsTargetRuns(t *testing.T) {
	dir := aGitCheckout(t, "/.envrc\n")

	runner, in := runningBuildIn(t, dir)
	runner.EXPECT().RunEnv(mock.Anything, dir, mock.Anything, "forge", "test-all").
		Run(func(context.Context, string, map[string]string, string, ...string) {
			content, err := os.ReadFile(filepath.Join(dir, ".envrc"))
			require.NoError(t, err, "the .envrc exists when the target starts")
			require.Empty(t, content)
		}).
		Return(passing(), nil).Once()

	out, err := compute(t, runner, &stubHarvester{}, fsadapter.New(), realGit()).Run(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPassed, out.Status)
}

func TestAnExistingEnvrcIsNeverOverwritten(t *testing.T) {
	dir := aGitCheckout(t, "")
	envrc := filepath.Join(dir, ".envrc")
	require.NoError(t, os.WriteFile(envrc, []byte("export FORGE_CI_EXAMPLE=1\n"), 0o600))

	runner, in := runningBuildIn(t, dir)
	runner.EXPECT().RunEnv(mock.Anything, dir, mock.Anything, "forge", "test-all").Return(passing(), nil).Once()

	_, err := compute(t, runner, &stubHarvester{}, fsadapter.New(), gitadaptermock.NewMockGit(t)).Run(context.Background(), in)
	require.NoError(t, err)

	content, err := os.ReadFile(envrc)
	require.NoError(t, err)
	require.Equal(t, "export FORGE_CI_EXAMPLE=1\n", string(content))
}

func TestADirectoryThatIsNoGitRepositoryGetsNoEnvrcAndItsTargetStillRuns(t *testing.T) {
	dir := t.TempDir()

	runner, in := runningBuildIn(t, dir)
	runner.EXPECT().RunEnv(mock.Anything, dir, mock.Anything, "forge", "test-all").Return(passing(), nil).Once()

	out, err := compute(t, runner, &stubHarvester{}, fsadapter.New(), realGit()).Run(context.Background(), in)
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPassed, out.Status)
	require.NoFileExists(t, filepath.Join(dir, ".envrc"))
}

func TestACheckoutWithNoEnvrcWhoseGitDoesNotIgnoreItIsRefusedByNameBeforeItsTargetRuns(t *testing.T) {
	dir := aGitCheckout(t, "")

	runner, in := runningBuildIn(t, dir)

	_, err := compute(t, runner, &stubHarvester{}, fsadapter.New(), realGit()).Run(context.Background(), in)
	require.EqualError(t, err, "running target \"build\" in "+dir+": it holds no .envrc and its git does not ignore .envrc, "+
		"so the engine will not write one git could carry. add /.envrc to its .gitignore")
	require.NoFileExists(t, filepath.Join(dir, ".envrc"))
}

func TestAMissingCheckoutGetsNoEnvrcAndTheRunnerReportsIt(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")

	runner, in := runningBuildIn(t, missing)
	runner.EXPECT().RunEnv(mock.Anything, missing, mock.Anything, "forge", "test-all").
		Return(execadapter.Result{}, errBoom).Once()

	_, err := compute(t, runner, &stubHarvester{}, fsadapter.New(), gitadaptermock.NewMockGit(t)).Run(context.Background(), in)
	require.ErrorIs(t, err, errBoom)
	require.NoDirExists(t, missing, "the engine never creates a checkout to hold an .envrc")
}

func TestAFailureGivingACloneItsEnvrcIsAnErrorNamingTheDir(t *testing.T) {
	cases := map[string]func(fs *fsadaptermock.MockFS, git *gitadaptermock.MockGit){
		"inspecting the dir": func(fs *fsadaptermock.MockFS, _ *gitadaptermock.MockGit) {
			fs.EXPECT().IsDir("/work").Return(false, errBoom).Once()
		},
		"looking for the envrc": func(fs *fsadaptermock.MockFS, _ *gitadaptermock.MockGit) {
			fs.EXPECT().IsDir("/work").Return(true, nil).Once()
			fs.EXPECT().Exists("/work/.envrc").Return(false, errBoom).Once()
		},
		"asking whether it is a repository": func(fs *fsadaptermock.MockFS, git *gitadaptermock.MockGit) {
			fs.EXPECT().IsDir("/work").Return(true, nil).Once()
			fs.EXPECT().Exists("/work/.envrc").Return(false, nil).Once()
			git.EXPECT().IsRepo(mock.Anything, "/work").Return(false, errBoom).Once()
		},
		"asking whether git ignores the envrc": func(fs *fsadaptermock.MockFS, git *gitadaptermock.MockGit) {
			fs.EXPECT().IsDir("/work").Return(true, nil).Once()
			fs.EXPECT().Exists("/work/.envrc").Return(false, nil).Once()
			git.EXPECT().IsRepo(mock.Anything, "/work").Return(true, nil).Once()
			git.EXPECT().IgnoresPath(mock.Anything, "/work", ".envrc").
				Return(false, fmt.Errorf("asking git in /work whether it ignores .envrc: %w", errBoom)).Once()
		},
		"writing the envrc": func(fs *fsadaptermock.MockFS, git *gitadaptermock.MockGit) {
			fs.EXPECT().IsDir("/work").Return(true, nil).Once()
			fs.EXPECT().Exists("/work/.envrc").Return(false, nil).Once()
			git.EXPECT().IsRepo(mock.Anything, "/work").Return(true, nil).Once()
			git.EXPECT().IgnoresPath(mock.Anything, "/work", ".envrc").Return(true, nil).Once()
			fs.EXPECT().WriteFile("/work/.envrc", []byte(nil)).Return(errBoom).Once()
		},
	}

	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			fs := fsadaptermock.NewMockFS(t)
			git := gitadaptermock.NewMockGit(t)
			arrange(fs, git)

			_, err := compute(t, execadaptermock.NewMockRunner(t), &stubHarvester{}, fs, git).Run(context.Background(), citypes.RunInput{
				Root:    "/work",
				Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}}},
			})
			require.ErrorIs(t, err, errBoom)
			require.Contains(t, err.Error(), "/work")
		})
	}
}

func TestForgeTargetRunsInTheNamedRepo(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().
		RunEnv(mock.Anything, "/work/golden-rust", mock.Anything, "forge", "test-all").
		Return(passing(), nil).Once()

	out, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}, In: []string{"golden-rust"}}},
	})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPassed, out.Status)
	require.Contains(t, out.Output, "$ forge test-all (in /work/golden-rust)")
}

func TestSyncConvergesTheWorkspaceBeforeAnyTarget(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)

	var order []string

	record := func(what string) func(context.Context, string, map[string]string, string, ...string) (execadapter.Result, error) {
		return func(context.Context, string, map[string]string, string, ...string) (execadapter.Result, error) {
			order = append(order, what)

			return passing(), nil
		}
	}

	runner.EXPECT().RunEnv(mock.Anything, "/work", mock.Anything, "forge-factory", "sync").
		RunAndReturn(record("sync")).Once()
	runner.EXPECT().RunEnv(mock.Anything, "/work", mock.Anything, "forge-factory", "lock").
		RunAndReturn(record("lock")).Once()
	runner.EXPECT().RunEnv(mock.Anything, "/work/golden-rust", mock.Anything, "forge", "test-all").
		RunAndReturn(record("target")).Once()

	out, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Sync:    true,
		Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}, In: []string{"golden-rust"}}},
	})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPassed, out.Status)
	require.Equal(t, []string{"sync", "lock", "target"}, order,
		"manifests first, then the closure, then the build")
}

func TestAFailingConvergenceFailsTheRunAndStopsIt(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, "/work", mock.Anything, "forge-factory", "sync").
		Return(execadapter.Result{ExitCode: 1, Stderr: "no engine is registered\n"}, nil).Once()

	out, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Sync:    true,
		Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}, In: []string{"golden-rust"}}},
	})
	require.NoError(t, err, "a red convergence is a failed run, never an error")
	require.Equal(t, citypes.StatusFailed, out.Status)
	require.Contains(t, out.Message, "forge-factory sync exited 1")
	require.Contains(t, out.Output, "no engine is registered")
}

func TestNoSyncMeansNoConvergence(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, "/work/golden-rust", mock.Anything, "forge", "test-all").
		Return(passing(), nil).Once()

	out, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}, In: []string{"golden-rust"}}},
	})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPassed, out.Status)
	require.NotContains(t, out.Output, "forge-factory")
}

func TestNoRepoMeansTheRoot(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, "/work", mock.Anything, "forge-ci", "apply").Return(passing(), nil).Once()

	out, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "self", Binary: "forge-ci", Args: []string{"apply"}}},
	})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPassed, out.Status)
}

func TestACheckoutPathWins(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, "/checkouts/abc/golden-rust", mock.Anything, "forge", "test-all").
		Return(passing(), nil).Once()

	_, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Repos:   []citypes.RepoCheckout{{Name: "golden-rust", Path: "/checkouts/abc/golden-rust"}},
		Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}, In: []string{"golden-rust"}}},
	})
	require.NoError(t, err)
}

func TestAFailingTestIsNotAnError(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge", "test-all").
		Return(execadapter.Result{ExitCode: 1, Stderr: "two tests failed\n"}, nil).Once()

	out, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}}},
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

	_, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}}},
	})
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), `running target "build"`)
}

func TestAFailedTargetStopsTheRest(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge", "one").
		Return(execadapter.Result{ExitCode: 2}, nil).Once()

	out, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root: "/work",
		Targets: []citypes.Target{
			{Alias: "first", Binary: "forge", Args: []string{"one"}},
			{Alias: "second", Binary: "forge", Args: []string{"two"}},
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

	_, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Params:  map[string]string{"region": "eu-west-1", "cell": "a"},
		Targets: []citypes.Target{{Alias: "deploy", Binary: "forge", Args: []string{"run", "deploy", "--region", "{{.Params.region}}", "--cell", "{{.Params.cell}}"}}},
	})
	require.NoError(t, err)
}

func TestAMissingParamIsAnError(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)

	_, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "deploy", Binary: "forge", Args: []string{"run", "deploy", "--cell", "{{.Params.cell}}"}}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), `expanding target "deploy"`)
}

func TestABrokenTemplateIsAnError(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)

	_, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "deploy", Binary: "forge", Args: []string{"run", "{{.Params."}}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), `expanding target "deploy"`)
}

func TestATargetNeedsABinary(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)

	for _, target := range []citypes.Target{
		{Alias: "neither"},
		{Alias: "blank", Binary: "  ", Args: []string{"a"}},
	} {
		_, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
			Root: "/work", Targets: []citypes.Target{target},
		})
		require.ErrorIs(t, err, computecontroller.ErrTarget)
	}
}

func TestNoTargetsIsAnError(t *testing.T) {
	_, err := compute(t, execadaptermock.NewMockRunner(t), &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).
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

	out, err := compute(t, runner, h, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}, In: []string{"a", "b"}}},
	})
	require.NoError(t, err)
	require.NotNil(t, out.Forge)
	require.Len(t, out.Forge.Artifacts, 2)
	require.Len(t, out.Forge.TestReports, 2)
	require.Equal(t, []string{filepath.Join("/work", "a"), filepath.Join("/work", "b")}, h.dirs)
}

func TestTheRevisionReachesTheTargetEnvironment(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().
		RunEnv(mock.Anything, mock.Anything,
			map[string]string{"FORGE_CI_REVISION": "abc123def456"}, "forge", "test-all").
		Return(passing(), nil).Once()

	_, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root:     "/work",
		Revision: "abc123def456",
		Targets:  []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}}},
	})
	require.NoError(t, err)
}

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

	out, err := compute(t, runner, h, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}, In: []string{"member"}}},
	})
	require.NoError(t, err)
	require.Equal(t, "member/build/dist/bin_linux_amd64", out.Forge.Artifacts[0].Location)
	require.Equal(t, "ghcr.io/x/img:v1", out.Forge.Artifacts[1].Location, "URLs pass through")
	require.Equal(t, "", out.Forge.Artifacts[2].Location, "nothing is invented for an empty location")
}

func TestNothingToHarvestIsFine(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge", "test-all").Return(passing(), nil).Once()

	out, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root: "/work", Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}}},
	})
	require.NoError(t, err)
	require.Nil(t, out.Forge)
}

func TestAHarvestFailureIsAnError(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge", "test-all").Return(passing(), nil).Once()

	_, err := compute(t, runner, &stubHarvester{err: errBoom}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).
		Run(context.Background(), citypes.RunInput{
			Root: "/work", Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}}},
		})
	require.ErrorIs(t, err, errBoom)
}

func TestEveryTargetIsHarvested(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge-ci", "apply").Return(passing(), nil).Once()

	h := &stubHarvester{result: &citypes.ForgeResult{Artifacts: []forge.Artifact{{Name: "x"}}}}

	out, err := compute(t, runner, h, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root: "/work", Targets: []citypes.Target{{Alias: "self", Binary: "forge-ci", Args: []string{"apply"}}},
	})
	require.NoError(t, err)
	require.NotNil(t, out.Forge)
	require.Equal(t, []string{"/work"}, h.dirs)
}

func TestComputeDeclaresNoResources(t *testing.T) {
	out, err := compute(t, nil, &stubHarvester{}, nil, nil).Declare(nil)
	require.NoError(t, err)
	require.Empty(t, out.Resources)
}

func TestANilHarvesterIsRefusedByNameAtConstruction(t *testing.T) {
	ctrl, err := computecontroller.New(
		execadaptermock.NewMockRunner(t), nil, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t), time.Now)
	require.EqualError(t, err, "building the compute controller: it was handed no harvester, "+
		"and every run reads what forge built through one")
	require.Nil(t, ctrl)
}

func TestANilClockIsRefusedByNameAtConstruction(t *testing.T) {
	ctrl, err := computecontroller.New(
		execadaptermock.NewMockRunner(t), &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t), nil)
	require.EqualError(t, err, "building the compute controller: it was handed no clock, "+
		"and every run tells the harvester when it started through one")
	require.Nil(t, ctrl)
}

func TestTheHarvesterIsToldWhenTheRunStarted(t *testing.T) {
	started := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)

	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge", "test-all").Return(passing(), nil).Once()

	h := &stubHarvester{result: &citypes.ForgeResult{}}

	ctrl, err := computecontroller.New(
		runner, h, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t), func() time.Time { return started })
	require.NoError(t, err)

	_, err = ctrl.Run(context.Background(), citypes.RunInput{
		Root: "/work", Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}}},
	})
	require.NoError(t, err)
	require.Equal(t, started, h.since)
}

func TestWithNoNeedsTheDirsRunOneAtATimeInOrder(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)

	var seen []string

	runner.EXPECT().RunEnv(mock.Anything, mock.Anything, mock.Anything, "forge", "test-all").
		RunAndReturn(func(_ context.Context, dir string, _ map[string]string, _ string, _ ...string) (execadapter.Result, error) {
			seen = append(seen, dir)

			return passing(), nil
		}).Times(3)

	out, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root: "/work",
		Repos: []citypes.RepoCheckout{
			{Name: "c"}, {Name: "a"}, {Name: "b"},
		},
		Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}, In: []string{"c", "a", "b"}}},
	})
	require.NoError(t, err)
	require.Equal(t, citypes.StatusPassed, out.Status)
	require.Equal(t, []string{"/work/c", "/work/a", "/work/b"}, seen,
		"a pipeline that declares nothing must run exactly as it did before dependencies existed")
}

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
		out, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
			Root: "/work",
			Repos: []citypes.RepoCheckout{
				{Name: "one"},
				{Name: "two"},
				{Name: "three"},
				{Name: "last", Needs: []string{"one", "two", "three"}},
			},
			Targets: []citypes.Target{{
				Alias: "build", Binary: "forge", Args: []string{"test-all"},
				In: []string{"one", "two", "three", "last"},
			}},
		})
		require.NoError(t, err)
		done <- out
	}()

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

func TestAFailedWaveFinishesAndStopsTheNext(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)

	runner.EXPECT().RunEnv(mock.Anything, "/work/one", mock.Anything, "forge", "test-all").
		Return(execadapter.Result{ExitCode: 1, Stdout: "one broke\n"}, nil).Once()
	runner.EXPECT().RunEnv(mock.Anything, "/work/two", mock.Anything, "forge", "test-all").
		Return(execadapter.Result{ExitCode: 1, Stdout: "two broke\n"}, nil).Once()

	out, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root: "/work",
		Repos: []citypes.RepoCheckout{
			{Name: "one"},
			{Name: "two"},
			{Name: "last", Needs: []string{"one", "two"}},
		},
		Targets: []citypes.Target{{
			Alias: "build", Binary: "forge", Args: []string{"test-all"}, In: []string{"one", "two", "last"},
		}},
	})
	require.NoError(t, err, "a failing build is a failed run and never an error")
	require.Equal(t, citypes.StatusFailed, out.Status)
	require.Contains(t, out.Output, "one broke")
	require.Contains(t, out.Output, "two broke", "the whole wave is reported, not just the first failure")
}

func TestACycleIsAnError(t *testing.T) {
	out, err := compute(t, execadaptermock.NewMockRunner(t), &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).
		Run(context.Background(), citypes.RunInput{
			Root: "/work",
			Repos: []citypes.RepoCheckout{
				{Name: "one", Needs: []string{"two"}},
				{Name: "two", Needs: []string{"one"}},
			},
			Targets: []citypes.Target{{Alias: "build", Binary: "forge", Args: []string{"test-all"}, In: []string{"one", "two"}}},
		})
	require.ErrorIs(t, err, citypes.ErrCycle)
	require.Empty(t, out.Status)
}

func TestAnArgumentHoldingASpaceStaysOneArgument(t *testing.T) {
	runner := execadaptermock.NewMockRunner(t)
	runner.EXPECT().
		RunEnv(mock.Anything, "/work", mock.Anything, "sh", "-c", "echo one two").
		Return(passing(), nil).Once()

	_, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
		Root:    "/work",
		Targets: []citypes.Target{{Alias: "say", Binary: "sh", Args: []string{"-c", "echo one two"}}},
	})
	require.NoError(t, err)
}

func TestALongOutputIsTruncatedFromTheFrontByTheSpec(t *testing.T) {
	long := strings.Repeat("x", 20000) + "THE INTERESTING PART\n"

	for _, tc := range []struct {
		name  string
		spec  map[string]any
		limit int
	}{
		{name: "default", spec: nil, limit: computecontroller.DefaultOutputLimit},
		{name: "declared", spec: map[string]any{"outputLimit": float64(2048)}, limit: 2048},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := execadaptermock.NewMockRunner(t)
			runner.EXPECT().
				RunEnv(mock.Anything, "/work/repo", mock.Anything, "forge", "test-all").
				Return(execadapter.Result{Stdout: long, ExitCode: 1}, nil)

			out, err := compute(t, runner, &stubHarvester{}, everyCheckoutHasAnEnvrc(t), gitadaptermock.NewMockGit(t)).Run(context.Background(), citypes.RunInput{
				Revision: "abc", Root: "/work", Spec: tc.spec,
				Targets: []citypes.Target{{Alias: "t", Binary: "forge", Args: []string{"test-all"}, In: []string{"repo"}}},
			})
			require.NoError(t, err)

			require.LessOrEqual(t, len(out.Output), tc.limit+len("... earlier output dropped ...\n"))
			require.Contains(t, out.Output, "THE INTERESTING PART")
			require.Contains(t, out.Output, "earlier output dropped")
		})
	}
}
