package reconcilecontroller

import (
	"context"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

// What one stage builds, the stage after it reads.
//
// A whole apply gets that for nothing: every stage runs in one process on
// one disk, so a file the build stage wrote is still where it wrote it when
// the stage after it looks. Stages in jobs of their own are not. Each one is
// a fresh clone carrying nothing but its checkout, and forge-self run 35
// died exactly there: the package stage's glob over the cross-built
// executables matched nothing, because the executables had never left the
// runner that built them.
//
// The door out already exists - put keeps what a run built, get brings it
// back - and it had only ever been opened for the release. This opens it for
// every stage that runs after another.

// carryForward brings back what the stages before this one built, so a
// stage job reads its predecessors' files whether or not this machine wrote
// them, and answers the records with the locations they came back to - which
// is what a substage that publishes is handed.
//
// A substage with no record has not run, which a promotion tolerant of
// failure allows, and it is skipped rather than refused: whether this stage
// may run at all was decided by the ordering check before this.
func (c *Controller) carryForward(
	ctx context.Context,
	index engineIndex,
	revision citypes.Revision,
	root string,
	before []config.Stage,
) ([]forge.Artifact, error) {
	stages := make([]StageReport, 0, len(before))

	for _, stage := range before {
		report := StageReport{Name: stage.Name}

		for _, sub := range stage.Substages {
			run, err := c.getRun(ctx, index, runKey(revision.ID, stage.Name, sub.Name))
			if err != nil {
				return nil, err
			}

			if run == nil {
				continue
			}

			report.Runs = append(report.Runs, *run)
		}

		if len(report.Runs) > 0 {
			stages = append(stages, report)
		}
	}

	if len(stages) == 0 {
		return nil, nil
	}

	return c.restoreArtifacts(ctx, index, revision.ID, root, stages)
}
