package workflowcontroller_test

import (
	"strings"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/workflowcontroller"
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

// One apply rendered as four jobs, each running one phase, so the run reads
// as what it is. The intent job's last line is its output, and the two jobs
// after it run only on proceed - GitHub's own skipped state for a revision
// with nothing to release.
func TestPhasesRenderAsFourJobsGatedOnTheIntent(t *testing.T) {
	t.Parallel()

	files, err := workflowcontroller.RenderAll(phasedSpec())
	require.NoError(t, err)

	var ci string
	for _, f := range files {
		if f.Name == "ci" {
			ci = f.Content
		}
	}

	require.NotEmpty(t, ci)

	for _, want := range []string{
		"  reconcile:\n",
		"  intent:\n    needs: [reconcile]\n    outputs:\n      outcome: ${{ steps.phase.outputs.outcome }}\n",
		"  stages:\n    needs: [intent]\n    if: needs.intent.outputs.outcome == 'proceed'\n",
		"  release:\n    needs: [intent, stages]\n    if: needs.intent.outputs.outcome == 'proceed'\n",
		"forge-ci apply --config forge-ci.yaml --root . --phase reconcile",
		"--phase intent)",
		"sed -n 's/^intent: //p' | tail -n 1",
		"forge-ci apply --config forge-ci.yaml --root . --phase stages",
		"forge-ci apply --config forge-ci.yaml --root . --phase release",
		"uses: actions/upload-artifact@v4",
		"uses: actions/download-artifact@v4",
		"path: .forge-ci/artifacts",
		"    paths-ignore: [\"*.md\", \"docs/**\"]\n",
	} {
		assert.Contains(t, ci, want)
	}

	// Every job stands the workspace up inside the container: four
	// checkouts, four images, no toolchain install.
	assert.Equal(t, 4, strings.Count(ci, "image: ghcr.io/o/toolchain:v1"))
	assert.Equal(t, 4, strings.Count(ci, "forge clone git@github.com:o/r.git ."))
	assert.NotContains(t, ci, "Install the toolchain")
	assert.Equal(t, 1, strings.Count(ci, "jobs:\n"))
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
