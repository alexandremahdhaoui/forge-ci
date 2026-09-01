package reconcilecontroller

import (
	"context"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
)

func (c *Controller) Bootstrap(
	ctx context.Context, p config.Pipeline, root string, opts Options,
) (Report, error) {
	index := newIndex(p, root)

	// true: the bootstrap is the one ceremony that writes credentials.
	//
	// The changed flag is read and dropped on purpose. A bootstrap runs no
	// stages, so there is no run to supersede, and the manager has already
	// made its changes durable - which is what lets the revision below hash
	// a committed tree rather than the files this call just wrote.
	actions, _, _, err := c.reconcileResources(ctx, p, index, root, true, opts)
	if err != nil {
		return Report{}, err
	}

	if opts.DryRun {
		return Report{Actions: actions, Planned: true}, nil
	}

	revision, err := c.resolveRevision(ctx, p, root)
	if err != nil {
		return Report{}, err
	}

	if err := c.putJSON(ctx, index, KindRevision, revision.ID, toWire(revision)); err != nil {
		return Report{}, err
	}

	return Report{Revision: revision, Actions: actions}, nil
}

func (c *Controller) Status(ctx context.Context, p config.Pipeline, root string) (Report, error) {
	index := newIndex(p, root)

	revision, err := c.resolveRevision(ctx, p, root)
	if err != nil {
		return Report{}, err
	}

	report := Report{Revision: revision}

	for _, stage := range p.Stages {
		stageReport := StageReport{Name: stage.Name, Advance: true}

		for _, sub := range stage.Substages {
			run, err := c.getRun(ctx, index, runKey(revision.ID, stage.Name, sub.Name))
			if err != nil {
				return Report{}, err
			}

			if run == nil {
				run = &citypes.Run{
					Revision: revision.ID,
					Stage:    stage.Name,
					Substage: sub.Name,
					Engine:   sub.Engine,
					Status:   citypes.StatusPending,
				}
			}

			if run.Status != citypes.StatusPassed || !allGatesPassed(*run) {
				stageReport.Advance = false
			}

			stageReport.Runs = append(stageReport.Runs, *run)
		}

		if stageReport.Advance {
			stageReport.Reason = "every substage passed"
		} else {
			stageReport.Reason = "not finished"
		}

		report.Stages = append(report.Stages, stageReport)
	}

	return report, nil
}

func (c *Controller) Poll(ctx context.Context, p config.Pipeline, root string) (citypes.TriggerOutput, error) {
	index := newIndex(p, root)

	combined := citypes.TriggerOutput{}

	for _, alias := range p.Triggers {
		engine, err := index.require(alias, config.PortTrigger)
		if err != nil {
			return citypes.TriggerOutput{}, err
		}

		previous, err := c.readFingerprint(ctx, index, alias)
		if err != nil {
			return citypes.TriggerOutput{}, err
		}

		spec := map[string]any{}
		for k, v := range engine.Spec {
			spec[k] = v
		}

		spec["previous"] = previous

		var out citypes.TriggerOutput

		if err := c.caller.Call(ctx, engine.Engine, ToolPoll, map[string]any{"spec": spec}, &out); err != nil {
			return citypes.TriggerOutput{}, err
		}

		if err := c.writeFingerprint(ctx, index, alias, out.Fingerprint); err != nil {
			return citypes.TriggerOutput{}, err
		}

		if out.Changed {
			combined.Changed = true
			combined.Reason = alias + ": " + out.Reason
			combined.Fingerprint = out.Fingerprint
		}
	}

	return combined, nil
}

func (c *Controller) readFingerprint(ctx context.Context, index engineIndex, alias string) (string, error) {
	var out citypes.StateGetOutput

	if err := c.callState(ctx, index, ToolGet, citypes.StateGetInput{
		Kind: KindOwned, Key: "trigger-" + alias, Spec: index.stateSpec,
	}, &out); err != nil {
		return "", err
	}

	if !out.Found {
		return "", nil
	}

	return out.Payload, nil
}

func (c *Controller) writeFingerprint(ctx context.Context, index engineIndex, alias, fingerprint string) error {
	return c.callState(ctx, index, ToolPut, citypes.StatePutInput{
		Kind: KindOwned, Key: "trigger-" + alias, Payload: fingerprint, Spec: index.stateSpec,
	}, nil)
}
