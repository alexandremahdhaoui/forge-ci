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

// A substage job runs targets, so it reads what the stages before it built.
func TestASubstageJobCarriesWhatTheStagesBeforeItBuilt(t *testing.T) {
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
}

// A substage that declares what it reads brings back exactly that. Two
// stages built before this one; the substage names one of them, and the
// other's files stay where they are - a job carries what it named, not
// everything the run has kept.
func TestASubstageCarriesOnlyWhatItUses(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["build/default"] = citypes.RunOutput{
		Status: citypes.StatusPassed,
		Forge: &citypes.ForgeResult{Artifacts: []forge.Artifact{
			{Name: "tool", Type: "binary", Location: "build/bin/tool_linux_amd64"},
		}},
	}
	f.runOutputs["docs/default"] = citypes.RunOutput{
		Status: citypes.StatusPassed,
		Forge: &citypes.ForgeResult{Artifacts: []forge.Artifact{
			{Name: "site", Type: "binary", Location: "docs/build/site_linux_amd64"},
		}},
	}

	packager := substage("default", []string{"build"})
	packager.Uses = []string{"build/default"}

	p := pipeline(
		stage("build", substage("default", []string{"build"})),
		stage("docs", substage("default", []string{"build"})),
		stage("package", packager),
	)

	apply := func(opts reconcilecontroller.Options) (reconcilecontroller.Report, error) {
		return reconcilecontroller.New(f.caller(), gitAt(t, "abc123", "v0.1.9"), clock()).
			Apply(context.Background(), p, "/work", opts)
	}

	_, err := apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseEvaluate})
	require.NoError(t, err)

	for _, name := range []string{"build", "docs"} {
		_, err = apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: name})
		require.NoError(t, err)
	}

	// The docs stage declared nothing, so it read the build's tool; that is
	// its business. What matters is what the package stage brings back.
	f.restored = nil

	_, err = apply(reconcilecontroller.Options{Phase: reconcilecontroller.PhaseStages, Stage: "package"})
	require.NoError(t, err)
	require.Equal(t, []string{"build/bin/tool_linux_amd64"}, f.restored,
		"the package stage reads what it named and nothing the docs stage made")
}
