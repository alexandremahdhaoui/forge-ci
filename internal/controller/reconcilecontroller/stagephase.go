package reconcilecontroller

import (
	"context"
	"errors"
	"fmt"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

// One stage as a job of its own.
//
// A compute engine may render the stages phase as one job per stage, or one
// per substage. Each of those jobs is an apply narrowed by --stage and
// --substage, and each carries on from the run records the jobs before it
// wrote: a substage job reads nothing but the evaluation, and any stage job
// asks the promotion of the stage in front over the records that stage's
// substages left. A job asked to run before those records exist refuses, by
// name, rather than running out of order.

var (
	// ErrUnknownStage is a stage or substage name the pipeline does not
	// declare.
	ErrUnknownStage = errors.New("the pipeline declares no such stage")

	// ErrStageOutOfOrder is a stage job asked to run before the stage in
	// front of it has a green record.
	ErrStageOutOfOrder = errors.New("the stage before this one has no green record; run it first")

	// ErrStageIncomplete is a promotion asked over a stage whose substages
	// have not all recorded a run.
	ErrStageIncomplete = errors.New("a substage of this stage has no run record; run it first")
)

// applyNamedStage runs the one stage or substage the options name, under
// the decision the evaluate phase recorded.
func (c *Controller) applyNamedStage(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	root string,
	revision citypes.Revision,
	decision releaseDecision,
	opts Options,
	report Report,
) (Report, error) {
	at := -1

	for i, stage := range p.Stages {
		if stage.Name == opts.Stage {
			at = i
		}
	}

	if at < 0 {
		return Report{}, fmt.Errorf("%w: %q", ErrUnknownStage, opts.Stage)
	}

	stage := p.Stages[at]

	// The stage in front of this one must have advanced. The answer is the
	// promotion's, and it is asked here rather than looked up, because the
	// run records it reads are already the shared memory between processes
	// and asking is a state read and one engine call.
	//
	// Looking it up needed a whole job of its own to write it - a checkout,
	// a container, a toolchain, minutes of it - to decide something the job
	// that needs the answer can decide for itself in seconds.
	if at > 0 {
		before := p.Stages[at-1]

		rec, err := c.promoteRecorded(ctx, index, before, revision)

		// A substage of the stage in front with no record has not run, which
		// is this job arriving early rather than the stage having failed.
		// Both are true and the caller wants the first one.
		if errors.Is(err, ErrStageIncomplete) {
			return Report{}, fmt.Errorf("%w: stage %q before %q: %w",
				ErrStageOutOfOrder, before.Name, stage.Name, err)
		}

		if err != nil {
			return Report{}, fmt.Errorf("asking whether stage %q advanced: %w", before.Name, err)
		}

		if !rec.Advance {
			return Report{}, fmt.Errorf("%w: stage %q before %q: %s",
				ErrStageOutOfOrder, before.Name, stage.Name, rec.Reason)
		}
	}

	// A stage job runs targets or publishes, and both read what the stages
	// before this one built.
	var (
		carried []forge.Artifact
		err     error
	)

	if at > 0 {
		carried, err = c.carryForward(ctx, index, revision, root, p.Stages[:at])
		if err != nil {
			return Report{}, fmt.Errorf("carrying what the stages before %q built: %w", stage.Name, err)
		}
	}

	var stageReport StageReport

	switch {
	case opts.Substage != "":
		stageReport, err = c.applyOneSubstage(ctx, p, index, stage, opts.Substage, revision, decision, root, carried)
	default:
		stageReport, err = c.applyStage(ctx, p, index, stage, revision, decision, root, carried)
	}

	if err != nil {
		return Report{}, err
	}

	report.Stages = []StageReport{stageReport}
	report.Released = append(report.Released, stageReport.Released...)

	// Nothing is recorded for the stage as a whole. The run records its
	// substages left are the memory, and the stage after this one asks the
	// promotion over them itself.
	return report, nil
}

// applyOneSubstage runs one substage and its gates, and reports it as the
// stage's only run: passed with its gates satisfied means this job is
// green, and nothing more is decided here.
func (c *Controller) applyOneSubstage(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	stage config.Stage,
	name string,
	revision citypes.Revision,
	decision releaseDecision,
	root string,
	carried []forge.Artifact,
) (StageReport, error) {
	for _, sub := range stage.Substages {
		if sub.Name != name {
			continue
		}

		run, reused, out, err := c.applySubstage(ctx, p, index, stage, sub, revision, decision, root, carried)
		if err != nil {
			return StageReport{}, err
		}

		report := StageReport{Name: stage.Name, Runs: []citypes.Run{run}}

		if reused {
			report.Reused = 1
		}

		if out.Published || out.URL != "" {
			report.Released = []citypes.ArtifactOutput{out}
		}

		if run.Status == citypes.StatusPassed && allGatesPassed(run) {
			report.Advance = true
			report.Reason = fmt.Sprintf("substage %q of stage %q passed", name, stage.Name)
		} else {
			report.Reason = fmt.Sprintf("substage %q of stage %q did not pass", name, stage.Name)
		}

		return report, nil
	}

	return StageReport{}, fmt.Errorf("%w: substage %q of stage %q", ErrUnknownStage, name, stage.Name)
}

// promoteRecorded asks the stage's promotion over the run every substage
// recorded. A substage with no record has not run yet, and a promotion over
// a stage half run would be a promotion over nothing.
func (c *Controller) promoteRecorded(
	ctx context.Context,
	index engineIndex,
	stage config.Stage,
	revision citypes.Revision,
) (StageReport, error) {
	report := StageReport{Name: stage.Name, Runs: make([]citypes.Run, 0, len(stage.Substages))}

	for _, sub := range stage.Substages {
		run, err := c.getRun(ctx, index, runKey(revision.ID, stage.Name, sub.Name))
		if err != nil {
			return StageReport{}, err
		}

		if run == nil {
			return StageReport{}, fmt.Errorf("%w: substage %q of stage %q", ErrStageIncomplete, sub.Name, stage.Name)
		}

		report.Runs = append(report.Runs, *run)
		report.Reused++
	}

	advance, reason, err := c.promote(ctx, index, stage, report.Runs)
	if err != nil {
		return StageReport{}, err
	}

	report.Advance = advance
	report.Reason = reason

	return report, nil
}
