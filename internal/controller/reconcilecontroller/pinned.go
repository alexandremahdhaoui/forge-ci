package reconcilecontroller

import (
	"context"
	"errors"
	"fmt"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
)

// One run proves one set of commits.
//
// A whole apply gets that for nothing: it resolves the revision once, before
// its first stage, and holds it. Phases in processes of their own do not. Each
// one clones the repos again and would answer whatever they hold NOW, which is
// a different question from the one the run is proving - and the difference is
// not hypothetical, because a stage that publishes into one of the pipeline's
// own repos moves that repo halfway through the run it belongs to.
//
// So the revision is decided once, recorded beside the release decision, and
// handed to every phase after it.

// ErrRevisionMismatch is a recorded decision that answers to a different
// revision than the one asked for. The store handed back the wrong record,
// which is machinery broken rather than a build gone red.
var ErrRevisionMismatch = errors.New("the recorded decision is for another revision")

// pinnedRevision answers the revision a phase is bound to, and the decision
// made for it, from the record the evaluate phase wrote.
func (c *Controller) pinnedRevision(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	root string,
	id string,
) (citypes.Revision, releaseDecision, error) {
	decision, err := c.readEvaluation(ctx, index, id)
	if err != nil {
		return citypes.Revision{}, releaseDecision{}, err
	}

	// A decision written by a forge-ci from before the revision was carried.
	// Resolving locally is what that version did, so an apply already in
	// flight when the toolchain moved finishes the way it started.
	if decision.Revision.ID == "" {
		revision, err := c.resolveRevision(ctx, p, root)
		if err != nil {
			return citypes.Revision{}, releaseDecision{}, err
		}

		return revision, decision, nil
	}

	if decision.Revision.ID != id {
		return citypes.Revision{}, releaseDecision{}, fmt.Errorf(
			"%w: asked for %s, the record answers for %s", ErrRevisionMismatch, id, decision.Revision.ID)
	}

	return decision.Revision, decision, nil
}
