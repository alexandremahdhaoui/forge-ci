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

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/workflowcontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/fsadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/githubadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

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
				On: workflowcontroller.OnSpec{Cron: "17 5 * * *"}, Job: "apply", StepName: "Run the register pipeline",
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
				On: workflowcontroller.OnSpec{Events: []string{"admission-request"}}, Job: "file", StepName: "File the request",
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

func TestAContainerReplacesTheToolchainInstallAndNotTheCheckout(t *testing.T) {
	t.Parallel()

	spec := registerSpec()
	spec.Container = workflowcontroller.ContainerSpec{Ref: "ghcr.io/an-owner/a-toolchain:v1.2.3"}
	spec.Workspace.ToolchainScript = ""

	files, err := workflowcontroller.RenderAll(spec)
	require.NoError(t, err)

	byName := map[string]string{}
	for _, f := range files {
		byName[f.Name] = f.Content
	}

	assertGolden(t, "intake-container", byName["intake"],
		"a command workflow in a container must render exactly this")

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

	assert.NotContains(t, byName["propagate"], "container:",
		"a fan-out only curls a dispatch, so it needs no toolchain image")
}

func TestAContainerFileResolvesThePinThroughTheFSTheConstructorReceived(t *testing.T) {
	t.Parallel()

	root := "/a/workspace"
	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().ReadFile(filepath.Join(root, ".forge", "toolchain-image")).
		Return([]byte("ghcr.io/an-owner/a-toolchain:v9.9.9\n"), nil)

	c := workflow(t, nil, fs, time.Now, time.Sleep)

	spec := specMap(t)
	spec["container"] = map[string]any{"file": ".forge/toolchain-image"}
	spec["workspace"] = map[string]any{"bootstrapCommand": "true"}

	out, err := c.Declare(spec, root, nil, nil)
	require.NoError(t, err)

	rendered := false

	for _, r := range out.Resources {
		content, _ := r.Spec["content"].(string)
		if strings.Contains(content, "image: ghcr.io/an-owner/a-toolchain:v9.9.9") {
			rendered = true
		}
	}

	assert.True(t, rendered, "the resolved pin must reach a rendered workflow's container block")
}

func TestAMissingContainerFileNamesTheSyncThatWritesIt(t *testing.T) {
	t.Parallel()

	root := "/a/workspace"
	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().ReadFile(filepath.Join(root, ".forge", "toolchain-image")).Return(nil, os.ErrNotExist)

	c := workflow(t, nil, fs, time.Now, time.Sleep)

	spec := specMap(t)
	spec["container"] = map[string]any{"file": ".forge/toolchain-image"}

	_, err := c.Declare(spec, root, nil, nil)
	require.ErrorContains(t, err, "sync first")
}

func TestAnEmptyContainerFileIsRefused(t *testing.T) {
	t.Parallel()

	root := "/a/workspace"
	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().ReadFile(filepath.Join(root, ".forge", "toolchain-image")).Return([]byte("\n"), nil)

	c := workflow(t, nil, fs, time.Now, time.Sleep)

	spec := specMap(t)
	spec["container"] = map[string]any{"file": ".forge/toolchain-image"}

	_, err := c.Declare(spec, root, nil, nil)
	require.ErrorContains(t, err, "is empty")
}

func TestContainerAndContainerFileRefuseEachOther(t *testing.T) {
	t.Parallel()

	spec := specMap(t)
	spec["container"] = map[string]any{"ref": "an-image:v1", "file": ".forge/toolchain-image"}

	_, err := workflowcontroller.ParseSpec(spec)
	require.ErrorContains(t, err, "exactly one of spec.container.ref or spec.container.file")
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
	require.ErrorContains(t, err, "needs on.cron, on.events or on.push")

	base := specMap(t)
	base["workspace"] = map[string]any{}
	_, err = workflowcontroller.ParseSpec(base)
	require.ErrorContains(t, err, "workspace.bootstrapCommand is required")

	noBootstrap := specMap(t)
	noBootstrap["container"] = map[string]any{"ref": "an-image:v1"}
	noBootstrap["workspace"] = map[string]any{"toolchainScript": "true\n"}
	_, err = workflowcontroller.ParseSpec(noBootstrap)
	require.ErrorContains(t, err, "workspace.bootstrapCommand is required")
	require.ErrorContains(t, err, "supplies tools and not a workspace")

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

	c := workflow(t, nil, fsadaptermock.NewMockFS(t), time.Now, time.Sleep)

	spec := specMap(t)
	spec["secrets"] = []any{map[string]any{"name": "FORGE_CI_GITHUB_TOKEN"}}
	spec["workflows"] = []any{
		map[string]any{"name": "release", "kind": "release", "secret": "FORGE_CI_GITHUB_TOKEN"},
	}

	out, err := c.Declare(spec, "", nil, nil)
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

type fakeClock struct {
	now time.Time
}

func (f *fakeClock) Now() time.Time        { return f.now }
func (f *fakeClock) Sleep(d time.Duration) { f.now = f.now.Add(d) }

func workflow(
	t *testing.T, api func(workflowcontroller.Spec) githubadapter.API, fs fsadapter.FS,
	now func() time.Time, sleep func(time.Duration),
) *workflowcontroller.Controller {
	t.Helper()

	c, err := workflowcontroller.New(api, fs, now, sleep)
	require.NoError(t, err)

	return c
}

func TestANilClockOrSleepIsRefusedByNameAtConstruction(t *testing.T) {
	for _, tc := range []struct {
		name  string
		now   func() time.Time
		sleep func(time.Duration)
		want  string
	}{
		{
			name: "clock", now: nil, sleep: time.Sleep,
			want: "building the workflow controller: it was handed no clock, " +
				"and every dispatch measures its wait through one",
		},
		{
			name: "sleep", now: time.Now, sleep: nil,
			want: "building the workflow controller: it was handed no sleep, " +
				"and every dispatch waits between polls through one",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := workflowcontroller.New(nil, fsadaptermock.NewMockFS(t), tc.now, tc.sleep)
			require.EqualError(t, err, tc.want)
			require.Nil(t, c)
		})
	}
}

func runInput(spec map[string]any) citypes.RunInput {
	return citypes.RunInput{
		Revision: "0123456789abcdef",
		Stage:    "process",
		Substage: "default",
		Targets:  []citypes.Target{{Alias: "process", Binary: "forge", Args: []string{"test", "run", "process"}, In: []string{"r"}}},
		Spec:     spec,
	}
}

func TestRunPassesOnASuccessConclusion(t *testing.T) {
	t.Parallel()

	api := githubadaptermock.NewMockAPI(t)
	clock := &fakeClock{now: time.Unix(1000, 0)}
	c := workflow(t,
		func(workflowcontroller.Spec) githubadapter.API { return api }, fsadaptermock.NewMockFS(t), clock.Now, clock.Sleep)

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

	api := githubadaptermock.NewMockAPI(t)
	clock := &fakeClock{now: time.Unix(1000, 0)}
	c := workflow(t,
		func(workflowcontroller.Spec) githubadapter.API { return api }, fsadaptermock.NewMockFS(t), clock.Now, clock.Sleep)

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
	c := workflow(t,
		func(workflowcontroller.Spec) githubadapter.API { return api }, fsadaptermock.NewMockFS(t), clock.Now, clock.Sleep)

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
	c := workflow(t,
		func(workflowcontroller.Spec) githubadapter.API { return api }, fsadaptermock.NewMockFS(t), clock.Now, clock.Sleep)

	api.EXPECT().Dispatch(mock.Anything, "o/r", "ci-runner.yaml", "main", mock.Anything).
		Return(assert.AnError)

	_, err := c.Run(t.Context(), runInput(specMap(t)))
	require.ErrorIs(t, err, assert.AnError)
}

func TestRunRefusesMissingRunnerOrTargets(t *testing.T) {
	t.Parallel()

	c := workflow(t, nil, fsadaptermock.NewMockFS(t), time.Now, time.Sleep)

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
			On: workflowcontroller.OnSpec{Cron: "0 0 * * *"}, Secret: "S", Command: "true\n",
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

	c := workflow(t, nil, fsadaptermock.NewMockFS(t), time.Now, time.Sleep)

	spec := specMap(t)
	spec["secrets"] = []any{map[string]any{"fromEnv": "X"}}

	_, err := c.Declare(spec, "", nil, nil)
	require.ErrorContains(t, err, "secrets entry has no name")
}

func TestRunRefusesABrokenTarget(t *testing.T) {
	t.Parallel()

	c := workflow(t, nil, fsadaptermock.NewMockFS(t), time.Now, time.Sleep)

	in := runInput(specMap(t))
	in.Targets = []citypes.Target{{Alias: "none", Args: []string{"x"}}}
	_, err := c.Run(t.Context(), in)
	require.ErrorContains(t, err, "needs a binary")
}

func TestRunSurfacesAPollError(t *testing.T) {
	t.Parallel()

	api := githubadaptermock.NewMockAPI(t)
	clock := &fakeClock{now: time.Unix(1000, 0)}
	c := workflow(t,
		func(workflowcontroller.Spec) githubadapter.API { return api }, fsadaptermock.NewMockFS(t), clock.Now, clock.Sleep)

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
	c := workflow(t,
		func(workflowcontroller.Spec) githubadapter.API { return api }, fsadaptermock.NewMockFS(t), clock.Now, clock.Sleep)

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
	c := workflow(t,
		func(workflowcontroller.Spec) githubadapter.API { return api }, fsadaptermock.NewMockFS(t), clock.Now, clock.Sleep)

	var marker string

	api.EXPECT().Dispatch(mock.Anything, "o/r", "ci-runner.yaml", "main", mock.Anything).
		RunAndReturn(func(_ context.Context, _, _, _ string, inputs map[string]string) error {
			marker = inputs["marker"]
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

	c := workflow(t, nil, fsadaptermock.NewMockFS(t), time.Now, time.Sleep)

	in := runInput(specMap(t))
	in.Revision = "abc123-dirty"

	_, err := c.Run(t.Context(), in)
	require.ErrorContains(t, err, "covers uncommitted changes")
}

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

var expressions = regexp.MustCompile(`\$\{\{[^}]*\}\}`)

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

	dispatched := byName["request"]
	assert.Contains(t, dispatched, "- name: Say that the run failed",
		"a run somebody triggered from another repo has no audience here")
	assert.Contains(t, dispatched, "issues: write")

	assert.NotContains(t, byName["propagate"], "Say that the run failed",
		"a tag push here is watched by whoever pushed the tag")
}

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

func TestAPublishingWorkflowGetsACredentialAndThePermissionToUseIt(t *testing.T) {
	t.Parallel()

	rendered, err := workflowcontroller.RenderAll(workflowcontroller.Spec{
		Dir: "workspace",
		Ref: "main",
		Workflows: []workflowcontroller.WorkflowSpec{{
			Name:     "pipeline",
			Kind:     "command",
			On:       workflowcontroller.OnSpec{Cron: "0 * * * *"},
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

func TestAWorkflowThatPublishesNothingCarriesNoToken(t *testing.T) {
	t.Parallel()

	rendered, err := workflowcontroller.RenderAll(workflowcontroller.Spec{
		Dir: "workspace",
		Ref: "main",
		Workflows: []workflowcontroller.WorkflowSpec{{
			Name:    "check",
			Kind:    "command",
			On:      workflowcontroller.OnSpec{Cron: "0 * * * *"},
			Command: "forge test-all",
		}},
	})
	require.NoError(t, err)

	got := rendered[0].Content

	assert.NotContains(t, got, "GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}")
	assert.NotContains(t, got, "packages: write")
}

func TestTheWorkflowSecretReachesTheCommandAndNotOnlyTheCheckout(t *testing.T) {
	t.Parallel()

	rendered, err := workflowcontroller.RenderAll(workflowcontroller.Spec{
		Dir: "workspace",
		Ref: "main",
		Workflows: []workflowcontroller.WorkflowSpec{{
			Name:    "pipeline",
			Kind:    "command",
			On:      workflowcontroller.OnSpec{Events: []string{"member-pushed"}},
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

func TestAWorkflowWithNoSecretCarriesNoSecretEnv(t *testing.T) {
	t.Parallel()

	rendered, err := workflowcontroller.RenderAll(workflowcontroller.Spec{
		Dir: "workspace",
		Ref: "main",
		Workflows: []workflowcontroller.WorkflowSpec{{
			Name:    "check",
			Kind:    "command",
			On:      workflowcontroller.OnSpec{Cron: "0 * * * *"},
			Command: "forge test-all",
		}},
	})
	require.NoError(t, err)
	assert.NotContains(t, rendered[0].Content, "secrets.FORGE_CI_GITHUB_TOKEN")
}

func TestAPipelineCanRunOnItsOwnPushAndOnlyOneAtATime(t *testing.T) {
	t.Parallel()

	spec := workflowcontroller.Spec{
		Repo:      "o/r",
		Workspace: workflowcontroller.Workspace{BootstrapCommand: "true", ToolchainScript: "true\n"},
		Workflows: []workflowcontroller.WorkflowSpec{{
			Name: "pipeline", Kind: "command", Command: "true\n", Secret: "S",
			On: workflowcontroller.OnSpec{
				Events: []string{"member-pushed"},
				Push:   &workflowcontroller.PushSpec{Branches: []string{"main"}},
			},
		}},
	}

	files, err := workflowcontroller.RenderAll(spec)
	require.NoError(t, err)
	require.Len(t, files, 1)

	got := files[0].Content
	assert.Contains(t, got, "  push:\n    branches: [main]\n")

	assert.Contains(t, got, "\nconcurrency:\n  group: ${{ github.workflow }}\n  cancel-in-progress: false\n")

	assert.NotContains(t, got, "cancel-in-progress: true")

	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(got), &parsed), "the rendered workflow must be valid YAML")
}

func TestPushTriggerIsOptionalAndSerializationIsNot(t *testing.T) {
	t.Parallel()

	files, err := workflowcontroller.RenderAll(registerSpec())
	require.NoError(t, err)

	byName := map[string]string{}
	for _, f := range files {
		byName[f.Name] = f.Content
	}

	for _, name := range []string{"intake", "request", "ci-runner"} {
		assert.NotContainsf(t, byName[name], "  push:\n    branches:",
			"%s asked for no push trigger", name)
	}

	for _, name := range []string{"intake", "request"} {
		assert.Containsf(t, byName[name],
			"\nconcurrency:\n  group: ${{ github.workflow }}\n  cancel-in-progress: false\n",
			"%s is a command workflow, so its runs serialize by the engine's own guarantee", name)
	}
}
