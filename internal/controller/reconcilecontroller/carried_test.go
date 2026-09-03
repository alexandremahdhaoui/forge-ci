package reconcilecontroller_test

import (
	"context"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/reconcilecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
	"github.com/stretchr/testify/require"
)

// forge-self run 35, reproduced.
//
// The build stage cross-compiled the executables and the stage after it
// packaged them. In one process that works: the files are still on the disk
// that wrote them. In jobs of their own it did not, and the package stage
// died on a glob that matched nothing, because nothing had ever brought the
// build's output to the runner that packages it.
func TestAStageJobReadsWhatTheStageBeforeItBuilt(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["build/default"] = citypes.RunOutput{
		Status: citypes.StatusPassed,
		Forge: &citypes.ForgeResult{Artifacts: []forge.Artifact{
			{Name: "tool", Type: "binary", Location: "build/bin/tool_linux_amd64"},
		}},
	}

	p := pipeline(
		stage("build", substage("default", []string{"build"})),
		stage("package", substage("default", []string{"build"})),
	)

	apply := func(opts reconcilecontroller.Options) (reconcilecontroller.Report, error) {
		return reconcilecontroller.New(f.caller(), gitAt(t, "abc123", "v0.1.9"), clock()).
			Apply(context.Background(), p, "/work", opts)
	}

	_, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseEvaluate})
	require.NoError(t, err)

	_, err = apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "build"})
	require.NoError(t, err)
	require.Empty(t, f.restored, "the first stage has nothing before it to read")

	_, err = apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "package"})
	require.NoError(t, err)
	require.Equal(t, []string{"build/bin/tool_linux_amd64"}, f.restored,
		"the package stage reads what the build stage made")
}

// A promotion job reads run records and runs no target, so it needs no
// files and asks for none: the download would cost a stage's output in
// bandwidth for a job that opens none of it.
func TestAPromotionJobCarriesNothing(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["build/default"] = citypes.RunOutput{
		Status: citypes.StatusPassed,
		Forge: &citypes.ForgeResult{Artifacts: []forge.Artifact{
			{Name: "tool", Type: "binary", Location: "build/bin/tool_linux_amd64"},
		}},
	}

	p := pipeline(
		stage("build", substage("default", []string{"build"})),
		stage("package", substage("default", []string{"build"})),
	)

	apply := func(opts reconcilecontroller.Options) (reconcilecontroller.Report, error) {
		return reconcilecontroller.New(f.caller(), gitAt(t, "abc123", "v0.1.9"), clock()).
			Apply(context.Background(), p, "/work", opts)
	}

	_, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseEvaluate})
	require.NoError(t, err)

	_, err = apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "build"})
	require.NoError(t, err)

	_, err = apply(reconcilecontroller.Options{
		Phase: reconcilecontroller.PhaseStages, Stage: "package", Substage: "default",
	})
	require.NoError(t, err)
	require.NotEmpty(t, f.restored, "a substage job runs targets, so it reads")

	f.restored = nil

	_, err = apply(reconcilecontroller.Options{
		Phase: reconcilecontroller.PhaseStages, Stage: "package", Promote: true,
	})
	require.NoError(t, err)
	require.Empty(t, f.restored)
}
