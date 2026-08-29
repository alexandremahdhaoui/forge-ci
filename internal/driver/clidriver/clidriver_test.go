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
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything).Return(passingReport(), nil).Once()

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

func TestApplyFailsWhenThePipelineDidNotAdvance(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything).Return(blockedReport(), nil).Once()

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
	r.EXPECT().Bootstrap(mock.Anything, mock.Anything, mock.Anything).Return(blockedReport(), nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(), []string{"bootstrap", "--config", write(t, minimal)})
	require.NoError(t, err)
}

func TestPollSaysWhenNothingMoved(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Poll(mock.Anything, mock.Anything).Return(citypes.TriggerOutput{}, nil).Once()

	err := clidriver.New(&out, r).Run(context.Background(), []string{"poll", "--config", write(t, minimal)})
	require.NoError(t, err)
	require.Equal(t, "nothing moved\n", out.String())
}

func TestPollSaysWhatMoved(t *testing.T) {
	var out bytes.Buffer

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Poll(mock.Anything, mock.Anything).
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
				r.EXPECT().Bootstrap(mock.Anything, mock.Anything, mock.Anything).
					Return(reconcilecontroller.Report{}, errBoom).Once()
			case "apply":
				r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything).
					Return(reconcilecontroller.Report{}, errBoom).Once()
			case "status":
				r.EXPECT().Status(mock.Anything, mock.Anything, mock.Anything).
					Return(reconcilecontroller.Report{}, errBoom).Once()
			case "poll":
				r.EXPECT().Poll(mock.Anything, mock.Anything).
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
	r.EXPECT().Apply(mock.Anything, mock.Anything, filepath.Dir(path)).Return(passingReport(), nil).Once()

	require.NoError(t, clidriver.New(&bytes.Buffer{}, r).
		Run(context.Background(), []string{"apply", "--config", path}))
}

func TestAnExplicitRootWins(t *testing.T) {
	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, "/elsewhere").Return(passingReport(), nil).Once()

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
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything).Return(passingReport(), nil).Once()

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
	require.Contains(t, err.Error(), "use forgeCI: bootstrap for a self stage")
}

func TestTheOtherVerbsStillWorkInsideApply(t *testing.T) {
	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Bootstrap(mock.Anything, mock.Anything, mock.Anything).Return(passingReport(), nil).Once()

	require.NoError(t, clidriver.New(&bytes.Buffer{}, r).
		AlreadyApplying(true).
		Run(context.Background(), []string{"bootstrap", "--config", write(t, minimal)}))
}

func TestAFailingRunPrintsItsOutput(t *testing.T) {
	var out bytes.Buffer

	report := blockedReport()
	report.Stages[0].Runs[0].Output = "two tests failed\nsecond line\n"

	r := clidrivermock.NewMockReconciler(t)
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything).Return(report, nil).Once()

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
	r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything).Return(report, nil).Once()

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

// The root reaches every engine as spec["root"]. The release engine builds
// two things from it that only agree while it is absolute: the directory gh
// runs in, <root>/<releaseIn>, and the asset paths gh is asked to upload.
//
// With --root . both joins look clean and mean different places, because gh
// runs one level down. A release then fails on "no matches found" for a file
// that is on disk - at the last step, after the build and publish stages
// passed and the tags are already cut.
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
			r.EXPECT().Apply(mock.Anything, mock.Anything, mock.Anything).
				RunAndReturn(func(_ context.Context, _ config.Pipeline, root string) (reconcilecontroller.Report, error) {
					seen = root

					return passingReport(), nil
				}).Once()

			var out bytes.Buffer

			require.NoError(t, clidriver.New(&out, r).Run(context.Background(), args))
			require.True(t, filepath.IsAbs(seen),
				"engines join paths against this and gh runs one level down; got %q", seen)
		})
	}
}
