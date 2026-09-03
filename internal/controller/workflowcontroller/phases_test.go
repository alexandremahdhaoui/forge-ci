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
		Container: "ghcr.io/o/toolchain:v1",
		Workspace: workflowcontroller.Workspace{BootstrapCommand: "forge clone git@github.com:o/r.git ."},
		Phases:    true,
		Jobs:      workflowcontroller.JobsPerStage,
		Stages: []citypes.DeclaredStage{
			{Name: "check", Substages: []citypes.DeclaredSubstage{{Name: "default"}}},
			{Name: "build", Substages: []citypes.DeclaredSubstage{{Name: "default"}}},
			{Name: "publish", Substages: []citypes.DeclaredSubstage{{Name: "default"}, {Name: "dist"}}},
		},
		Workflows: []workflowcontroller.WorkflowSpec{{
			Name:            "ci",
			Kind:            workflowcontroller.KindCommand,
			PushBranches:    []string{"main"},
			PushPathsIgnore: []string{"*.md", "docs/**"},
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

// One apply rendered as jobs: the two fixed ones, one per stage in the
// pipeline's order, and the release. The evaluate job's last line is its
// output, and every job after it runs only on proceed - GitHub's own skipped
// state for a revision with nothing to release.
func TestPhasesRenderOneJobPerStageGatedOnTheEvaluation(t *testing.T) {
	t.Parallel()

	ci := renderCI(t, phasedSpec())

	for _, want := range []string{
		"  self-reconcile:\n    name: Reconcile CI resources\n",
		"  evaluate:\n    name: Evaluate next steps\n    needs: [self-reconcile]\n    outputs:\n      outcome: ${{ steps.phase.outputs.outcome }}\n" +
			"      revision: ${{ steps.phase.outputs.revision }}\n",
		"  check:\n    name: check\n    needs: [evaluate]\n    if: needs.evaluate.outputs.outcome == 'proceed'\n",
		"  build:\n    name: build\n    needs: [evaluate, check]\n    if: needs.evaluate.outputs.outcome == 'proceed'\n",
		"  publish:\n    name: publish\n    needs: [evaluate, build]\n    if: needs.evaluate.outputs.outcome == 'proceed'\n",
		"  release:\n    name: Release\n    needs: [evaluate, publish]\n    if: needs.evaluate.outputs.outcome == 'proceed'\n",
		"- name: Reconcile CI resources\n",
		"- name: Evaluate next steps\n",
		"- name: Run stage check\n",
		"- name: Run stage publish\n",
		"- name: Release\n",
		"forge-ci apply --config forge-ci.yaml --root . --phase self-reconcile",
		"--phase evaluate)",
		"sed -n 's/^evaluate: //p' | tail -n 1",
		"forge-ci apply --config forge-ci.yaml --root . --phase stages --stage check --revision ${{ needs.evaluate.outputs.revision }}\n",
		"forge-ci apply --config forge-ci.yaml --root . --phase stages --stage publish --revision ${{ needs.evaluate.outputs.revision }}\n",
		"forge-ci apply --config forge-ci.yaml --root . --phase release --revision ${{ needs.evaluate.outputs.revision }}",
		"name: built-${{ github.run_id }}-check\n",
		"name: built-${{ github.run_id }}-publish\n",
		"pattern: built-${{ github.run_id }}-*\n          merge-multiple: true\n",
		"path: .forge-ci/artifacts",
		"    paths-ignore: [\"*.md\", \"docs/**\"]\n",
	} {
		assert.Contains(t, ci, want)
	}

	assert.NotContains(t, ci, "--substage")
	assert.NotContains(t, ci, "--promote")

	// Every job stands the workspace up inside the container: six
	// checkouts, six images, no toolchain install.
	assert.Equal(t, 6, strings.Count(ci, "image: ghcr.io/o/toolchain:v1"))
	assert.Equal(t, 6, strings.Count(ci, "forge clone git@github.com:o/r.git ."))
	assert.Equal(t, 3, strings.Count(ci, "uses: actions/upload-artifact@v4"))
	// Three stage jobs and the release: a stage reads what the stage before
	// it built, not only the release.
	assert.Equal(t, 4, strings.Count(ci, "uses: actions/download-artifact@v4"))
	assert.NotContains(t, ci, "Install the toolchain")
	assert.Equal(t, 1, strings.Count(ci, "jobs:\n"))
}

// jobs: substage cuts each stage into one job per substage, running beside
// each other, and one promotion job that needs them all. The next stage
// needs the promotion, and so does the release.
func TestJobsPerSubstageRenderAPromotionJobPerStage(t *testing.T) {
	t.Parallel()

	spec := phasedSpec()
	spec.Jobs = workflowcontroller.JobsPerSubstage

	ci := renderCI(t, spec)

	for _, want := range []string{
		"  check-default:\n    name: check › default\n    needs: [evaluate]\n    if: needs.evaluate.outputs.outcome == 'proceed'\n",
		"  check-promotion-gate:\n    name: check › promotion gate\n    needs: [evaluate, check-default]\n    if: needs.evaluate.outputs.outcome == 'proceed'\n",
		"  build-default:\n    name: build › default\n    needs: [evaluate, check-promotion-gate]\n",
		"  publish-default:\n    name: publish › default\n    needs: [evaluate, build-promotion-gate]\n",
		"  publish-dist:\n    name: publish › dist\n    needs: [evaluate, build-promotion-gate]\n",
		"  publish-promotion-gate:\n    name: publish › promotion gate\n    needs: [evaluate, publish-default, publish-dist]\n",
		"  release:\n    name: Release\n    needs: [evaluate, publish-promotion-gate]\n",
		"- name: Run publish dist\n",
		"- name: Promotion gate for stage publish\n",
		"--phase stages --stage publish --substage dist --revision ${{ needs.evaluate.outputs.revision }}\n",
		"--phase stages --stage publish --promote --revision ${{ needs.evaluate.outputs.revision }}\n",
		"name: built-${{ github.run_id }}-publish-dist\n",
	} {
		assert.Contains(t, ci, want)
	}

	// Four substage jobs upload; the three promotion jobs build nothing.
	assert.Equal(t, 4, strings.Count(ci, "uses: actions/upload-artifact@v4"))
	// The same four download, and so does the release. A promotion gate
	// reads records and runs no target, so it needs no files.
	assert.Equal(t, 5, strings.Count(ci, "uses: actions/download-artifact@v4"))
	gate := ci[strings.Index(ci, "  publish-promotion-gate:"):strings.Index(ci, "  release:")]
	assert.NotContains(t, gate, "download-artifact")
	assert.Equal(t, 10, strings.Count(ci, "forge clone git@github.com:o/r.git ."))
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
	assert.Contains(t, ci, "  release:\n    name: Release\n    needs: [evaluate, stages]\n")
	assert.Contains(t, ci, "--phase stages --revision ${{ needs.evaluate.outputs.revision }}\n")
	assert.Contains(t, ci, "name: built-${{ github.run_id }}-stages\n")
}

// A stage may not take a fixed job's name: the jobs would collide and the
// needs graph would read itself.
func TestAStageNamedLikeAFixedJobIsRefused(t *testing.T) {
	t.Parallel()

	spec := phasedSpec()
	spec.Stages = []citypes.DeclaredStage{{Name: "release", Substages: []citypes.DeclaredSubstage{{Name: "default"}}}}

	_, err := workflowcontroller.RenderAll(spec)
	require.Error(t, err)
	require.Contains(t, err.Error(), `stage "release" takes the name of a fixed job`)

	spec.Jobs = workflowcontroller.JobsPerSubstage
	spec.Stages = []citypes.DeclaredStage{
		{Name: "build", Substages: []citypes.DeclaredSubstage{{Name: "promotion-gate"}}},
		{Name: "build-promotion-gate", Substages: []citypes.DeclaredSubstage{{Name: "x"}}},
	}

	_, err = workflowcontroller.RenderAll(spec)
	require.Error(t, err)
	require.Contains(t, err.Error(), `two jobs would be named "build-promotion-gate"`)
}

// jobs is a knob on a phased workflow and nothing else; a value nobody
// implements is refused by name.
func TestJobsNeedsPhasesAndAKnownValue(t *testing.T) {
	t.Parallel()

	base := map[string]any{
		"repo": "o/r", "container": "img",
		"workspace": map[string]any{"bootstrapCommand": "forge clone x ."},
	}

	spec := map[string]any{"jobs": "substage"}
	for k, v := range base {
		spec[k] = v
	}

	_, err := workflowcontroller.ParseSpec(spec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "spec.phases: true")

	spec["phases"] = true
	spec["jobs"] = "step"
	_, err = workflowcontroller.ParseSpec(spec)
	require.Error(t, err)
	require.Contains(t, err.Error(), `spec.jobs "step" is not stage or substage`)

	spec["jobs"] = "substage"
	parsed, err := workflowcontroller.ParseSpec(spec)
	require.NoError(t, err)
	require.Equal(t, workflowcontroller.JobsPerSubstage, parsed.Jobs)

	delete(spec, "jobs")
	parsed, err = workflowcontroller.ParseSpec(spec)
	require.NoError(t, err)
	require.Equal(t, workflowcontroller.JobsPerStage, parsed.Jobs, "the default is one job per stage")
}

// With phases off, nothing changes: the ignore list is the only new line,
// and only when it is set.
func TestPathsIgnoreRendersOnlyWhenSet(t *testing.T) {
	t.Parallel()

	spec := phasedSpec()
	spec.Phases = false

	files, err := workflowcontroller.RenderAll(spec)
	require.NoError(t, err)
	assert.Contains(t, files[0].Content, "  push:\n    branches: [main]\n    paths-ignore: [\"*.md\", \"docs/**\"]\n")
	assert.Equal(t, 1, strings.Count(files[0].Content, "runs-on:"))

	spec.Workflows[0].PushPathsIgnore = nil

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
		"  check-promotion-gate:\n    name: check › promotion gate\n",
		// The stage's own name reaches its substages and its gate.
		"  publish-members:\n    name: Ship it › members\n",
		"  publish-promotion-gate:\n    name: Ship it › promotion gate\n",
		// A substage that says what it does says it verbatim.
		"  publish-dist:\n    name: Cross-build every executable\n",
	} {
		assert.Contains(t, ci, want)
	}
}
