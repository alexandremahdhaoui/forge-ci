package workflowcontroller_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

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
			FactoryRepo:      "golden-factory",
			PlaceScript:      "hack/place.sh",
			BootstrapCommand: "go run github.com/alexandremahdhaoui/forge-factory/cmd/forge-factory@latest clone --config ../forge-factory.yaml --root ..",
			ToolchainScript: `(cd forge && go install ./cmd/forge) || go install github.com/alexandremahdhaoui/forge/cmd/forge@latest
(cd forge-factory && go install ./cmd/...)
(cd forge-ci && go install ./cmd/...)
(cd forge-register && go install ./cmd/...)
echo "$HOME/go/bin" >> "$GITHUB_PATH"
`,
		},
		Secrets: []workflowcontroller.SecretSpec{
			{Name: "WORKSPACE_TOKEN", FromEnv: "WORKSPACE_TOKEN"},
			{Name: "DISPATCH_TOKEN", FromEnv: "DISPATCH_TOKEN"},
		},
		Workflows: []workflowcontroller.WorkflowSpec{
			{
				Name: "intake", Kind: workflowcontroller.KindCommand,
				Header: `# Scheduled security intake: run the register pipeline so a fresh OSV
# snapshot re-evaluates every track and raises any advisory that is due.
#
# UNEXERCISED: written where Actions cannot run. It needs one secret,
# WORKSPACE_TOKEN - a token that can read every workspace member and push
# to this repo - because the pipeline builds the whole playground
# workspace around this checkout.
`,
				Cron: "17 5 * * *", Job: "apply", StepName: "Run the register pipeline",
				Secret: "WORKSPACE_TOKEN", Push: true,
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
# WORKSPACE_TOKEN as intake. A consumer fires it with:
#
#   forge-register add go:github.com/x/pkg --reason "..." \
#     --dispatch <owner>/golden-register
`,
				Events: []string{"admission-request"}, Job: "file", StepName: "File the request",
				Secret:     "WORKSPACE_TOKEN",
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
# DISPATCH_TOKEN - a token that can send repository_dispatch to the
# consumer repos named below.
`,
				TagPattern: "v*", Secret: "DISPATCH_TOKEN", EventType: "register-tag",
				Consumers: []string{"golden-factory", "golden-register-consumer"},
			},
			{Name: "release", Kind: workflowcontroller.KindRelease},
		},
		Runner: workflowcontroller.RunnerSpec{Name: "ci-runner", Secret: "WORKSPACE_TOKEN"},
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
		want, err := os.ReadFile("testdata/" + name + ".yaml")
		require.NoError(t, err)
		assert.Equalf(t, string(want), byName[name],
			"%s must render byte-identical to the hand-written file it replaces", name)
	}

	assert.Contains(t, byName["ci-runner"], "run-name: run ${{ inputs.marker }}")
	assert.Contains(t, byName["ci-runner"], "${{ inputs.script }}")
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
	require.ErrorContains(t, err, "workspace.factoryRepo, placeScript, bootstrapCommand and toolchainScript are required")

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
			"factoryRepo": "ws", "placeScript": "hack/place.sh",
			"bootstrapCommand": "true", "toolchainScript": "true\n",
		},
		"runner": map[string]any{"name": "ci-runner", "secret": "S"},
	}
}

func TestDeclareEmitsEveryGitHubResource(t *testing.T) {
	t.Parallel()

	c := workflowcontroller.New(nil, nil, nil)

	spec := specMap(t)
	spec["secrets"] = []any{map[string]any{"name": "WORKSPACE_TOKEN"}}
	spec["workflows"] = []any{map[string]any{"name": "release", "kind": "release"}}

	out, err := c.Declare(spec)
	require.NoError(t, err)

	ids := make([]string, 0, len(out.Resources))
	for _, r := range out.Resources {
		ids = append(ids, r.ID())
	}

	assert.Equal(t, []string{
		"file-content/r/.github/workflows/release.yaml",
		"file-content/r/.github/workflows/ci-runner.yaml",
		"actions-secret/o/r/WORKSPACE_TOKEN",
		"workflow-enabled/o/r/release.yaml",
		"workflow-enabled/o/r/ci-runner.yaml",
	}, ids)

	for _, r := range out.Resources {
		require.NotNilf(t, r.Spec, "%s must carry a non-nil spec over the wire", r.ID())
	}
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
