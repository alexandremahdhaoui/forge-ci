package managercontroller

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var ErrOwnedElsewhere = errors.New("resource is recorded as owned by a different manager")

type Realizer interface {
	Kind() string
	Realize(citypes.Resource) (string, error)
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
	var failures []error

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

		action, err := c.realizer.Realize(r)
		if err != nil {
			failures = append(failures, fmt.Errorf("realizing %s: %w", r.ID(), err))

			continue
		}

		out.Actions = append(out.Actions, action)
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
	if path == "" || c.fs == nil {
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

	if err := c.fs.WriteFile(path, payload); err != nil {
		return fmt.Errorf("recording manager state: %w", err)
	}

	return nil
}
