package workflowcontroller_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/workflowcontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/githubadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

// registerSpec reproduces one real pipeline's GitHub surface. The golden
// files under testdata are byte copies of that pipeline's hand-written
// workflows; rendering must match them exactly, because the realized
// files replace them in place.
func registerSpec() workflowcontroller.Spec {
	return workflowcontroller.Spec{
		Repo: "alexandremahdhaoui/golden-register",
		Dir:  "golden-register",
		Ref:  "main",
		Setup: []workflowcontroller.SetupStep{
			{Uses: "actions/setup-go@v5", With: map[string]string{"go-version": "1.26"}},
		},
		Workspace: workflowcontroller.Workspace{
			BootstrapCommand: "go run github.com/alexandremahdhaoui/forge-factory/cmd/forge-factory@7d34e3e bootstrap git@github.com:alexandremahdhaoui/golden-factory.git .",
			ToolchainScript: `(cd forge && go install ./cmd/forge) || go install github.com/alexandremahdhaoui/forge/cmd/forge@latest
(cd forge-factory && go install ./cmd/...)
(cd forge-ci && go install ./cmd/...)
(cd forge-register && go install ./cmd/...)
echo "$HOME/go/bin" >> "$GITHUB_PATH"
`,
		},
		Secrets: []workflowcontroller.SecretSpec{
			{Name: "FORGE_CI_GITHUB_TOKEN", FromEnv: "FORGE_CI_GITHUB_TOKEN"},
			{Name: "FORGE_CI_GITHUB_TOKEN", FromEnv: "FORGE_CI_GITHUB_TOKEN"},
		},
		Workflows: []workflowcontroller.WorkflowSpec{
			{
				Name: "intake", Kind: workflowcontroller.KindCommand,
				Header: `# Scheduled security intake: run the register pipeline so a fresh OSV
# snapshot re-evaluates every track and raises any advisory that is due.
#
# UNEXERCISED: written where Actions cannot run. It needs one secret,
# FORGE_CI_GITHUB_TOKEN - a token that can read every workspace member and push
# to this repo - because the pipeline builds the whole playground
# workspace around this checkout.
`,
				Cron: "17 5 * * *", Job: "apply", StepName: "Run the register pipeline",
				Secret: "FORGE_CI_GITHUB_TOKEN", Push: true, ReportFailure: true,
				Command: "cd golden-register\ntouch .envrc\nforge-ci apply --config forge-ci.yaml --root ..\n",
			},
			{
				Name: "request", Kind: workflowcontroller.KindCommand,
				Header: `# The remote consumer's request door: a repository_dispatch of type
# admission-request files the request into the register's store, where the
# next pipeline run answers it under the same policy as everything else.
# A request is untrusted input whoever files it and however it arrives.
#
# UNEXERCISED: written where Actions cannot run. It needs the same
# FORGE_CI_GITHUB_TOKEN as intake. A consumer fires it with:
#
#   forge-register add go:github.com/x/pkg --reason "..." \
#     --dispatch <owner>/golden-register
`,
				Events: []string{"admission-request"}, Job: "file", StepName: "File the request",
				Secret: "FORGE_CI_GITHUB_TOKEN", ReportFailure: true,
				PayloadEnv: []string{"ecosystem", "package", "track", "version", "requester", "reason"},
				Command: `cd golden-register
forge-register add \
  ${TRACK:+--track "$TRACK"} \
  ${VERSION:+--version "$VERSION"} \
  ${REQUESTER:+--requester "$REQUESTER"} \
  --reason "$REASON" \
  "$ECOSYSTEM:$PACKAGE"
`,
			},
			{
				Name: "propagate", Kind: workflowcontroller.KindFanOut,
				Header: `# A new register tag is news every consumer wants: tell them, so their
# pipelines re-sync against it instead of waiting for a human.
#
# UNEXERCISED: written where Actions cannot run. It needs one secret,
# FORGE_CI_GITHUB_TOKEN - a token that can send repository_dispatch to the
# consumer repos named below.
`,
				TagPattern: "v*", Secret: "FORGE_CI_GITHUB_TOKEN", EventType: "register-tag",
				Consumers: []string{"golden-factory", "golden-register-consumer"},
			},
			{Name: "release", Kind: workflowcontroller.KindRelease, Secret: "FORGE_CI_GITHUB_TOKEN"},
		},
		Runner: workflowcontroller.RunnerSpec{Name: "ci-runner", Secret: "FORGE_CI_GITHUB_TOKEN"},
	}
}

func TestRenderMatchesTheHandWrittenWorkflows(t *testing.T) {
	t.Parallel()

	files, err := workflowcontroller.RenderAll(registerSpec())
	require.NoError(t, err)

	byName := map[string]string{}
	for _, f := range files {
		byName[f.Name] = f.Content
	}

	for _, name := range []string{"intake", "request", "propagate", "release"} {
		assertGolden(t, name, byName[name],
			"%s must render byte-identical to the hand-written file it replaces", name)
	}

	assert.Contains(t, byName["ci-runner"], "run-name: run ${{ inputs.marker }}")
	assert.Contains(t, byName["ci-runner"], "${{ inputs.script }}")
}

// assertGolden compares against testdata and, with UPDATE_GOLDEN set, writes
// it instead. A deliberate change is then one environment variable and an
// accidental one is still a failure, which is the point: these files are the
// only thing standing between a rendering change and every consumer's CI.
func assertGolden(t *testing.T, name, got, msg string, args ...any) {
	t.Helper()

	path := filepath.Join("testdata", name+".yaml")

	if os.Getenv("UPDATE_GOLDEN") != "" {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
	}

	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equalf(t, string(want), got, msg, args...)
}

// A job that runs inside a prebuilt toolchain image installs no toolchain,
// and still checks the workspace out: a container carries tools, not members.
func TestAContainerReplacesTheToolchainInstallAndNotTheCheckout(t *testing.T) {
	t.Parallel()

	spec := registerSpec()
	spec.Container = "ghcr.io/an-owner/a-toolchain:v1.2.3"
	spec.Workspace.ToolchainScript = ""

	files, err := workflowcontroller.RenderAll(spec)
	require.NoError(t, err)

	byName := map[string]string{}
	for _, f := range files {
		byName[f.Name] = f.Content
	}

	assertGolden(t, "intake-container", byName["intake"],
		"a command workflow in a container must render exactly this")

	// release is in this list because it stands the workspace up too: its
	// toolchain script does `cd <member> && go install`, so a lone checkout
	// of this repo leaves that step with no directory to enter.
	for _, name := range []string{"intake", "request", "ci-runner", "release"} {
		got := byName[name]

		assert.Containsf(t, got,
			"    runs-on: ubuntu-latest\n    container:\n      image: ghcr.io/an-owner/a-toolchain:v1.2.3\n    steps:\n",
			"%s: the container block sits under the job beside runs-on and steps", name)
		assert.NotContainsf(t, got, "Install the toolchain from the workspace",
			"%s: the image supplies the toolchain, so the install step goes", name)
		assert.Containsf(t, got, "Check out the workspace around this repo",
			"%s: the checkout stays - a container carries tools and not a workspace", name)
	}

	// A fan-out curls a dispatch and nothing else. It builds no workspace and
	// runs no toolchain, so it stays on the runner's own image.
	assert.NotContains(t, byName["propagate"], "container:",
		"a fan-out only curls a dispatch, so it needs no toolchain image")
}

func TestRenderHonorsAVerbatimOverride(t *testing.T) {
	t.Parallel()

	spec := registerSpec()
	spec.Workflows = []workflowcontroller.WorkflowSpec{{Name: "custom", Content: "on: push\n"}}

	files, err := workflowcontroller.RenderAll(spec)
	require.NoError(t, err)
	assert.Equal(t, "on: push\n", files[0].Content)
}

func TestParseSpecDefaultsAndRefusals(t *testing.T) {
	t.Parallel()

	_, err := workflowcontroller.ParseSpec(map[string]any{})
	require.ErrorContains(t, err, "spec.repo is required")

	s, err := workflowcontroller.ParseSpec(map[string]any{"repo": "o/r"})
	require.NoError(t, err)
	assert.Equal(t, "r", s.Dir)
	assert.Equal(t, "GITHUB_TOKEN", s.TokenEnv)
	assert.Equal(t, "main", s.Ref)
	assert.Equal(t, 15, s.Runner.PollIntervalSeconds)
	assert.Equal(t, 30, s.Runner.TimeoutMinutes)

	_, err = workflowcontroller.ParseSpec(map[string]any{
		"repo": "o/r", "workflows": []any{map[string]any{"kind": "command"}},
	})
	require.ErrorContains(t, err, "has no name")

	_, err = workflowcontroller.ParseSpec(map[string]any{
		"repo": "o/r", "workflows": []any{map[string]any{"name": "x", "kind": "command"}},
	})
	require.ErrorContains(t, err, "needs command and secret")

	_, err = workflowcontroller.ParseSpec(map[string]any{
		"repo": "o/r", "workflows": []any{map[string]any{
			"name": "x", "kind": "command", "command": "true", "secret": "S",
		}},
	})
	require.ErrorContains(t, err, "needs a cron or events")

	base := specMap(t)
	base["workspace"] = map[string]any{}
	_, err = workflowcontroller.ParseSpec(base)
	require.ErrorContains(t, err, "workspace.bootstrapCommand is required")

	// A container carries tools, not a checked-out workspace, so the
	// bootstrap stays required however the job gets its toolchain.
	noBootstrap := specMap(t)
	noBootstrap["container"] = "an-image:v1"
	noBootstrap["workspace"] = map[string]any{"toolchainScript": "true\n"}
	_, err = workflowcontroller.ParseSpec(noBootstrap)
	require.ErrorContains(t, err, "workspace.bootstrapCommand is required")
	require.ErrorContains(t, err, "supplies tools and not a workspace")

	// Neither a script nor an image means nothing installs the toolchain.
	// The refusal has to name the alternative, or the reader adds a script
	// they did not need.
	noToolchain := specMap(t)
	noToolchain["workspace"] = map[string]any{"bootstrapCommand": "true"}
	_, err = workflowcontroller.ParseSpec(noToolchain)
	require.ErrorContains(t, err, "workspace.toolchainScript is required unless spec.container")

	_, err = workflowcontroller.ParseSpec(map[string]any{
		"repo": "o/r", "setup": []any{map[string]any{"with": map[string]any{"a": "b"}}},
	})
	require.ErrorContains(t, err, "setup[0] has no uses")

	_, err = workflowcontroller.ParseSpec(map[string]any{
		"repo": "o/r", "workflows": []any{map[string]any{"name": "x", "kind": "fan-out"}},
	})
	require.ErrorContains(t, err, "needs eventType, consumers and secret")

	_, err = workflowcontroller.ParseSpec(map[string]any{
		"repo": "o/r", "workflows": []any{map[string]any{"name": "x", "kind": "mystery"}},
	})
	require.ErrorContains(t, err, `unknown kind "mystery"`)
}

func specMap(t *testing.T) map[string]any {
	t.Helper()

	return map[string]any{
		"repo": "o/r",
		"workspace": map[string]any{
			"bootstrapCommand": "true", "toolchainScript": "true\n",
		},
		"runner": map[string]any{"name": "ci-runner", "secret": "S"},
	}
}

func TestDeclareEmitsEveryGitHubResource(t *testing.T) {
	t.Parallel()

	c := workflowcontroller.New(nil, nil, nil)

	spec := specMap(t)
	spec["secrets"] = []any{map[string]any{"name": "FORGE_CI_GITHUB_TOKEN"}}
	spec["workflows"] = []any{
		map[string]any{"name": "release", "kind": "release", "secret": "FORGE_CI_GITHUB_TOKEN"},
	}

	out, err := c.Declare(spec)
	require.NoError(t, err)

	ids := make([]string, 0, len(out.Resources))
	for _, r := range out.Resources {
		ids = append(ids, r.ID())
	}

	assert.Equal(t, []string{
		"file-content/r/.github/workflows/release.yaml",
		"file-content/r/.github/workflows/ci-runner.yaml",
		"actions-secret/o/r/FORGE_CI_GITHUB_TOKEN",
		"workflow-enabled/o/r/release.yaml",
		"workflow-enabled/o/r/ci-runner.yaml",
	}, ids)

	for _, r := range out.Resources {
		require.NotNilf(t, r.Spec, "%s must carry a non-nil spec over the wire", r.ID())
	}

	byID := map[string]bool{}
	for _, r := range out.Resources {
		byID[r.ID()] = r.BootstrapOnly
	}

	assert.True(t, byID["actions-secret/o/r/FORGE_CI_GITHUB_TOKEN"], "a credential is written, not converged")
	assert.False(t, byID["workflow-enabled/o/r/release.yaml"],
		"a workflow the spec adds or renames must take effect on the next apply")
	assert.False(t, byID["workflow-enabled/o/r/ci-runner.yaml"])
	assert.False(t, byID["file-content/r/.github/workflows/release.yaml"],
		"a workflow file must follow its engine, drift included")
}

// fakeClock advances only when the controller sleeps, so a poll loop
// finishes in microseconds.
type fakeClock struct {
	now time.Time
}

func (f *fakeClock) Now() time.Time        { return f.now }
func (f *fakeClock) Sleep(d time.Duration) { f.now = f.now.Add(d) }

func runInput(spec map[string]any) citypes.RunInput {
	return citypes.RunInput{
		Revision: "0123456789abcdef",
		Stage:    "process",
		Substage: "default",
		Targets:  []citypes.Target{{Alias: "process", Forge: "test run process", In: []string{"r"}}},
		Spec:     spec,
	}
}

func TestRunPassesOnASuccessConclusion(t *testing.T) {
	t.Parallel()

	api := githubadaptermock.NewMockAPI(t)
	clock := &fakeClock{now: time.Unix(1000, 0)}
	c := workflowcontroller.New(
		func(workflowcontroller.Spec) githubadapter.API { return api }, clock.Now, clock.Sleep)

	var marker string

	api.EXPECT().Dispatch(mock.Anything, "o/r", "ci-runner.yaml", "main", mock.Anything).
		RunAndReturn(func(_ context.Context, _, _, _ string, inputs map[string]string) error {
			marker = inputs["marker"]
			assert.Contains(t, inputs["marker"], "0123456789ab-process-default-")
			assert.Contains(t, inputs["script"], "set -eu")
			assert.Contains(t, inputs["script"], `(cd 'r' && 'forge' 'test' 'run' 'process')`)

			return nil
		})
	api.EXPECT().ListRuns(mock.Anything, "o/r", "ci-runner.yaml", mock.Anything).
		RunAndReturn(func(context.Context, string, string, time.Time) ([]githubadapter.RunInfo, error) {
			return []githubadapter.RunInfo{
				{ID: 3, DisplayTitle: "run other-marker", Status: "completed"},
				{ID: 7, DisplayTitle: "run " + marker, Status: "in_progress"},
			}, nil
		})
	api.EXPECT().Run(mock.Anything, "o/r", int64(7)).
		Return(githubadapter.RunInfo{ID: 7, Status: "in_progress"}, nil).Once()
	api.EXPECT().Run(mock.Anything, "o/r", int64(7)).
		Return(githubadapter.RunInfo{ID: 7, Status: "completed", Conclusion: "success", HTMLURL: "http://x/7"}, nil).Once()

	out, err := c.Run(t.Context(), runInput(specMap(t)))
	require.NoError(t, err)
	assert.Equal(t, citypes.StatusPassed, out.Status)
	assert.Contains(t, out.Output, "http://x/7")
}

func TestRunAFailedConclusionIsAFailedRunNotAnError(t *testing.T) {
	t.Parallel()

	// The hard rule: a red build must never look like a broken runner.
	api := githubadaptermock.NewMockAPI(t)
	clock := &fakeClock{now: time.Unix(1000, 0)}
	c := workflowcontroller.New(
		func(workflowcontroller.Spec) githubadapter.API { return api }, clock.Now, clock.Sleep)

	var marker string

	api.EXPECT().Dispatch(mock.Anything, "o/r", "ci-runner.yaml", "main", mock.Anything).
		RunAndReturn(func(_ context.Context, _, _, _ string, inputs map[string]string) error {
			marker = inputs["marker"]

			return nil
		})
	api.EXPECT().ListRuns(mock.Anything, "o/r", "ci-runner.yaml", mock.Anything).
		RunAndReturn(func(context.Context, string, string, time.Time) ([]githubadapter.RunInfo, error) {
			return []githubadapter.RunInfo{{ID: 9, DisplayTitle: "run " + marker}}, nil
		})
	api.EXPECT().Run(mock.Anything, "o/r", int64(9)).
		Return(githubadapter.RunInfo{ID: 9, Status: "completed", Conclusion: "failure", HTMLURL: "http://x/9"}, nil)

	out, err := c.Run(t.Context(), runInput(specMap(t)))
	require.NoError(t, err, "a failed run is a result, not an error")
	assert.Equal(t, citypes.StatusFailed, out.Status)
	assert.Contains(t, out.Message, "concluded failure")
	assert.Contains(t, out.Message, "http://x/9")
}

func TestRunTimesOutWhenTheRunNeverAppears(t *testing.T) {
	t.Parallel()

	api := githubadaptermock.NewMockAPI(t)
	clock := &fakeClock{now: time.Unix(1000, 0)}
	c := workflowcontroller.New(
		func(workflowcontroller.Spec) githubadapter.API { return api }, clock.Now, clock.Sleep)

	api.EXPECT().Dispatch(mock.Anything, "o/r", "ci-runner.yaml", "main", mock.Anything).Return(nil)
	api.EXPECT().ListRuns(mock.Anything, "o/r", "ci-runner.yaml", mock.Anything).
		Return([]githubadapter.RunInfo{}, nil)

	_, err := c.Run(t.Context(), runInput(specMap(t)))
	require.ErrorContains(t, err, "no completed run inside 30 minutes")
}

func TestRunErrorsWhenTheMachineryBreaks(t *testing.T) {
	t.Parallel()

	api := githubadaptermock.NewMockAPI(t)
	clock := &fakeClock{now: time.Unix(1000, 0)}
	c := workflowcontroller.New(
		func(workflowcontroller.Spec) githubadapter.API { return api }, clock.Now, clock.Sleep)

	api.EXPECT().Dispatch(mock.Anything, "o/r", "ci-runner.yaml", "main", mock.Anything).
		Return(assert.AnError)

	_, err := c.Run(t.Context(), runInput(specMap(t)))
	require.ErrorIs(t, err, assert.AnError)
}

func TestRunRefusesMissingRunnerOrTargets(t *testing.T) {
	t.Parallel()

	c := workflowcontroller.New(nil, nil, nil)

	in := runInput(map[string]any{"repo": "o/r"})
	_, err := c.Run(t.Context(), in)
	require.ErrorContains(t, err, "spec.runner.name is required")

	in = runInput(specMap(t))
	in.Targets = nil
	_, err = c.Run(t.Context(), in)
	require.ErrorContains(t, err, "no targets given")
}

func TestRenderAllRefusesAnUnrenderableKind(t *testing.T) {
	t.Parallel()

	// ParseSpec blocks unknown kinds at the door; this pins the renderer's
	// own refusal for a hand-built spec.
	_, err := workflowcontroller.RenderAll(workflowcontroller.Spec{
		Workflows: []workflowcontroller.WorkflowSpec{{Name: "x", Kind: "weird"}},
	})
	require.ErrorContains(t, err, `nothing renders kind "weird"`)
}

func TestRenderCommandDefaultsJobAndStep(t *testing.T) {
	t.Parallel()

	files, err := workflowcontroller.RenderAll(workflowcontroller.Spec{
		Workflows: []workflowcontroller.WorkflowSpec{{
			Name: "chore", Kind: workflowcontroller.KindCommand,
			Cron: "0 0 * * *", Secret: "S", Command: "true\n",
		}},
	})
	require.NoError(t, err)
	assert.Contains(t, files[0].Content, "\n  run:\n")
	assert.Contains(t, files[0].Content, "- name: Run the command")
}

func TestRenderFanOutDefaultsTheTagPattern(t *testing.T) {
	t.Parallel()

	files, err := workflowcontroller.RenderAll(workflowcontroller.Spec{
		Workflows: []workflowcontroller.WorkflowSpec{{
			Name: "propagate", Kind: workflowcontroller.KindFanOut,
			Secret: "S", EventType: "tag", Consumers: []string{"a"},
		}},
	})
	require.NoError(t, err)
	assert.Contains(t, files[0].Content, `tags: ["v*"]`)
}

func TestDeclareRefusesANamelessSecret(t *testing.T) {
	t.Parallel()

	c := workflowcontroller.New(nil, nil, nil)

	spec := specMap(t)
	spec["secrets"] = []any{map[string]any{"fromEnv": "X"}}

	_, err := c.Declare(spec)
	require.ErrorContains(t, err, "secrets entry has no name")
}

func TestRunRefusesABrokenTarget(t *testing.T) {
	t.Parallel()

	c := workflowcontroller.New(nil, nil, nil)

	in := runInput(specMap(t))
	in.Targets = []citypes.Target{{Alias: "both", Forge: "x", ForgeCI: "y"}}
	_, err := c.Run(t.Context(), in)
	require.ErrorContains(t, err, "exactly one of forge or forgeCI")
}

func TestRunSurfacesAPollError(t *testing.T) {
	t.Parallel()

	api := githubadaptermock.NewMockAPI(t)
	clock := &fakeClock{now: time.Unix(1000, 0)}
	c := workflowcontroller.New(
		func(workflowcontroller.Spec) githubadapter.API { return api }, clock.Now, clock.Sleep)

	api.EXPECT().Dispatch(mock.Anything, "o/r", "ci-runner.yaml", "main", mock.Anything).Return(nil)
	api.EXPECT().ListRuns(mock.Anything, "o/r", "ci-runner.yaml", mock.Anything).
		Return(nil, assert.AnError)

	_, err := c.Run(t.Context(), runInput(specMap(t)))
	require.ErrorIs(t, err, assert.AnError)
}

func TestRunSurfacesARunFetchError(t *testing.T) {
	t.Parallel()

	api := githubadaptermock.NewMockAPI(t)
	clock := &fakeClock{now: time.Unix(1000, 0)}
	c := workflowcontroller.New(
		func(workflowcontroller.Spec) githubadapter.API { return api }, clock.Now, clock.Sleep)

	var marker string

	api.EXPECT().Dispatch(mock.Anything, "o/r", "ci-runner.yaml", "main", mock.Anything).
		RunAndReturn(func(_ context.Context, _, _, _ string, inputs map[string]string) error {
			marker = inputs["marker"]

			return nil
		})
	api.EXPECT().ListRuns(mock.Anything, "o/r", "ci-runner.yaml", mock.Anything).
		RunAndReturn(func(context.Context, string, string, time.Time) ([]githubadapter.RunInfo, error) {
			return []githubadapter.RunInfo{{ID: 2, DisplayTitle: "run " + marker}}, nil
		})
	api.EXPECT().Run(mock.Anything, "o/r", int64(2)).Return(githubadapter.RunInfo{}, assert.AnError)

	_, err := c.Run(t.Context(), runInput(specMap(t)))
	require.ErrorIs(t, err, assert.AnError)
}

func TestRunPinsTheRevisionShasInTheScript(t *testing.T) {
	t.Parallel()

	api := githubadaptermock.NewMockAPI(t)
	clock := &fakeClock{now: time.Unix(1000, 0)}
	c := workflowcontroller.New(
		func(workflowcontroller.Spec) githubadapter.API { return api }, clock.Now, clock.Sleep)

	var marker string

	api.EXPECT().Dispatch(mock.Anything, "o/r", "ci-runner.yaml", "main", mock.Anything).
		RunAndReturn(func(_ context.Context, _, _, _ string, inputs map[string]string) error {
			marker = inputs["marker"]
			// The remote run must execute the code the revision names,
			// not whatever the branch moved to.
			assert.Contains(t, inputs["script"], `git -C 'r' fetch origin 'abc123'`)
			assert.Contains(t, inputs["script"], `git -C 'r' checkout --detach 'abc123'`)

			return nil
		})
	api.EXPECT().ListRuns(mock.Anything, "o/r", "ci-runner.yaml", mock.Anything).
		RunAndReturn(func(context.Context, string, string, time.Time) ([]githubadapter.RunInfo, error) {
			return []githubadapter.RunInfo{{ID: 4, DisplayTitle: "run " + marker}}, nil
		})
	api.EXPECT().Run(mock.Anything, "o/r", int64(4)).
		Return(githubadapter.RunInfo{ID: 4, Status: "completed", Conclusion: "success"}, nil)

	in := runInput(specMap(t))
	in.Repos = []citypes.RepoCheckout{{Name: "r", SHA: "abc123"}}

	_, err := c.Run(t.Context(), in)
	require.NoError(t, err)
}

func TestRunRefusesADirtyRevision(t *testing.T) {
	t.Parallel()

	// A dirty revision covers uncommitted changes; a remote runner can
	// never see them, so running it there would test other code than the
	// revision names.
	c := workflowcontroller.New(nil, nil, nil)

	in := runInput(specMap(t))
	in.Revision = "abc123-dirty"

	_, err := c.Run(t.Context(), in)
	require.ErrorContains(t, err, "covers uncommitted changes")
}

// TestTheRenderedCheckoutStandsUpAWorkspace executes the checkout step the
// generator writes, instead of comparing it to a golden file.
//
// The goldens could not catch what shipped: they were written from the
// generator, so a generator that renders a broken checkout renders a golden
// that agrees with it. This ran five hand-written lines for a week, cloning
// a factory and calling its place script from the directory above it, and
// every scheduled run failed at the same step while both goldens stayed
// green.
//
// So this drives the block. The seed command is a stand-in, because the real
// one fetches a module, but the shape under test is the one that broke: what
// the step assumes about the working directory, and whether anything but the
// spec's own command decides where files land.
func TestTheRenderedCheckoutStandsUpAWorkspace(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	work := t.TempDir()

	spec := registerSpec()
	spec.Workspace.BootstrapCommand = "mkdir -p golden-register && touch forge-factory.yaml forge-ci.yaml"

	files, err := workflowcontroller.RenderAll(spec)
	require.NoError(t, err)

	var intake string

	for _, f := range files {
		if f.Name == "intake" {
			intake = f.Content
		}
	}

	require.NotEmpty(t, intake)

	block := checkoutBlock(t, intake)
	require.NotEmpty(t, block, "the rendered intake must carry a checkout step")

	// A runner expands every ${{ }} before sh sees the block. Stand in for
	// that, or sh reads the braces as its own syntax and fails on the line
	// this test is not about.
	block = expressions.ReplaceAllString(block, "expanded")

	cmd := exec.Command("sh", "-e", "-c", block)
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "HOME="+home, "GIT_CONFIG_GLOBAL="+filepath.Join(home, ".gitconfig"))

	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "the checkout step must run from the working directory it is given: %s", out)

	for _, want := range []string{"forge-factory.yaml", "forge-ci.yaml", "golden-register"} {
		_, err := os.Stat(filepath.Join(work, want))
		require.NoErrorf(t, err, "the checkout must leave %s at the working directory", want)
	}
}

// checkoutBlock lifts the run: block of the checkout step out of a rendered
// workflow and strips its indentation, so the test drives the same text a
// runner would.
func checkoutBlock(t *testing.T, workflow string) string {
	t.Helper()

	const (
		head   = "      - name: Check out the workspace around this repo\n        run: |\n"
		indent = "          "
	)

	start := strings.Index(workflow, head)
	if start < 0 {
		return ""
	}

	var b strings.Builder

	for _, line := range strings.Split(workflow[start+len(head):], "\n") {
		if line != "" && !strings.HasPrefix(line, indent) {
			break
		}

		b.WriteString(strings.TrimPrefix(line, indent) + "\n")
	}

	return b.String()
}

// expressions matches an Actions ${{ }} expression, which a runner expands
// before the shell ever sees it.
var expressions = regexp.MustCompile(`\$\{\{[^}]*\}\}`)

// TestOnlyAScheduledWorkflowRaisesItsOwnAlarm pins who gets a failure report.
//
// A scheduled run has no audience by construction, and one instance proved
// what that costs: eight consecutive red runs over eight days, seen by
// nobody, while the thing the schedule existed to maintain went stale. So a
// cron workflow files an issue on itself.
//
// A dispatched run already has the person who dispatched it. Filing there
// would turn a mistyped payload into repository noise.
func TestOnlyAScheduledWorkflowRaisesItsOwnAlarm(t *testing.T) {
	t.Parallel()

	files, err := workflowcontroller.RenderAll(registerSpec())
	require.NoError(t, err)

	byName := map[string]string{}
	for _, f := range files {
		byName[f.Name] = f.Content
	}

	scheduled := byName["intake"]
	assert.Contains(t, scheduled, "- name: Say that the run failed")
	assert.Contains(t, scheduled, "if: failure()")
	assert.Contains(t, scheduled, "issues: write",
		"a workflow that files an issue needs the permission to file one")
	assert.NotContains(t, scheduled, "gh issue",
		"the toolchain image carries no gh, so the step that reports a failure must not need one")
	assert.Contains(t, scheduled, `grep -q "\"title\": *\"$TITLE\""`,
		"a job that fails daily must leave one issue open, not thirty")
	assert.Contains(t, scheduled, "TOKEN: ${{ github.token }}",
		"reporting a failure must not need a secret somebody has to remember to seal")

	// A repository_dispatch run looks attended and is not: somebody caused
	// it from another repo, and they are watching that repo's checks. This
	// was gated on cron alone until a second workspace needed a
	// member-pushed pipeline and the hole showed.
	dispatched := byName["request"]
	assert.Contains(t, dispatched, "- name: Say that the run failed",
		"a run somebody triggered from another repo has no audience here")
	assert.Contains(t, dispatched, "issues: write")

	// A push to this repo, or a workflow_dispatch somebody typed, has a
	// person already looking at these checks.
	assert.NotContains(t, byName["propagate"], "Say that the run failed",
		"a tag push here is watched by whoever pushed the tag")
}

// TestEveryGeneratedWorkflowCanBeFiredByHand pins the trigger that makes a
// workflow testable.
//
// A workflow only a schedule or another repo can fire cannot be run by the
// person who just changed it. They push the change and wait, which is how one
// of these went eight days without completing a run: nobody could try it, so
// nobody did.
func TestEveryGeneratedWorkflowCanBeFiredByHand(t *testing.T) {
	t.Parallel()

	files, err := workflowcontroller.RenderAll(registerSpec())
	require.NoError(t, err)

	for _, f := range files {
		if !strings.Contains(f.Content, "schedule:") &&
			!strings.Contains(f.Content, "repository_dispatch:") {
			continue
		}

		assert.Containsf(t, f.Content, "workflow_dispatch: {}",
			"%s can only be fired by something other than a person, so nobody can test it", f.Name)
	}
}

// TestAPublishingWorkflowGetsACredentialAndThePermissionToUseIt pins the gap
// that would have failed the first release at its last step.
//
// Nothing else puts a token into the environment the pipeline command runs
// in. Engines inherit forge-ci's environment and nothing else, and the
// secrets a bootstrap seals into Actions are sealed on the operator's laptop:
// they put nothing into a running job. So a release engine that shells out to
// the API, or pushes to a registry, had no credential at all, and would have found
// that out after the build and the tags.
func TestAPublishingWorkflowGetsACredentialAndThePermissionToUseIt(t *testing.T) {
	t.Parallel()

	rendered, err := workflowcontroller.RenderAll(workflowcontroller.Spec{
		Dir: "workspace",
		Ref: "main",
		Workflows: []workflowcontroller.WorkflowSpec{{
			Name:     "pipeline",
			Kind:     "command",
			Cron:     "0 * * * *",
			Command:  "forge-ci apply --config forge-ci.yaml --root .",
			Token:    true,
			Packages: true,
		}},
	})
	require.NoError(t, err)
	require.Len(t, rendered, 1)

	got := rendered[0].Content

	assert.Contains(t, got, "GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}",
		"the release engine has no other source of a token")
	assert.Contains(t, got, "packages: write",
		"a registry write needs its own permission and nothing else grants it")
	assert.Contains(t, got, "contents: write")
}

// A workflow that publishes nothing must not carry a credential it does not
// need. A token in an environment is a token something can read.
func TestAWorkflowThatPublishesNothingCarriesNoToken(t *testing.T) {
	t.Parallel()

	rendered, err := workflowcontroller.RenderAll(workflowcontroller.Spec{
		Dir: "workspace",
		Ref: "main",
		Workflows: []workflowcontroller.WorkflowSpec{{
			Name:    "check",
			Kind:    "command",
			Cron:    "0 * * * *",
			Command: "forge test-all",
		}},
	})
	require.NoError(t, err)

	got := rendered[0].Content

	assert.NotContains(t, got, "GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}")
	assert.NotContains(t, got, "packages: write")
}

// TestTheWorkflowSecretReachesTheCommandAndNotOnlyTheCheckout pins the gap
// that failed the pipeline at its real work.
//
// An apply reconciles the resources it declares before it runs anything, and
// a sealed Actions secret is realized by PUTTING it: the API is write-only,
// so a put is the only convergence there is, and a put needs the value. The
// secret reached the checkout step's git config and nothing else, so every
// run died on "environment variable FORGE_CI_GITHUB_TOKEN is empty" - advice
// written for an operator's laptop, printed inside a runner that has no
// .envrc to fix.
func TestTheWorkflowSecretReachesTheCommandAndNotOnlyTheCheckout(t *testing.T) {
	t.Parallel()

	rendered, err := workflowcontroller.RenderAll(workflowcontroller.Spec{
		Dir: "workspace",
		Ref: "main",
		Workflows: []workflowcontroller.WorkflowSpec{{
			Name:    "pipeline",
			Kind:    "command",
			Events:  []string{"member-pushed"},
			Secret:  "FORGE_CI_GITHUB_TOKEN",
			Command: "forge-ci apply --config forge-ci.yaml --root .",
		}},
	})
	require.NoError(t, err)

	got := rendered[0].Content

	assert.Contains(t, got, "FORGE_CI_GITHUB_TOKEN: ${{ secrets.FORGE_CI_GITHUB_TOKEN }}",
		"the apply re-seals this secret, so it needs the value")
	assert.Contains(t, got, "x-access-token:${{ secrets.FORGE_CI_GITHUB_TOKEN }}",
		"the checkout still gets it too")
}

// A workflow that names no secret carries none. A credential in an
// environment is a credential something can read.
func TestAWorkflowWithNoSecretCarriesNoSecretEnv(t *testing.T) {
	t.Parallel()

	rendered, err := workflowcontroller.RenderAll(workflowcontroller.Spec{
		Dir: "workspace",
		Ref: "main",
		Workflows: []workflowcontroller.WorkflowSpec{{
			Name:    "check",
			Kind:    "command",
			Cron:    "0 * * * *",
			Command: "forge test-all",
		}},
	})
	require.NoError(t, err)
	assert.NotContains(t, rendered[0].Content, "secrets.FORGE_CI_GITHUB_TOKEN")
}

// A factory's own pipeline needs both: nothing dispatches when the workspace
// files themselves change, and two applies at once race the state repo.
//
// Live case: the hand-written workflow these replace carried a concurrency
// group with the comment "apply is idempotent, and two at once would race the
// state repo", and seven dispatches arrived within seconds of each other the
// day this was written.
func TestAPipelineCanRunOnItsOwnPushAndOnlyOneAtATime(t *testing.T) {
	t.Parallel()

	spec := workflowcontroller.Spec{
		Repo:      "o/r",
		Workspace: workflowcontroller.Workspace{BootstrapCommand: "true", ToolchainScript: "true\n"},
		Workflows: []workflowcontroller.WorkflowSpec{{
			Name: "pipeline", Kind: "command", Command: "true\n", Secret: "S",
			Events:       []string{"member-pushed"},
			PushBranches: []string{"main"},
			Concurrency:  "pipeline",
		}},
	}

	files, err := workflowcontroller.RenderAll(spec)
	require.NoError(t, err)
	require.Len(t, files, 1)

	got := files[0].Content
	assert.Contains(t, got, "  push:\n    branches: [main]\n")
	assert.Contains(t, got, "\nconcurrency:\n  group: pipeline\n  cancel-in-progress: false\n")

	// Queued, never cancelled. An apply already writing state has to finish,
	// or the store keeps a revision with no run beside it.
	assert.NotContains(t, got, "cancel-in-progress: true")

	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(got), &parsed), "the rendered workflow must be valid YAML")
}

// Neither is rendered when unasked, so every existing workflow is unchanged.
func TestNoPushTriggerAndNoConcurrencyWithoutThem(t *testing.T) {
	t.Parallel()

	files, err := workflowcontroller.RenderAll(registerSpec())
	require.NoError(t, err)

	byName := map[string]string{}
	for _, f := range files {
		byName[f.Name] = f.Content
		assert.NotContainsf(t, f.Content, "concurrency:", "%s asked for no concurrency group", f.Name)
	}

	// Only the command workflows. A fan-out fires on a tag push by
	// definition, so "push:" there is its trigger and not this feature.
	for _, name := range []string{"intake", "request", "ci-runner"} {
		assert.NotContainsf(t, byName[name], "  push:\n    branches:",
			"%s asked for no push trigger", name)
	}
}
