package clidriver

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/reconcilecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/releasecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
)

const DefaultPath = "forge-ci.yaml"

const EnvInApply = "FORGE_CI_IN_APPLY"

var (
	ErrUsage = errors.New(
		"usage: forge-ci <bootstrap|apply|status|poll|graph|validate|release> [flags]")
	ErrBlocked = errors.New("the pipeline did not advance")
	ErrRecurse = errors.New("apply cannot run inside apply")
	ErrNoGit   = errors.New("this build was wired without a GitHub client")
)

// retiredPhases maps a phase name a workflow rendered by an earlier
// forge-ci may still carry to the name it has now.
var retiredPhases = map[string]string{
	"reconcile": reconcilecontroller.PhaseSelfReconcile,
	"intent":    reconcilecontroller.PhaseEvaluate,
}

type Reconciler interface {
	Bootstrap(context.Context, config.Pipeline, string, reconcilecontroller.Options) (reconcilecontroller.Report, error)
	Apply(context.Context, config.Pipeline, string, reconcilecontroller.Options) (reconcilecontroller.Report, error)
	Status(context.Context, config.Pipeline, string) (reconcilecontroller.Report, error)
	Poll(context.Context, config.Pipeline, string) (citypes.TriggerOutput, error)
}

// Publisher tags a commit and releases it. The generated release workflow
// calls this rather than a CLI the image may not carry, so the idempotency
// rule lives in a controller with a test instead of in a shell `if`.
type Publisher interface {
	Publish(ctx context.Context, dir, repo, tag, sha string) (releasecontroller.Report, error)
}

// GitHubFor builds the controller that reaches GitHub. It is a function
// because the credential is named by a flag, so nothing can be constructed
// until the flags are parsed - which is also what keeps the token out of
// every code path that does not need one.
type GitHubFor func(tokenEnv, apiBaseURL string) Publisher

type Driver struct {
	out        io.Writer
	reconciler Reconciler
	github     GitHubFor
	applying   bool
}

func New(out io.Writer, reconciler Reconciler) *Driver {
	return &Driver{out: out, reconciler: reconciler}
}

// WithGitHub wires the verb the generated release workflow calls. A build
// without it still runs every pipeline verb and refuses that one by name.
func (d *Driver) WithGitHub(github GitHubFor) *Driver {
	d.github = github

	return d
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

	// release acts on one repo somebody named and never reads a pipeline, so
	// it is answered before the config is loaded. The generated release
	// workflow calls it from a job that has a checkout and no forge-ci.yaml.
	if verb == "release" {
		return d.release(ctx, args[1:])
	}

	p, root, opts, err := d.load(verb, args[1:])
	if err != nil {
		return err
	}

	switch verb {
	case "validate":
		return d.write(describe(p))
	case "bootstrap":
		return d.reportOf(func() (reconcilecontroller.Report, error) {
			return d.reconciler.Bootstrap(ctx, p, root, opts)
		}, false)
	case "apply":
		if d.applying {
			return fmt.Errorf(
				"%w: a stage ran forge-ci apply. every apply already reconciles the pipeline's own "+
					"resources before its first stage, so a stage that applies is the loop calling itself", ErrRecurse)
		}

		return d.reportOf(func() (reconcilecontroller.Report, error) {
			return d.reconciler.Apply(ctx, p, root, opts)
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
		out, err := d.reconciler.Poll(ctx, p, root)
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

func (d *Driver) load(verb string, args []string) (config.Pipeline, string, reconcilecontroller.Options, error) {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(d.out)

	path := fs.String("config", DefaultPath, "path to the forge-ci file")
	root := fs.String("root", "", "directory holding the repos, defaults to the pipeline file's parent")
	dryRun := fs.Bool("dry-run", false,
		"say what would change and write nothing, anywhere")
	force := fs.Bool("force", false,
		"rewrite a resource that exists and cannot be compared, which today means one thing: a secret")
	phase := fs.String("phase", "",
		"run one part of the apply: self-reconcile, evaluate or stages. Empty runs the whole loop")
	stage := fs.String("stage", "",
		"with --phase stages: run this one stage, after the stage before it has a green record")
	substage := fs.String("substage", "",
		"with --stage: run this one substage and its gates, and decide nothing for the stage")
	promote := fs.Bool("promote", false,
		"with --stage: ask the stage's promotion over every substage's record")
	revision := fs.String("revision", "",
		"the revision this run is bound to, as the evaluate phase reported it")

	if err := fs.Parse(args); err != nil {
		return config.Pipeline{}, "", reconcilecontroller.Options{}, fmt.Errorf("parsing flags: %w", err)
	}

	// A rendered workflow carries the phase names of the forge-ci that
	// rendered it, and the job that re-renders it is the first phase. So a
	// spelling this binary retired must still reach that phase, or a
	// pipeline that adopts a new image can never converge its own
	// workflow: golden run 24 died exactly there.
	if renamed, ok := retiredPhases[*phase]; ok {
		*phase = renamed
	}

	if *phase != "" && !slices.Contains(reconcilecontroller.Phases, *phase) {
		return config.Pipeline{}, "", reconcilecontroller.Options{}, fmt.Errorf(
			"%w: --phase %q is not one of %s", ErrUsage, *phase, strings.Join(reconcilecontroller.Phases, ", "))
	}

	// A narrowed stage run is a stages phase and nothing else: the other
	// phases have no stage to name, and a substage or a promotion without
	// a stage names nothing.
	if *stage != "" && *phase != reconcilecontroller.PhaseStages {
		return config.Pipeline{}, "", reconcilecontroller.Options{}, fmt.Errorf(
			"%w: --stage needs --phase %s", ErrUsage, reconcilecontroller.PhaseStages)
	}

	if (*substage != "" || *promote) && *stage == "" {
		return config.Pipeline{}, "", reconcilecontroller.Options{}, fmt.Errorf(
			"%w: --substage and --promote need --stage", ErrUsage)
	}

	if *substage != "" && *promote {
		return config.Pipeline{}, "", reconcilecontroller.Options{}, fmt.Errorf(
			"%w: --substage runs one substage and --promote decides the stage; pick one", ErrUsage)
	}

	// A run proves one set of commits, and only the phases after the
	// evaluate one can be told which: the evaluate phase is what decides it,
	// and a whole apply holds it in hand from its first stage to its last.
	if *revision != "" && *phase != reconcilecontroller.PhaseStages {
		return config.Pipeline{}, "", reconcilecontroller.Options{}, fmt.Errorf(
			"%w: --revision needs --phase %s; every other phase resolves its own",
			ErrUsage, reconcilecontroller.PhaseStages)
	}

	opts := reconcilecontroller.Options{
		DryRun: *dryRun, Force: *force, Phase: *phase,
		Stage: *stage, Substage: *substage, Promote: *promote,
		Revision: *revision,
	}

	p, err := Load(*path)
	if err != nil {
		return config.Pipeline{}, "", opts, err
	}

	// The root is absolute from here on, whichever way it arrived. It
	// reaches every engine as spec["root"], and an engine joins paths from
	// it that only mean one place while it is absolute: the member
	// checkouts it runs git in, and the asset files it reads to upload.
	//
	// With --root . every join looks clean and means whatever directory the
	// process happens to be in. An engine that changed directory, or one
	// started somewhere else, then resolves a relative asset path against
	// the wrong place and the release fails on a file that is sitting on
	// disk. It fails at the last step, after the build and publish stages
	// have passed and the tags are already cut.
	//
	// Deriving the root from the config file already absolutised it. An
	// explicit one took the other branch and stayed relative, which is the
	// form the operator runbook tells people to type.
	if *root == "" {
		abs, err := filepath.Abs(*path)
		if err != nil {
			return config.Pipeline{}, "", opts, fmt.Errorf("resolving %s: %w", *path, err)
		}

		*root = filepath.Dir(abs)
	}

	abs, err := filepath.Abs(*root)
	if err != nil {
		return config.Pipeline{}, "", opts, fmt.Errorf("resolving %s: %w", *root, err)
	}

	return p, abs, opts, nil
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

	// A plan says so on its first line and never prints a revision, because
	// it resolved none. An operator reading a wall of actions has to be able
	// to tell at a glance whether they already happened.
	if report.Planned {
		b.WriteString("this is a plan, nothing was written\n")

		if len(report.Actions) == 0 {
			b.WriteString("  everything already matches what is declared\n")
		}

		writeActions(&b, report.Actions)

		return b.String()
	}

	// The superseded report comes first and reads as its own thing. It is
	// not a failure and the exit status says so, so the only way an operator
	// learns why no stage ran is by reading this.
	if report.Superseded {
		b.WriteString("reconcile changed this pipeline's own resources and settled them\n")
		b.WriteString("this run is superseded by the run those changes trigger\n")
		writeActions(&b, report.Actions)

		return b.String()
	}

	fmt.Fprintf(&b, "revision %s\n", report.Revision.ID)

	// A skipped run says why on its own line and ends there: no stage ran
	// and nothing was tagged. The last line is the machine-readable word a
	// rendered workflow reads to skip the jobs that follow.
	if report.Skipped {
		fmt.Fprintf(&b, "skipped: %s\n", report.Reason)
		writeActions(&b, report.Actions)
		fmt.Fprintf(&b, "%s: %s\n", reconcilecontroller.PhaseEvaluate, report.Evaluation)

		return b.String()
	}

	// An evaluate phase that found work says so as its last line, the same
	// word a skip prints, so a rendered workflow reads one line either way.
	if report.Evaluation != "" {
		writeActions(&b, report.Actions)
		fmt.Fprintf(&b, "%s: %s\n", reconcilecontroller.PhaseEvaluate, report.Evaluation)

		return b.String()
	}

	// Nothing new reads as its own thing, up front: every run below was
	// answered from the recorded state, so this apply executed nothing. A
	// serialized duplicate - the second dispatch of one push wave - lands
	// here, and without this line it reads like a build that did work.
	if report.NothingNew {
		fmt.Fprintf(&b, "nothing new: revision %s already ran green; every run below was reused from the recorded state\n",
			report.Revision.ID)
	}

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

	for _, released := range report.Released {
		fmt.Fprintf(&b, "released %s", released.URL)

		if len(released.Tagged) > 0 {
			fmt.Fprintf(&b, " (tagged %s)", strings.Join(released.Tagged, ", "))
		}

		fmt.Fprintln(&b)
	}

	return b.String()
}

// writeActions indents every action, splitting the ones that carry more than
// one line. A settle reports one line per repo it pushed.
func writeActions(b *strings.Builder, actions []string) {
	for _, action := range actions {
		for _, line := range strings.Split(strings.TrimRight(action, "\n"), "\n") {
			fmt.Fprintf(b, "  %s\n", line)
		}
	}
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
