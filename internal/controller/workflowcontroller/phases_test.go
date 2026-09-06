package workflowcontroller_test

import (
	"strings"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/workflowcontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func phasedSpec() workflowcontroller.Spec {
	return workflowcontroller.Spec{
		Repo:      "o/r",
		Dir:       "r",
		Container: workflowcontroller.ContainerSpec{Ref: "ghcr.io/o/toolchain:v1"},
		Workspace: workflowcontroller.Workspace{BootstrapCommand: "forge clone git@github.com:o/r.git ."},
		Jobs:      workflowcontroller.JobsPerStage,
		Stages: []citypes.DeclaredStage{
			{Name: "check", Substages: []citypes.DeclaredSubstage{{Name: "default"}}},
			{Name: "build", Substages: []citypes.DeclaredSubstage{{Name: "default"}}},
			{Name: "publish", Substages: []citypes.DeclaredSubstage{{Name: "default"}, {Name: "dist"}}},
		},
		Workflows: []workflowcontroller.WorkflowSpec{{
			Name:            "ci",
			Kind:            workflowcontroller.KindCommand,
			On: workflowcontroller.OnSpec{
				Push: &workflowcontroller.PushSpec{Branches: []string{"main"}, IgnorePaths: []string{"*.md", "docs/**"}},
			},
			Job:             "apply",
			Secret:          "FORGE_CI_GITHUB_TOKEN",
			Command:         "forge-ci apply --config forge-ci.yaml --root .",
		}},
	}
}

func renderCI(t *testing.T, spec workflowcontroller.Spec) string {
	t.Helper()

	files, err := workflowcontroller.RenderAll(spec)
	require.NoError(t, err)

	for _, f := range files {
		if f.Name == "ci" {
			return f.Content
		}
	}

	t.Fatal("no ci workflow rendered")

	return ""
}

// One apply rendered as jobs: the two fixed ones and then one per stage, in
// the pipeline's order. There is no release job - a release is a substage, so
// the stage that publishes is a stage job like any other. The evaluate job's
// last line is its output, and every job after it runs only on proceed -
// GitHub's own skipped state for a revision with nothing to release.
func TestPhasesRenderOneJobPerStageGatedOnTheEvaluation(t *testing.T) {
	t.Parallel()

	ci := renderCI(t, phasedSpec())

	for _, want := range []string{
		"  self-reconcile:\n    name: Reconcile CI resources\n    outputs:\n      outcome: ${{ steps.phase.outputs.outcome }}\n",
		// The evaluate job runs only when the reconcile converged. A whole
		// apply that superseded itself stops in-process; this gate is what
		// stops the phased run the same way, so the run the settle's push
		// fired is the one that releases - once, not once per run.
		"  evaluate:\n    name: Evaluate next steps\n    needs: [self-reconcile]\n    if: needs.self-reconcile.outputs.outcome == 'converged'\n" +
			"    outputs:\n      outcome: ${{ steps.phase.outputs.outcome }}\n" +
			"      revision: ${{ steps.phase.outputs.revision }}\n",
		"--phase self-reconcile)",
		"sed -n 's/^self-reconcile: //p' | tail -n 1",
		"  check:\n    name: check\n    needs: [evaluate]\n    if: needs.evaluate.outputs.outcome == 'proceed'\n",
		"  build:\n    name: build\n    needs: [evaluate, check]\n    if: needs.evaluate.outputs.outcome == 'proceed'\n",
		"  publish:\n    name: publish\n    needs: [evaluate, build]\n    if: needs.evaluate.outputs.outcome == 'proceed'\n",
		"- name: Reconcile CI resources\n",
		"- name: Evaluate next steps\n",
		"- name: Run stage check\n",
		"- name: Run stage publish\n",
		"out=$(forge-ci apply --config forge-ci.yaml --root . --phase self-reconcile)",
		"--phase evaluate)",
		"sed -n 's/^evaluate: //p' | tail -n 1",
		"forge-ci apply --config forge-ci.yaml --root . --phase stages --stage check --revision ${{ needs.evaluate.outputs.revision }}\n",
		"forge-ci apply --config forge-ci.yaml --root . --phase stages --stage publish --revision ${{ needs.evaluate.outputs.revision }}\n",
		"name: built-${{ github.run_id }}-check\n",
		"name: built-${{ github.run_id }}-publish\n",
		"pattern: built-${{ github.run_id }}-*\n          merge-multiple: true\n",
		// The transport is a tarball per job: a zip carries no unix mode,
		// so a binary a later stage has to RUN would arrive unexecutable.
		"path: .forge-ci/carried\n",
		// Only what THIS job wrote goes in the tarball: anything older than
		// the mark came out of a tarball a job before it kept.
		"find .forge-ci/artifacts -type f -newer .forge-ci/pack-mark -printf '%P\\n' > .forge-ci/pack-list",
		"tar -czf .forge-ci/carried/built-build.tar.gz -C .forge-ci/artifacts -T .forge-ci/pack-list",
		"if: steps.pack.outputs.packed == 'true'\n        uses: actions/upload-artifact@v7",
		"tar -xzf \"$f\" -C .forge-ci/artifacts",
		"    paths-ignore: [\"*.md\", \"docs/**\"]\n",
	} {
		assert.Contains(t, ci, want)
	}

	assert.NotContains(t, ci, "--substage")
	assert.NotContains(t, ci, "--promote")

	// Every job stands the workspace up inside the container: five
	// checkouts, five images, no toolchain install.
	assert.Equal(t, 5, strings.Count(ci, "image: ghcr.io/o/toolchain:v1"))
	assert.Equal(t, 5, strings.Count(ci, "forge clone git@github.com:o/r.git ."))
	assert.Equal(t, 3, strings.Count(ci, "uses: actions/upload-artifact@v7"))
	// Every stage job after the first downloads: a stage reads what the
	// stage before it built, and the stage that publishes reads all of it.
	// The first stage has nothing in front of it, so it skips a step that
	// could only ever match nothing.
	assert.Equal(t, 2, strings.Count(ci, "uses: actions/download-artifact@v8"))
	first := ci[strings.Index(ci, "  check:\n"):strings.Index(ci, "  build:\n")]
	assert.NotContains(t, first, "download-artifact")
	assert.NotContains(t, ci, "--phase release")
	assert.NotContains(t, ci, "Install the toolchain")
	assert.Equal(t, 1, strings.Count(ci, "jobs:\n"))
}

// jobs: substage cuts each stage into one job per substage, running beside
// each other, and one promotion job that needs them all. The next stage needs
// the promotion.
func TestJobsPerSubstageWaitOnEverySubstageOfTheStageBefore(t *testing.T) {
	t.Parallel()

	spec := phasedSpec()
	spec.Jobs = workflowcontroller.JobsPerSubstage

	ci := renderCI(t, spec)

	for _, want := range []string{
		"  check-default:\n    name: check › default\n    needs: [evaluate]\n    if: needs.evaluate.outputs.outcome == 'proceed'\n",
		"  build-default:\n    name: build › default\n    needs: [evaluate, check-default]\n",
		"  publish-default:\n    name: publish › default\n    needs: [evaluate, build-default]\n",
		"  publish-dist:\n    name: publish › dist\n    needs: [evaluate, build-default]\n",
		"- name: Run publish dist\n",
		"--phase stages --stage publish --substage dist --revision ${{ needs.evaluate.outputs.revision }}\n",
		"name: built-${{ github.run_id }}-publish-dist\n",
	} {
		assert.Contains(t, ci, want)
	}

	// A stage's promotion has no job. The jobs of the stage after it wait on
	// every substage of this one, and each works the promotion out from the
	// same run records - which costs a state read, not a whole runner.
	assert.NotContains(t, ci, "promotion-gate")
	assert.NotContains(t, ci, "--promote")
	// Two fixed jobs and one per substage. Nothing else.
	assert.Equal(t, 6, strings.Count(ci, "runs-on: ubuntu-latest"))

	// All four substage jobs upload; the three in stages after the first
	// download, and the first has nothing in front of it to bring back.
	assert.Equal(t, 4, strings.Count(ci, "uses: actions/upload-artifact@v7"))
	assert.Equal(t, 3, strings.Count(ci, "uses: actions/download-artifact@v8"))
	firstStage := ci[strings.Index(ci, "  check-default:"):strings.Index(ci, "  build-default:")]
	assert.NotContains(t, firstStage, "download-artifact")
	assert.Equal(t, 6, strings.Count(ci, "forge clone git@github.com:o/r.git ."))
}

// What a substage declares it needs becomes an edge between the two jobs,
// and a job that waits on a sibling brings back what the sibling built. At
// stage granularity nothing is rendered for it: the waves run inside the
// one job.
func TestASubstageNeedRendersAsAnEdgeBetweenItsJobs(t *testing.T) {
	t.Parallel()

	spec := phasedSpec()
	spec.Stages = []citypes.DeclaredStage{{
		Name: "release",
		Substages: []citypes.DeclaredSubstage{
			{Name: "artifacts"},
			{Name: "container", Needs: []string{"artifacts"}},
			{Name: "revision", Needs: []string{"container"}},
		},
	}}

	stageJobs := renderCI(t, spec)
	assert.Contains(t, stageJobs, "  release:\n    name: release\n    needs: [evaluate]\n")
	assert.NotContains(t, stageJobs, "release-container")

	spec.Jobs = workflowcontroller.JobsPerSubstage
	ci := renderCI(t, spec)

	for _, want := range []string{
		"  release-artifacts:\n    name: release › artifacts\n    needs: [evaluate]\n",
		"  release-container:\n    name: release › container\n    needs: [evaluate, release-artifacts]\n",
		"  release-revision:\n    name: release › revision\n    needs: [evaluate, release-container]\n",
	} {
		assert.Contains(t, ci, want)
	}

	// The first substage has nothing to bring back; the two that wait on a
	// sibling do, even though no stage runs before this one.
	first := ci[strings.Index(ci, "  release-artifacts:"):strings.Index(ci, "  release-container:")]
	assert.NotContains(t, first, "download-artifact")
	assert.Equal(t, 2, strings.Count(ci, "uses: actions/download-artifact@v8"))
}

// With no stage names handed in - an older core - the stages are one job,
// which is the shape every phased workflow had before the names crossed
// the wire.
func TestPhasesWithoutStageNamesRenderOneStagesJob(t *testing.T) {
	t.Parallel()

	spec := phasedSpec()
	spec.Stages = nil

	ci := renderCI(t, spec)
	assert.Contains(t, ci, "  stages:\n    name: Run the stages\n    needs: [evaluate]\n    if: needs.evaluate.outputs.outcome == 'proceed'\n")
	assert.Contains(t, ci, "--phase stages --revision ${{ needs.evaluate.outputs.revision }}\n")
	assert.Contains(t, ci, "name: built-${{ github.run_id }}-stages\n")
}

// A stage may not take a fixed job's name: the jobs would collide and the
// needs graph would read itself.
func TestAStageNamedLikeAFixedJobIsRefused(t *testing.T) {
	t.Parallel()

	spec := phasedSpec()
	spec.Stages = []citypes.DeclaredStage{{Name: "evaluate", Substages: []citypes.DeclaredSubstage{{Name: "default"}}}}

	_, err := workflowcontroller.RenderAll(spec)
	require.Error(t, err)
	require.Contains(t, err.Error(), `stage "evaluate" takes the name of a fixed job`)

	spec.Jobs = workflowcontroller.JobsPerSubstage
	spec.Stages = []citypes.DeclaredStage{
		{Name: "build", Substages: []citypes.DeclaredSubstage{{Name: "unit-tests"}}},
		{Name: "build-unit", Substages: []citypes.DeclaredSubstage{{Name: "tests"}}},
	}

	_, err = workflowcontroller.RenderAll(spec)
	require.Error(t, err)
	require.Contains(t, err.Error(), `two jobs would be named "build-unit-tests"`)
}

// jobs is how a command workflow is cut, and a value nobody implements is
// refused by name. The default is whole: one job running the whole apply,
// which is what every pipeline was before phases existed.
func TestJobsIsOneOfThreeCutsAndDefaultsToWhole(t *testing.T) {
	t.Parallel()

	base := map[string]any{
		"repo": "o/r", "container": map[string]any{"ref": "img"},
		"workspace": map[string]any{"bootstrapCommand": "forge clone x ."},
	}

	spec := map[string]any{"jobs": "step"}
	for k, v := range base {
		spec[k] = v
	}

	_, err := workflowcontroller.ParseSpec(spec)
	require.Error(t, err)
	require.Contains(t, err.Error(), `spec.jobs "step" is not whole, stage or substage`)

	spec["jobs"] = "substage"
	parsed, err := workflowcontroller.ParseSpec(spec)
	require.NoError(t, err)
	require.Equal(t, workflowcontroller.JobsPerSubstage, parsed.Jobs)

	delete(spec, "jobs")
	parsed, err = workflowcontroller.ParseSpec(spec)
	require.NoError(t, err)
	require.Equal(t, workflowcontroller.JobsWhole, parsed.Jobs, "the default is one whole job")

	// The key that used to switch phases on is refused naming its fold.
	spec["phases"] = true
	_, err = workflowcontroller.ParseSpec(spec)
	require.ErrorContains(t, err, "phases is no longer a key; write jobs: stage")
}

// With phases off, nothing changes: the ignore list is the only new line,
// and only when it is set.
func TestPathsIgnoreRendersOnlyWhenSet(t *testing.T) {
	t.Parallel()

	spec := phasedSpec()
	spec.Jobs = workflowcontroller.JobsWhole

	files, err := workflowcontroller.RenderAll(spec)
	require.NoError(t, err)
	assert.Contains(t, files[0].Content, "  push:\n    branches: [main]\n    paths-ignore: [\"*.md\", \"docs/**\"]\n")
	assert.Equal(t, 1, strings.Count(files[0].Content, "runs-on:"))

	spec.Workflows[0].On.Push.IgnorePaths = nil

	files, err = workflowcontroller.RenderAll(spec)
	require.NoError(t, err)
	assert.NotContains(t, files[0].Content, "paths-ignore")
}

// A job's title is what a person reads in a list of jobs, so it is derived
// from the names the pipeline already carries. A pipeline that would rather
// say something else declares a display name, and that is used verbatim.
func TestAJobIsTitledByTheFactoryWhenTheFactorySaysSo(t *testing.T) {
	t.Parallel()

	spec := phasedSpec()
	spec.Jobs = workflowcontroller.JobsPerSubstage
	spec.Stages = []citypes.DeclaredStage{
		{Name: "check", Substages: []citypes.DeclaredSubstage{{Name: "configs"}}},
		{
			Name: "publish", DisplayName: "Ship it",
			Substages: []citypes.DeclaredSubstage{
				{Name: "members"},
				{Name: "dist", DisplayName: "Cross-build every executable"},
			},
		},
	}

	ci := renderCI(t, spec)

	for _, want := range []string{
		// Derived, both halves, because this stage declares nothing.
		"  check-configs:\n    name: check › configs\n",
		// The stage's own name reaches its substages.
		"  publish-members:\n    name: Ship it › members\n",
		// A substage that says what it does says it verbatim.
		"  publish-dist:\n    name: Cross-build every executable\n",
	} {
		assert.Contains(t, ci, want)
	}
}

// A substage that declared what it reads brings back the tarballs of exactly
// the jobs that kept them, by name: the stage's job at stage granularity,
// the substage's own at substage granularity. One that declared nothing
// keeps the pattern over everything the run kept.
func TestAUsesDeclarationBringsBackOnlyTheJobsItNamed(t *testing.T) {
	t.Parallel()

	spec := phasedSpec()
	spec.Stages[2].Substages = []citypes.DeclaredSubstage{
		{Name: "default", Uses: []string{"build/default"}},
		{Name: "dist", Uses: []string{"build/default"}},
	}

	ci := renderCI(t, spec)
	publish := ci[strings.Index(ci, "  publish:"):]
	assert.Contains(t, publish, "- name: Bring back what build built\n        uses: actions/download-artifact@v8\n        with:\n          pattern: built-${{ github.run_id }}-build\n")
	assert.NotContains(t, publish, "pattern: built-${{ github.run_id }}-*")

	build := ci[strings.Index(ci, "  build:"):strings.Index(ci, "  publish:")]
	assert.Contains(t, build, "pattern: built-${{ github.run_id }}-*", "a substage that declared nothing reads everything")

	spec.Jobs = workflowcontroller.JobsPerSubstage
	ci = renderCI(t, spec)
	assert.Contains(t, ci, "pattern: built-${{ github.run_id }}-build-default\n")

	// One substage of the stage declared nothing, so at stage granularity
	// the stage reads everything.
	spec.Jobs = workflowcontroller.JobsPerStage
	spec.Stages[2].Substages[1].Uses = nil
	ci = renderCI(t, spec)
	publish = ci[strings.Index(ci, "  publish:"):]
	assert.Contains(t, publish, "pattern: built-${{ github.run_id }}-*")
}

// Every literal the renderer used to hold is the spec's now, and a spec
// that says nothing renders exactly what it did: the goldens prove the
// second half, and this proves the first by setting each key and reading
// it back out of the file.
func TestTheJobShapeIsTheSpecs(t *testing.T) {
	t.Parallel()

	spec := phasedSpec()
	spec.RunsOn = "self-hosted"
	spec.Concurrency = workflowcontroller.ConcurrencySpec{Group: "one-at-a-time", CancelInProgress: true}
	spec.Carry = workflowcontroller.CarrySpec{Compression: workflowcontroller.CompressionNone, Retention: 2}
	spec.Actions = workflowcontroller.ActionsSpec{
		Cache: "actions/cache@v9", DownloadArtifact: "actions/download-artifact@v9", UploadArtifact: "actions/upload-artifact@v9",
	}
	spec.FailureReport = workflowcontroller.FailureReportSpec{Title: "red: {{.Workflow}}", Body: "look at it"}
	spec.Workflows[0].ReportFailure = true

	ci := renderCI(t, spec)

	for _, want := range []string{
		"    runs-on: self-hosted\n",
		"concurrency:\n  group: one-at-a-time\n  cancel-in-progress: true\n",
		"uses: actions/cache/restore@v9\n",
		"uses: actions/cache/save@v9\n",
		"uses: actions/download-artifact@v9\n",
		"uses: actions/upload-artifact@v9\n",
		"tar -cf .forge-ci/carried/built-check.tar -C",
		"path: .forge-ci/carried/built-check.tar\n          retention-days: 2\n",
		"tar -xf \"$f\" -C",
		`TITLE: "red: ci"`,
		`\"body\":\"look at it\"`,
	} {
		assert.Contains(t, ci, want)
	}

	assert.NotContains(t, ci, "ubuntu-latest")
	assert.NotContains(t, ci, ".tar.gz")
	assert.NotContains(t, ci, "@v6")
}

// caches: [] is no cache at all, which is different from saying nothing.
func TestAnEmptyCachesListRendersNoCacheStep(t *testing.T) {
	t.Parallel()

	spec := phasedSpec()
	caches := []workflowcontroller.CacheSpec{}
	spec.Caches = &caches

	ci := renderCI(t, spec)
	assert.NotContains(t, ci, "actions/cache")
	assert.NotContains(t, ci, "tool store")

	// Saying nothing keeps the tool store.
	spec.Caches = nil
	ci = renderCI(t, spec)
	assert.Contains(t, ci, "      - name: Restore the tool store\n        id: tool-store\n        uses: actions/cache/restore@v6\n        with:\n          path: ~/.cache/forge\n")
	assert.Contains(t, ci, "      - name: Save the tool store\n")
}

// A key this spec does not name fails by name, and a key it used to name
// fails naming the key that carries it now.
func TestAnUnknownOrRetiredKeyIsRefusedByName(t *testing.T) {
	t.Parallel()

	base := func() map[string]any {
		return map[string]any{
			"repo": "o/r", "container": map[string]any{"ref": "img"},
			"workspace": map[string]any{"bootstrapCommand": "forge clone x ."},
			"workflows": []any{map[string]any{
				"name": "ci", "kind": "command", "command": "true", "secret": "S",
				"on": map[string]any{"events": []any{"member-pushed"}},
			}},
		}
	}

	spec := base()
	spec["runsOnn"] = "x"
	_, err := workflowcontroller.ParseSpec(spec)
	require.ErrorContains(t, err, `unknown field "runsOnn"`)

	spec = base()
	spec["containerFile"] = ".forge/toolchain-image"
	_, err = workflowcontroller.ParseSpec(spec)
	require.ErrorContains(t, err, "containerFile is no longer a key; write container: {file: ...}")

	spec = base()
	spec["container"] = "img"
	_, err = workflowcontroller.ParseSpec(spec)
	require.ErrorContains(t, err, "container is an object now")

	for old, now := range map[string]string{
		"cron": "on: {cron: ...}", "events": "on: {events: [...]}",
		"pushBranches": "on: {push: {branches: [...]}}", "pushPathsIgnore": "on: {push: {ignorePaths: [...]}}",
	} {
		spec = base()
		spec["workflows"].([]any)[0].(map[string]any)[old] = "x"
		_, err = workflowcontroller.ParseSpec(spec)
		require.ErrorContains(t, err, old+" is no longer a key; write "+now)
	}

	// YAML 1.1 reads a bare on: as the boolean true, so the key arrives as
	// "true" from an unquoted file; the refusal names the quotes.
	spec = base()
	w := spec["workflows"].([]any)[0].(map[string]any)
	w["true"] = w["on"]
	delete(w, "on")
	_, err = workflowcontroller.ParseSpec(spec)
	require.ErrorContains(t, err, `write it quoted, "on":`)

	// on.push needs branches: a push block with nothing to match starts
	// nothing, silently.
	spec = base()
	spec["workflows"].([]any)[0].(map[string]any)["on"] = map[string]any{"push": map[string]any{}}
	_, err = workflowcontroller.ParseSpec(spec)
	require.ErrorContains(t, err, "on.push needs branches")

	// And the parsed shape reaches the render: on.push.ignorePaths is the
	// platform's paths-ignore.
	spec = base()
	spec["workflows"].([]any)[0].(map[string]any)["on"] = map[string]any{
		"push": map[string]any{"branches": []any{"main"}, "ignorePaths": []any{"*.md"}},
	}
	parsed, err := workflowcontroller.ParseSpec(spec)
	require.NoError(t, err)
	files, err := workflowcontroller.RenderAll(parsed)
	require.NoError(t, err)
	assert.Contains(t, files[0].Content, "  push:\n    branches: [main]\n    paths-ignore: [\"*.md\"]\n")
}
