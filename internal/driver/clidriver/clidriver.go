package clidriver

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/reconcilecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
)

const DefaultPath = "pipeline.yaml"

const EnvInApply = "FORGE_CI_IN_APPLY"

var (
	ErrUsage   = errors.New("usage: forge-ci <bootstrap|apply|status|poll|graph|validate> [--config path] [--root dir]")
	ErrBlocked = errors.New("the pipeline did not advance")
	ErrRecurse = errors.New("apply cannot run inside apply")
)

type Reconciler interface {
	Bootstrap(context.Context, config.Pipeline, string) (reconcilecontroller.Report, error)
	Apply(context.Context, config.Pipeline, string) (reconcilecontroller.Report, error)
	Status(context.Context, config.Pipeline, string) (reconcilecontroller.Report, error)
	Poll(context.Context, config.Pipeline) (citypes.TriggerOutput, error)
}

type Driver struct {
	out        io.Writer
	reconciler Reconciler
	applying   bool
}

func New(out io.Writer, reconciler Reconciler) *Driver {
	return &Driver{out: out, reconciler: reconciler}
}

func (d *Driver) AlreadyApplying(applying bool) *Driver {
	d.applying = applying

	return d
}

func (d *Driver) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return ErrUsage
	}

	verb := args[0]

	p, root, err := d.load(verb, args[1:])
	if err != nil {
		return err
	}

	switch verb {
	case "validate":
		return d.write(describe(p))
	case "bootstrap":
		return d.reportOf(func() (reconcilecontroller.Report, error) {
			return d.reconciler.Bootstrap(ctx, p, root)
		}, false)
	case "apply":
		if d.applying {
			return fmt.Errorf(
				"%w: a stage ran forge-ci apply. use forgeCI: bootstrap for a self stage, "+
					"which reconciles the pipeline without running it again", ErrRecurse)
		}

		return d.reportOf(func() (reconcilecontroller.Report, error) {
			return d.reconciler.Apply(ctx, p, root)
		}, true)
	case "status":
		return d.reportOf(func() (reconcilecontroller.Report, error) {
			return d.reconciler.Status(ctx, p, root)
		}, false)
	case "graph":
		report, err := d.reconciler.Status(ctx, p, root)
		if err != nil {
			return err
		}

		return d.write(mermaid(p, report))
	case "poll":
		out, err := d.reconciler.Poll(ctx, p)
		if err != nil {
			return err
		}

		if !out.Changed {
			return d.write("nothing moved\n")
		}

		return d.write("changed: " + out.Reason + "\n")
	default:
		return fmt.Errorf("unknown subcommand %q: %w", verb, ErrUsage)
	}
}

func (d *Driver) load(verb string, args []string) (config.Pipeline, string, error) {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(d.out)

	path := fs.String("config", DefaultPath, "path to the pipeline file")
	root := fs.String("root", "", "directory holding the repos, defaults to the pipeline file's parent")

	if err := fs.Parse(args); err != nil {
		return config.Pipeline{}, "", fmt.Errorf("parsing flags: %w", err)
	}

	p, err := Load(*path)
	if err != nil {
		return config.Pipeline{}, "", err
	}

	if *root == "" {
		abs, err := filepath.Abs(*path)
		if err != nil {
			return config.Pipeline{}, "", fmt.Errorf("resolving %s: %w", *path, err)
		}

		*root = filepath.Dir(abs)
	}

	return p, *root, nil
}

func (d *Driver) reportOf(
	run func() (reconcilecontroller.Report, error),
	failWhenBlocked bool,
) error {
	report, err := run()
	if err != nil {
		return err
	}

	if err := d.write(render(report)); err != nil {
		return err
	}

	if failWhenBlocked && !report.Advanced() {
		return fmt.Errorf("%w: %s", ErrBlocked, blockedReason(report))
	}

	return nil
}

func (d *Driver) write(s string) error {
	if _, err := io.WriteString(d.out, s); err != nil {
		return fmt.Errorf("writing report: %w", err)
	}

	return nil
}

func describe(p config.Pipeline) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s: %d repos, %d engines, %d stages\n",
		p.Name, len(p.Repos), len(p.Engines), len(p.Stages))

	for i, s := range p.Stages {
		fmt.Fprintf(&b, "  %d. %s (%d substages)\n", i+1, s.Name, len(s.Substages))
	}

	return b.String()
}

func render(report reconcilecontroller.Report) string {
	var b strings.Builder

	fmt.Fprintf(&b, "revision %s\n", report.Revision.ID)

	for name, sha := range report.Revision.Repos {
		fmt.Fprintf(&b, "  %s %s\n", name, short(sha))
	}

	for _, action := range report.Actions {
		fmt.Fprintf(&b, "  %s\n", action)
	}

	for _, stage := range report.Stages {
		fmt.Fprintf(&b, "stage %s: %s\n", stage.Name, stage.Reason)

		for _, run := range stage.Runs {
			fmt.Fprintf(&b, "  %s %s", run.Substage, run.Status)

			if run.Message != "" {
				fmt.Fprintf(&b, " (%s)", run.Message)
			}

			fmt.Fprintln(&b)

			if run.Status == citypes.StatusFailed && run.Output != "" {
				for _, line := range strings.Split(strings.TrimRight(run.Output, "\n"), "\n") {
					fmt.Fprintf(&b, "    | %s\n", line)
				}
			}

			for _, gate := range run.Gates {
				fmt.Fprintf(&b, "    gate %s %s %s\n", gate.Alias, gate.Status, gate.Message)
			}

			if run.Forge != nil {
				for _, tr := range run.Forge.TestReports {
					fmt.Fprintf(&b, "    forge %s %s %d tests %.1f%% coverage\n",
						tr.Stage, tr.Status, tr.TestStats.Total, tr.Coverage.Percentage)
				}
			}
		}
	}

	return b.String()
}

func blockedReason(report reconcilecontroller.Report) string {
	for _, stage := range report.Stages {
		if !stage.Advance {
			return stage.Reason
		}
	}

	return "no stage advanced"
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}

	return sha
}

func Load(path string) (config.Pipeline, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return config.Pipeline{}, fmt.Errorf("reading %s: %w", path, err)
	}

	p, err := config.Parse(raw)
	if err != nil {
		return config.Pipeline{}, fmt.Errorf("validating %s: %w", path, err)
	}

	return p, nil
}
