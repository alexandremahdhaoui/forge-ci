package reconcilecontroller_test

import (
	"context"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/reconcilecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
	"github.com/stretchr/testify/require"
)

// needing is a substage that waits on others of its stage.
func needing(name string, needs ...string) config.Substage {
	sub := substage(name, []string{"build"})
	sub.Needs = needs

	return sub
}

// Substages of one stage run at the same time unless one says otherwise.
// A substage that names what it needs runs after it, and the stage still
// advances as one.
func TestASubstageRunsAfterWhatItNeeds(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(stage("release",
		substage("artifacts", []string{"build"}),
		needing("container", "artifacts"),
		needing("revision", "container"),
	))

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock()).
		Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.True(t, report.Stages[0].Advance)
	require.Len(t, report.Stages[0].Runs, 3)

	// Three waves of one: the peak concurrency the fake saw is one, where
	// three substages with no needs would have run together.
	require.Equal(t, 1, f.peak, "each substage waited for the one it needs")
	require.Equal(t, 3, f.counted(call{uriCompute, "run"}))
}

// Nothing declared, nothing changes: the substages of a stage run together,
// which is what every pipeline in the fleet relies on.
func TestSubstagesWithNoNeedsStillRunTogether(t *testing.T) {
	f := newFakeEngines(t)
	p := pipeline(stage("test",
		substage("unit", []string{"build"}),
		substage("lint", []string{"build"}),
		substage("gates", []string{"build"}),
	))

	_, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock()).
		Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err)
	require.Equal(t, 3, f.peak, "no need declared: one wave of everything")
}

// A substage whose need did not pass is not run, and a failed record stands
// in its place, so the promotion sees the whole stage rather than the half
// that happened to run. The stage does not advance.
func TestASubstageWhoseNeedFailedIsNotRun(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["release/artifacts"] = citypes.RunOutput{Status: citypes.StatusFailed, Message: "boom"}

	p := pipeline(stage("release",
		substage("artifacts", []string{"build"}),
		needing("container", "artifacts"),
	))

	report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock()).
		Apply(context.Background(), p, "/work", plain)
	require.NoError(t, err, "a failing substage is a report, never an error")
	require.False(t, report.Stages[0].Advance)
	require.Len(t, report.Stages[0].Runs, 2)

	held := report.Stages[0].Runs[1]
	require.Equal(t, "container", held.Substage)
	require.Equal(t, citypes.StatusFailed, held.Status)
	require.Contains(t, held.Message, `needs "artifacts", which did not pass`)
	require.Equal(t, 1, f.counted(call{uriCompute, "run"}), "the held substage never ran")
}

// Golden run 50, reproduced. A run of a revision that already passed reads
// the build's record instead of running it - and on a fresh runner the
// files that record names are not there, so the stage after fails on a
// file nothing carried. A record is reusable only when what it built is at
// hand; otherwise the substage runs again.
func TestAReusedRecordWhoseFilesAreGoneRunsAgain(t *testing.T) {
	f := newFakeEngines(t)
	f.runOutputs["build/default"] = citypes.RunOutput{
		Status: citypes.StatusPassed,
		Forge: &citypes.ForgeResult{Artifacts: []forge.Artifact{
			{Name: "tool", Type: "binary", Location: "build/bin/tool_linux_amd64"},
		}},
	}

	p := pipeline(stage("build", substage("default", []string{"build"})))

	apply := func() reconcilecontroller.Report {
		report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock()).
			Apply(context.Background(), p, "/work", plain)
		require.NoError(t, err)

		return report
	}

	apply()
	require.Equal(t, 1, f.counted(call{uriCompute, "run"}))

	// Same revision, same machine: the record is reused and nothing runs.
	second := apply()
	require.Equal(t, 1, second.Stages[0].Reused)
	require.Equal(t, 1, f.counted(call{uriCompute, "run"}))

	// Same revision, a machine that never put the files: the record is
	// not reused, the substage runs, and its output is put again.
	f.missing = map[string]bool{"build/bin/tool_linux_amd64": true}

	third := apply()
	require.Equal(t, 0, third.Stages[0].Reused)
	require.Equal(t, 2, f.counted(call{uriCompute, "run"}), "the build ran again for the files")
	require.True(t, third.Stages[0].Advance)
}

// A record that built nothing has nothing to carry, so it is reused wherever
// it is read: a lint that passed once for this revision passed.
func TestARecordThatBuiltNothingIsReusedAnywhere(t *testing.T) {
	f := newFakeEngines(t)
	f.missing = map[string]bool{"anything": true}

	p := pipeline(stage("test", substage("lint", []string{"build"})))

	apply := func() reconcilecontroller.Report {
		report, err := reconcilecontroller.New(f.caller(), gitAt(t, "abc123"), clock()).
			Apply(context.Background(), p, "/work", plain)
		require.NoError(t, err)

		return report
	}

	apply()
	second := apply()
	require.Equal(t, 1, second.Stages[0].Reused)
	require.Equal(t, 1, f.counted(call{uriCompute, "run"}))
	require.Equal(t, 0, f.counted(call{uriCompute, "get"}), "nothing to ask for")
}
