package clidriver_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/reconcilecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/driver/clidriver"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/clidrivermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

const minimal = `
name: demo
managers:
  - alias: local
    engine: "forge://x/cmd/m@v0.1.0"
engines:
  - alias: here
    type: compute
    engine: "forge://x/cmd/c@v0.1.0"
    manager: local
  - alias: st
    type: state
    engine: "forge://x/cmd/s@v0.1.0"
    manager: local
state: st
targets:
  - alias: build
    forge: test-all
stages:
  - name: build
    substages:
      - name: default
        engine: here
        manager: local
        targets: [build]
`

func write(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "forge-ci.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	return path
}

func passingReport() reconcilecontroller.Report {
	return reconcilecontroller.Report{
		Revision: citypes.Revision{ID: "rev123", Repos: map[string]string{"golden-rust": "abcdef0123456789"}},
		Actions:  []string{"created directory /tmp/state"},
		Stages: []reconcilecontroller.StageReport{{
			Name:    "build",
			Advance: true,
			Reason:  "all good",
			Runs: []citypes.Run{{
				Substage: "default",
				Status:   citypes.StatusPassed,
				Gates:    []citypes.GateResult{{Alias: "approve", Status: citypes.StatusPassed, Message: "approved"}},
				Forge: &citypes.ForgeResult{TestReports: []forge.TestReport{{
					Stage: "unit", Status: "passed",
					TestStats: forge.TestStats{Total: 37},
					Coverage:  forge.Coverage{Percentage: 96.4},
				}}},
			}},
		}},
	}
}

func blockedReport() reconcilecontroller.Report {
	r := passingReport()
	r.Stages[0].Advance = false
	r.Stages[0].Reason = "a gate is not satisfied"
	r.Stages[0].Runs[0].Status = citypes.StatusFailed
	r.Stages[0].Runs[0].Message = "two tests failed"

	return r
}

func TestValidateReportsTheShape(t *testing.T) {
	var out bytes.Buffer

	err := clidriver.New(&out, clidrivermock.NewMockReconciler(t)).
		Run(context.Background(), []string{"validate", "--config", write(t, minimal)})
	require.NoError(t, err)
	require.Contains(t, out.String(), "demo: 0 repos, 2 engines, 1 stages")
	require.Contains(t, out.String(), "1. build (1 substages)")
}

func TestApplyRendersTheWholeReport(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(passingReport(), nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(), []string{"apply", "--config", write(t, minimal)})
	require.NoError(t, err)

	text := out.String()
	require.Contains(t, text, "revision rev123")
	require.Contains(t, text, "golden-rust abcdef012345")
	require.Contains(t, text, "created directory /tmp/state")
	require.Contains(t, text, "stage build: all good")
	require.Contains(t, text, "default passed")
	require.Contains(t, text, "gate approve passed approved")
	require.Contains(t, text, "forge unit passed 37 tests 96.4% coverage")
}

// A superseded run exits 0. It is not a failure - nothing broke, the drift
// is corrected and pushed, and the run that correction triggers does the
// work. So the report on stdout is the only way an operator learns why no
// stage ran, which is why it leads and says both halves.
func TestASupersededApplyReportsLoudlyAndDoesNotFail(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(reconcilecontroller.Report{
		Superseded: true,
		Actions: []string{
			"converged file golden-go/.github/workflows/notify.yaml",
			"committed and pushed golden-go @ 3f1c9a2b4d5e to main",
		},
	}, nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(), []string{"apply", "--config", write(t, minimal)})
	require.NoError(t, err)

	text := out.String()
	require.Contains(t, text, "reconcile changed this pipeline's own resources and settled them")
	require.Contains(t, text, "superseded by the run those changes trigger")
	require.Contains(t, text, "converged file golden-go/.github/workflows/notify.yaml")
	require.Contains(t, text, "committed and pushed golden-go @ 3f1c9a2b4d5e to main")
	require.NotContains(t, text, "revision ",
		"no revision was resolved, so printing an empty one would read as a bug")
	require.True(t, strings.HasSuffix(text, "self-reconcile: superseded\n"),
		"the last line is the word a rendered workflow reads to stop the jobs after the reconcile; got %q", text)
}

// A self-reconcile phase that found no drift ends on the other word, on its
// last line, so a rendered workflow reads one line either way and lets the
// evaluate job run only on this one.
func TestAConvergedSelfReconcilePhaseEndsOnItsWord(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(reconcilecontroller.Report{
		Reconciliation: reconcilecontroller.ReconciliationConverged,
		Actions:        []string{"kept file golden-go/.github/workflows/notify.yaml"},
	}, nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(),
		[]string{"apply", "--config", write(t, minimal), "--phase", "self-reconcile"})
	require.NoError(t, err)

	text := out.String()
	require.Contains(t, text, "kept file golden-go/.github/workflows/notify.yaml")
	require.True(t, strings.HasSuffix(text, "self-reconcile: converged\n"), "got %q", text)
	require.NotContains(t, text, "superseded")
	require.NotContains(t, text, "revision ")
}

// A plan says so on its first line. An operator reading a wall of actions
// has to be able to tell at a glance whether they already happened, and the
// exit status cannot say it: a plan exits 0 and so does a green run.
func TestADryRunSaysItIsAPlanAndPrintsNoRevision(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, reconcilecontroller.Options{DryRun: true}).
		Return(reconcilecontroller.Report{
			Planned: true,
			Actions: []string{"would converge file golden-go/.github/workflows/notify.yaml"},
		}, nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(),
		[]string{"apply", "--config", write(t, minimal), "--dry-run"})
	require.NoError(t, err)

	text := out.String()
	require.Contains(t, text, "this is a plan, nothing was written")
	require.Contains(t, text, "would converge file golden-go/.github/workflows/notify.yaml")
	require.NotContains(t, text, "revision ")
	require.NotContains(t, text, "stage ")
}

// The empty plan has to say something. A bare "nothing was written" with no
// lines under it reads like a run that failed to look.
func TestAnEmptyPlanSaysEverythingAlreadyMatches(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Bootstrap(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(reconcilecontroller.Report{Planned: true}, nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(),
		[]string{"bootstrap", "--config", write(t, minimal), "--dry-run"})
	require.NoError(t, err)
	require.Contains(t, out.String(), "everything already matches what is declared")
}

// --force reaches the reconcile. It is one bool and it decides whether a
// second operator's bootstrap silently replaces the first one's credential.
func TestForceReachesTheReconcile(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Bootstrap(mock.Anything, mock.Anything, mock.Anything, reconcilecontroller.Options{Force: true}).
		Return(passingReport(), nil).Once()

	require.NoError(t, clidriver.New(&out, r).Run(context.Background(),
		[]string{"bootstrap", "--config", write(t, minimal), "--force"}))
}

func TestApplyFailsWhenThePipelineDidNotAdvance(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(blockedReport(), nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(), []string{"apply", "--config", write(t, minimal)})
	require.ErrorIs(t, err, clidriver.ErrBlocked)
	require.Contains(t, err.Error(), "a gate is not satisfied")
	require.Contains(t, out.String(), "default failed (two tests failed)")
}

func TestStatusNeverFailsOnABlockedPipeline(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Status(mock.Anything, mock.Anything, mock.Anything).Return(blockedReport(), nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(), []string{"status", "--config", write(t, minimal)})
	require.NoError(t, err)
}

func TestBootstrapNeverFailsOnABlockedPipeline(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Bootstrap(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(blockedReport(), nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(), []string{"bootstrap", "--config", write(t, minimal)})
	require.NoError(t, err)
}

func TestPollSaysWhenNothingMoved(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Poll(mock.Anything, mock.Anything, mock.Anything).Return(citypes.TriggerOutput{}, nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(), []string{"poll", "--config", write(t, minimal)})
	require.NoError(t, err)
	require.Equal(t, "nothing moved\n", out.String())
}

func TestPollSaysWhatMoved(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Poll(mock.Anything, mock.Anything, mock.Anything).
		Return(citypes.TriggerOutput{Changed: true, Reason: "on-change: the watched repos moved"}, nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(), []string{"poll", "--config", write(t, minimal)})
	require.NoError(t, err)
	require.Contains(t, out.String(), "changed: on-change: the watched repos moved")
}

func TestEveryVerbPropagatesAnError(t *testing.T) {
	for _, verb := range []string{"bootstrap", "apply", "status", "poll"} {
		t.Run(verb, func(t *testing.T) {
			r := clidrivermock.NewMockReconciler(t)

			switch verb {
			case "bootstrap":
				r.EXPECT().Bootstrap(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
					Return(reconcilecontroller.Report{}, errBoom).Once()
			case "apply":
				r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
					Return(reconcilecontroller.Report{}, errBoom).Once()
			case "status":
				r.EXPECT().Status(mock.Anything, mock.Anything, mock.Anything).
					Return(reconcilecontroller.Report{}, errBoom).Once()
			case "poll":
				r.EXPECT().Poll(mock.Anything, mock.Anything, mock.Anything).
					Return(citypes.TriggerOutput{}, errBoom).Once()
			}

			err := clidriver.New(&bytes.Buffer{}, r).
				Run(context.Background(), []string{verb, "--config", write(t, minimal)})
			require.ErrorIs(t, err, errBoom)
		})
	}
}

func TestTheRootDefaultsToThePipelineDirectory(t *testing.T) {
	path := write(t, minimal)

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, filepath.Dir(path), mock.Anything).Return(passingReport(), nil).Once()

	require.NoError(t, clidriver.New(&bytes.Buffer{}, r).
		Run(context.Background(), []string{"apply", "--config", path}))
}

func TestAnExplicitRootWins(t *testing.T) {
	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, "/elsewhere", mock.Anything).Return(passingReport(), nil).Once()

	require.NoError(t, clidriver.New(&bytes.Buffer{}, r).
		Run(context.Background(), []string{"apply", "--config", write(t, minimal), "--root", "/elsewhere"}))
}

func TestValidateNamesTheFileItRejected(t *testing.T) {
	path := write(t, strings.Replace(minimal, "state: st", "state: here", 1))

	err := clidriver.New(&bytes.Buffer{}, clidrivermock.NewMockReconciler(t)).
		Run(context.Background(), []string{"validate", "--config", path})
	require.Error(t, err)
	require.Contains(t, err.Error(), path)
	require.Contains(t, err.Error(), "want state")
}

func TestDuplicateKeyIsRejected(t *testing.T) {
	err := clidriver.New(&bytes.Buffer{}, clidrivermock.NewMockReconciler(t)).
		Run(context.Background(), []string{"validate", "--config", write(t, minimal+"\nstate: here\n")})
	require.Error(t, err)
	require.Contains(t, err.Error(), `key "state" already set`)
}

func TestMissingFileIsNotAValidationError(t *testing.T) {
	err := clidriver.New(&bytes.Buffer{}, clidrivermock.NewMockReconciler(t)).
		Run(context.Background(), []string{"validate", "--config", "/nope/forge-ci.yaml"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "reading /nope/forge-ci.yaml")
}

func TestNoArgsIsUsage(t *testing.T) {
	require.ErrorIs(t,
		clidriver.New(&bytes.Buffer{}, clidrivermock.NewMockReconciler(t)).Run(context.Background(), nil),
		clidriver.ErrUsage)
}

func TestUnknownSubcommandIsUsage(t *testing.T) {
	err := clidriver.New(&bytes.Buffer{}, clidrivermock.NewMockReconciler(t)).
		Run(context.Background(), []string{"deploy", "--config", write(t, minimal)})
	require.ErrorIs(t, err, clidriver.ErrUsage)
	require.Contains(t, err.Error(), "deploy")
}

func TestABadFlagIsReported(t *testing.T) {
	err := clidriver.New(&bytes.Buffer{}, clidrivermock.NewMockReconciler(t)).
		Run(context.Background(), []string{"apply", "--nope"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "parsing flags")
}

func TestAFailingWriterIsReported(t *testing.T) {
	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(passingReport(), nil).Once()

	err := clidriver.New(brokenWriter{}, r).
		Run(context.Background(), []string{"apply", "--config", write(t, minimal)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "writing report")
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errBoom }

func TestApplyRefusesToRunInsideApply(t *testing.T) {
	err := clidriver.New(&bytes.Buffer{}, clidrivermock.NewMockReconciler(t)).
		AlreadyApplying(true).
		Run(context.Background(), []string{"apply", "--config", write(t, minimal)})
	require.ErrorIs(t, err, clidriver.ErrRecurse)
	require.Contains(t, err.Error(), "the loop calling itself")
}

func TestTheOtherVerbsStillWorkInsideApply(t *testing.T) {
	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Bootstrap(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(passingReport(), nil).Once()

	require.NoError(t, clidriver.New(&bytes.Buffer{}, r).
		AlreadyApplying(true).
		Run(context.Background(), []string{"bootstrap", "--config", write(t, minimal)}))
}

func TestAFailingRunPrintsItsOutput(t *testing.T) {
	var out bytes.Buffer

	report := blockedReport()
	report.Stages[0].Runs[0].Output = "two tests failed\nsecond line\n"

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(report, nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(), []string{"apply", "--config", write(t, minimal)})
	require.ErrorIs(t, err, clidriver.ErrBlocked)
	require.Contains(t, out.String(), "    | two tests failed")
	require.Contains(t, out.String(), "    | second line")
}

func TestAPassingRunDoesNotPrintItsOutput(t *testing.T) {
	var out bytes.Buffer

	report := passingReport()
	report.Stages[0].Runs[0].Output = "lots of noise\n"

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(report, nil).Once()

	require.NoError(t, clidriver.New(&out, r).
		Run(context.Background(), []string{"apply", "--config", write(t, minimal)}))
	require.NotContains(t, out.String(), "lots of noise")
}

func TestGraphRendersMermaidFromLiveState(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Status(mock.Anything, mock.Anything, mock.Anything).Return(passingReport(), nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(), []string{"graph", "--config", write(t, minimal)})
	require.NoError(t, err)

	text := out.String()
	require.True(t, strings.HasPrefix(text, "flowchart LR\n"))
	require.Contains(t, text, `revision["revision rev123<br/>golden-rust abcdef012345"]`)
	require.Contains(t, text, `subgraph s0["build"]`)
	require.Contains(t, text, ":::passed")
	require.Contains(t, text, `done(["released"])`)
	require.Contains(t, text, "classDef passed")
}

func TestGraphShowsAFailureInRed(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Status(mock.Anything, mock.Anything, mock.Anything).Return(blockedReport(), nil).Once()

	require.NoError(t, clidriver.New(&out, r).
		Run(context.Background(), []string{"graph", "--config", write(t, minimal)}))
	require.Contains(t, out.String(), ":::failed")
}

func TestGraphMarksUnrunSubstagesPending(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Status(mock.Anything, mock.Anything, mock.Anything).
		Return(reconcilecontroller.Report{}, nil).Once()

	require.NoError(t, clidriver.New(&out, r).
		Run(context.Background(), []string{"graph", "--config", write(t, minimal)}))

	text := out.String()
	require.Contains(t, text, ":::pending")
	require.Contains(t, text, "revision none")
	require.Contains(t, text, `trigger["manual"]`)
}

func TestGraphShowsGatesAndEngines(t *testing.T) {
	var out bytes.Buffer

	withGate := strings.Replace(minimal, `        targets: [build]`,
		`        targets: [build]
        gates: [approve]`, 1)
	withGate = strings.Replace(withGate, `  - alias: st`,
		`  - alias: approve
    type: gate
    engine: "forge://x/cmd/g@v0.1.0"
    manager: local
  - alias: st`, 1)

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Status(mock.Anything, mock.Anything, mock.Anything).Return(passingReport(), nil).Once()

	require.NoError(t, clidriver.New(&out, r).
		Run(context.Background(), []string{"graph", "--config", write(t, withGate)}))

	text := out.String()
	require.Contains(t, text, "gate approve")
	require.Contains(t, text, `subgraph engines["engines and managers"]`)
	require.Contains(t, text, "<i>compute</i>")
}

func TestGraphPropagatesAnError(t *testing.T) {
	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Status(mock.Anything, mock.Anything, mock.Anything).
		Return(reconcilecontroller.Report{}, errBoom).Once()

	err := clidriver.New(&bytes.Buffer{}, r).
		Run(context.Background(), []string{"graph", "--config", write(t, minimal)})
	require.ErrorIs(t, err, errBoom)
}

// The root reaches every engine as spec["root"]. An engine joins paths from
// it that only mean one place while it is absolute: the member checkouts it
// runs git in, and the asset files it reads to upload.
//
// With --root . every join looks clean and means whatever directory the
// process happens to be in. A release then fails on a file that is on disk -
// at the last step, after the build and publish stages passed and the tags
// are already cut.
//
// --root . is the form the operator runbook prints, so this was reachable by
// following the documentation. Deriving the root from the config file
// already absolutised it; an explicit one took the other branch.
func TestTheRootReachesTheEnginesAbsolute(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "forge-ci.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(minimal), 0o600))

	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))

	t.Cleanup(func() { _ = os.Chdir(cwd) })

	for name, args := range map[string][]string{
		"a dot root":      {"apply", "--config", "forge-ci.yaml", "--root", "."},
		"a relative root": {"apply", "--config", "forge-ci.yaml", "--root", "./"},
		"no root at all":  {"apply", "--config", "forge-ci.yaml"},
		"an absolute root": {
			"apply", "--config", "forge-ci.yaml", "--root", dir,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var seen string

			r := clidrivermock.NewMockReconciler(t)
			r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, mock.Anything).
				RunAndReturn(func(
					_ context.Context, _ config.Pipeline, root string, _ reconcilecontroller.Options,
				) (reconcilecontroller.Report, error) {
					seen = root

					return passingReport(), nil
				}).Once()

			var out bytes.Buffer

			require.NoError(t, clidriver.New(&out, r).Run(context.Background(), args))
			require.True(t, filepath.IsAbs(seen),
				"engines join paths against this from a working directory nobody promises; got %q", seen)
		})
	}
}

// A skipped run says why on its own line, runs no stage, exits 0, and ends
// on the one word a rendered workflow reads. An intent phase that found
// work ends on the other word.
func TestASkippedApplyReportsTheReasonAndTheWord(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, reconcilecontroller.Options{Phase: "evaluate"}).
		Return(reconcilecontroller.Report{
			Revision:   citypes.Revision{ID: "abc123def456"},
			Version:    "v0.5.6",
			Skipped:    true,
			Reason:     "Nothing to release. No repo in the release set has a new commit since v0.5.6.",
			Evaluation: reconcilecontroller.EvaluationSkip,
		}, nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(),
		[]string{"apply", "--config", write(t, minimal), "--phase", "evaluate"})
	require.NoError(t, err)
	require.Contains(t, out.String(), "skipped: Nothing to release. No repo in the release set has a new commit since v0.5.6.")
	require.True(t, strings.HasSuffix(out.String(), "evaluate: skip\n"), out.String())

	out.Reset()
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, reconcilecontroller.Options{Phase: "evaluate"}).
		Return(reconcilecontroller.Report{
			Revision:   citypes.Revision{ID: "abc123def456"},
			Version:    "v0.5.7",
			Evaluation: reconcilecontroller.EvaluationProceed,
		}, nil).Once()

	err = clidriver.New(&out, r).Run(context.Background(),
		[]string{"apply", "--config", write(t, minimal), "--phase", "evaluate"})
	require.NoError(t, err)
	require.True(t, strings.HasSuffix(out.String(), "evaluate: proceed\n"), out.String())
}

// A phase nobody implements is refused by name, before anything runs.
func TestAnUnknownPhaseIsRefused(t *testing.T) {
	var out bytes.Buffer

	err := clidriver.New(&out, clidrivermock.NewMockReconciler(t)).Run(context.Background(),
		[]string{"apply", "--config", write(t, minimal), "--phase", "deploy"})
	require.ErrorIs(t, err, clidriver.ErrUsage)
	require.Contains(t, err.Error(), "self-reconcile, evaluate, stages")
}

// A workflow rendered by an earlier forge-ci still names the phases it
// knew. Its first job must reach the self reconcile under the old spelling,
// or the workflow can never re-render itself.
func TestARetiredPhaseNameStillReachesItsPhase(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, reconcilecontroller.Options{Phase: "self-reconcile"}).
		Return(reconcilecontroller.Report{}, nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(),
		[]string{"apply", "--config", write(t, minimal), "--phase", "reconcile"})
	require.NoError(t, err)
}

// A stage job is a stages phase narrowed by name, and the flags that narrow
// it need each other in one order: --stage needs the stages phase, and
// --substage needs --stage.
func TestTheStageFlagsNeedEachOther(t *testing.T) {
	for name, args := range map[string][]string{
		"stage without the phase":  {"--stage", "build"},
		"stage on another phase":   {"--phase", "release", "--stage", "build"},
		"substage without a stage": {"--phase", "stages", "--substage", "default"},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer

			err := clidriver.New(&out, clidrivermock.NewMockReconciler(t)).Run(context.Background(),
				append([]string{"apply", "--config", write(t, minimal)}, args...))
			require.ErrorIs(t, err, clidriver.ErrUsage)
		})
	}

	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, reconcilecontroller.Options{
		Phase: "stages", Stage: "build", Substage: "default",
	}).Return(reconcilecontroller.Report{Revision: citypes.Revision{ID: "abc"}}, nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(),
		[]string{"apply", "--config", write(t, minimal), "--phase", "stages", "--stage", "build", "--substage", "default"})
	require.NoError(t, err)
}

// A run proves one set of commits, and only the phases after the evaluate one
// can be told which: the evaluate phase decides it, and a whole apply holds it
// from its first stage to its last. So --revision reaches the stages phase and
// is refused everywhere else.
func TestTheRevisionFlagBelongsToThePhasesAfterTheEvaluation(t *testing.T) {
	for name, args := range map[string][]string{
		"a whole apply":  {"--revision", "abc123"},
		"the reconcile":  {"--phase", "self-reconcile", "--revision", "abc123"},
		"the evaluation": {"--phase", "evaluate", "--revision", "abc123"},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer

			err := clidriver.New(&out, clidrivermock.NewMockReconciler(t)).Run(context.Background(),
				append([]string{"apply", "--config", write(t, minimal)}, args...))
			require.ErrorIs(t, err, clidriver.ErrUsage)
		})
	}

	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything, reconcilecontroller.Options{
		Phase: "stages", Revision: "abc123",
	}).Return(reconcilecontroller.Report{Revision: citypes.Revision{ID: "abc123"}}, nil).Once()

	require.NoError(t, clidriver.New(&out, r).Run(context.Background(),
		[]string{"apply", "--config", write(t, minimal), "--phase", "stages", "--revision", "abc123"}))
}
