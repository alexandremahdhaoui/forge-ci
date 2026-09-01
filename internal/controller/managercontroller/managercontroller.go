package managercontroller

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var ErrOwnedElsewhere = errors.New("resource is recorded as owned by a different manager")

// Action is what a realizer did to one resource: a line for a human, and
// whether the world actually moved.
//
// Changed is the whole point. A reconcile that rewrites the pipeline's own
// files and then lets the run continue measures the tree it just wrote, so
// the revision comes out dirty and the release refuses it. The caller stops
// instead, and the run that the change triggers reads the corrected state.
//
// So a realizer must be honest about the difference between converging a
// resource and finding it already correct. Reporting Changed for a resource
// that already matched stops every run forever.
type Action struct {
	Text    string
	Changed bool
}

// Kept is an action that found the resource already as declared.
func Kept(text string) Action { return Action{Text: text} }

// Did is an action that found a difference and closed it.
func Did(text string) Action { return Action{Text: text, Changed: true} }

// Options is how one reconcile is being run, handed to every realizer.
//
// DryRun forbids every write. A realizer still reads actual state and still
// answers what it would do, because the plan is only worth reading if it
// came from the same comparison the real run makes.
//
// Force rewrites what cannot be compared. Only a write-only resource needs
// it, and only a human asks for it.
type Options struct {
	DryRun bool
	Force  bool
}

// would prefixes an action that did not happen, so a plan never reads like a
// report of work done.
func (o Options) would(text string) string {
	if o.DryRun {
		return "would " + text
	}

	return text
}

type Realizer interface {
	Kind() string
	Realize(citypes.Resource, Options) (Action, error)
}

// Settler is the optional half of a manager: making this reconcile's changes
// durable, in whatever way this manager's world means durable. A manager
// whose realize step was already durable - a file on local disk, a function
// deployed through an API - implements nothing and still reports Changed.
//
// paths are the resource names this reconcile actually changed. Only those
// may be touched. A settle that swept up everything uncommitted would commit
// a human's unrelated work, which is not the pipeline's to publish.
type Settler interface {
	// Settle makes this reconcile's changes durable and answers whether any
	// of them were PUBLISHED - delivered somewhere outside this machine,
	// like a git push. The distinction is what the core's stop decision
	// hangs on: a published change re-fires the pipeline, so the run may
	// stop superseded; a change nobody published cannot re-trigger
	// anything, and a run that stopped for one would strand the pipeline.
	Settle(paths []string) (Action, bool, error)
}

type Controller struct {
	realizer Realizer
	fs       fsadapter.FS
}

func New(realizer Realizer, fs fsadapter.FS) *Controller {
	return &Controller{realizer: realizer, fs: fs}
}

func (c *Controller) Reconcile(in citypes.ReconcileInput) (citypes.ReconcileOutput, error) {
	if in.Manager == "" {
		return citypes.ReconcileOutput{}, errors.New("reconciling: manager alias is required")
	}

	owners := map[string]string{}
	for _, o := range in.Owned {
		owners[o.Resource] = o.Manager
	}

	out := citypes.ReconcileOutput{Owned: []citypes.Ownership{}, Actions: []string{}}

	// Every resource is attempted, and the failures are reported together.
	//
	// Stopping at the first one made a reconcile's result depend on
	// declaration order: one 403 on a credential left every resource after it
	// untouched, including the files that cannot fail for a network reason at
	// all. An operator then fixed one thing, re-ran, and met the next
	// failure - and the local checkout stayed stale the whole time, which
	// reads exactly like a generator that produced nothing.
	//
	// A malformed resource is still fatal on the spot: that is the caller
	// handing over something that is not a resource, not a resource that
	// could not be realized.
	var (
		failures []error
		changed  []string
	)

	opts := Options{DryRun: in.DryRun, Force: in.Force}

	for _, r := range in.Resources {
		if r.Kind == "" || r.Name == "" {
			return citypes.ReconcileOutput{}, fmt.Errorf("reconciling: resource needs a kind and a name, got %+v", r)
		}

		if prev, ok := owners[r.ID()]; ok && prev != in.Manager {
			return citypes.ReconcileOutput{}, fmt.Errorf(
				"reconciling %s: %w: recorded owner is %q, declared owner is %q. import it or destroy it first",
				r.ID(), ErrOwnedElsewhere, prev, in.Manager)
		}

		// Ownership is recorded either way. It is what stops another manager
		// claiming the resource, and that is true whether or not this
		// particular reconcile is allowed to write it.
		out.Owned = append(out.Owned, citypes.Ownership{Resource: r.ID(), Manager: in.Manager})

		// A bootstrapOnly resource cannot be converged: it is written blind,
		// because nothing can be read back to compare against, so realizing
		// one on every run is only writing it again - and a run that did so
		// would have to hold the rights to rewrite it. Credentials are the
		// case this exists for. Only a bootstrap writes them.
		if r.BootstrapOnly && !in.Bootstrap {
			out.Actions = append(out.Actions,
				"left "+r.ID()+" alone: only a bootstrap writes it")

			continue
		}

		action, err := c.realizer.Realize(r, opts)
		if err != nil {
			failures = append(failures, fmt.Errorf("realizing %s: %w", r.ID(), err))

			continue
		}

		out.Actions = append(out.Actions, action.Text)

		if action.Changed {
			out.Changed = true

			changed = append(changed, r.Name)
		}
	}

	// Everything is reconciled before anything settles. Settling at the first
	// resource that moved would need one run per resource to converge a
	// pipeline, so a thousand declared resources would take a thousand runs
	// to update - and every one of those runs would report the same thing.
	//
	// A bootstrap settles like any other reconcile. One rule: a change is
	// made durable by whoever made it. A bootstrap that wrote eight workflow
	// files and left them uncommitted resolves a revision over its own
	// output, and leaves an operator to commit across eight repos by hand.
	//
	// A dry run settles nothing, because it changed nothing.
	if settler, ok := c.realizer.(Settler); ok && !in.DryRun && len(changed) > 0 {
		action, published, err := settler.Settle(changed)
		if err != nil {
			failures = append(failures, fmt.Errorf("settling what changed: %w", err))
		} else {
			if action.Text != "" {
				out.Actions = append(out.Actions, action.Text)
			}

			out.Published = published
		}
	}

	sort.Slice(out.Owned, func(i, j int) bool { return out.Owned[i].Resource < out.Owned[j].Resource })

	// Ownership is recorded for what this reconcile declared even when some
	// of it could not be realized, which is the same rule the loop above
	// applies: it is what stops another manager claiming the resource, and
	// that does not depend on whether today's attempt reached the network.
	if err := c.record(in, out); err != nil {
		failures = append(failures, err)
	}

	if len(failures) > 0 {
		return citypes.ReconcileOutput{}, errors.Join(failures...)
	}

	return out, nil
}

func (c *Controller) record(in citypes.ReconcileInput, out citypes.ReconcileOutput) error {
	path, _ := in.Spec["statePath"].(string)
	if path == "" || c.fs == nil || in.DryRun {
		return nil
	}

	payload, err := json.MarshalIndent(struct {
		Manager string              `json:"manager"`
		Kind    string              `json:"kind"`
		Owned   []citypes.Ownership `json:"owned"`
		Actions []string            `json:"actions"`
	}{in.Manager, c.realizer.Kind(), out.Owned, out.Actions}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding manager state: %w", err)
	}

	// Written only when it would differ. This file sits inside a member
	// checkout, so rewriting identical bytes every reconcile leaves the tree
	// dirty on a run where nothing else moved - and a dirty tree is a dirty
	// revision, which the release refuses.
	if have, err := c.fs.ReadFile(path); err == nil && bytes.Equal(have, payload) {
		return nil
	}

	if err := c.fs.WriteFile(path, payload); err != nil {
		return fmt.Errorf("recording manager state: %w", err)
	}

	return nil
}
