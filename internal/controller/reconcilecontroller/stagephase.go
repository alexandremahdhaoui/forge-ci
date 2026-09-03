package reconcilecontroller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

// One stage as a job of its own.
//
// A compute engine may render the stages phase as one job per stage, or one
// per substage with one promotion job per stage. Each of those jobs is an
// apply narrowed by --stage, --substage and --promote, and each carries on
// from the records the jobs before it wrote: a substage job reads nothing
// but the evaluation, a promotion job reads every substage's run record, and
// any stage job reads the record the stage before it left. A job asked to
// run before the record it needs exists refuses, by name, rather than
// running out of order.

// stageKeyPrefix is where a stage job keeps what its promotion decided, so
// the stage after it in another process can ask.
const stageKeyPrefix = "stage-"

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

// stageRecord is what a stage job decided: whether the stage advanced, and
// why, in the words the promotion used.
type stageRecord struct {
	Revision   string    `json:"revision"`
	Stage      string    `json:"stage"`
	Advance    bool      `json:"advance"`
	Reason     string    `json:"reason"`
	PromotedAt time.Time `json:"promotedAt"`
}

// applyNamedStage runs the one stage, substage or promotion the options
// name, under the decision the evaluate phase recorded.
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

	// The stage in front of this one must have advanced, in this process's
	// past or in another's: the record is the only thing both share.
	if at > 0 {
		before := p.Stages[at-1]

		rec, err := c.readStage(ctx, index, revision.ID, before.Name)
		if err != nil {
			return Report{}, err
		}

		if rec == nil || !rec.Advance {
			return Report{}, fmt.Errorf("%w: stage %q before %q", ErrStageOutOfOrder, before.Name, stage.Name)
		}
	}

	// A promotion reads records and runs nothing, so it needs no files. Every
	// other shape runs targets or publishes, and both read what the stages
	// before this one built.
	var (
		carried []forge.Artifact
		err     error
	)

	if !opts.Promote && at > 0 {
		carried, err = c.carryForward(ctx, index, revision, root, p.Stages[:at])
		if err != nil {
			return Report{}, fmt.Errorf("carrying what the stages before %q built: %w", stage.Name, err)
		}
	}

	var stageReport StageReport

	switch {
	case opts.Promote:
		stageReport, err = c.promoteRecorded(ctx, index, stage, revision)
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

	// A substage job decides nothing for the stage; the promotion job, or
	// the whole-stage job, does, and records it for the stage after it.
	if opts.Substage != "" {
		return report, nil
	}

	if err := c.writeStage(ctx, index, stageRecord{
		Revision: revision.ID, Stage: stage.Name,
		Advance: stageReport.Advance, Reason: stageReport.Reason, PromotedAt: c.now(),
	}); err != nil {
		return Report{}, err
	}

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

func (c *Controller) readStage(ctx context.Context, index engineIndex, revision, stage string) (*stageRecord, error) {
	var out citypes.StateGetOutput

	if err := c.callState(ctx, index, ToolGet, citypes.StateGetInput{
		Kind: KindOwned, Key: stageKeyPrefix + revision + "-" + stage, Spec: index.stateSpec,
	}, &out); err != nil {
		return nil, err
	}

	if !out.Found {
		return nil, nil
	}

	var rec stageRecord
	if err := json.Unmarshal([]byte(out.Payload), &rec); err != nil {
		return nil, fmt.Errorf("reading the record of stage %q: %w", stage, err)
	}

	return &rec, nil
}

func (c *Controller) writeStage(ctx context.Context, index engineIndex, rec stageRecord) error {
	return c.putJSON(ctx, index, KindOwned, stageKeyPrefix+rec.Revision+"-"+rec.Stage, rec)
}
