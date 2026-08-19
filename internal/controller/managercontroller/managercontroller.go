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

	for _, r := range in.Resources {
		if r.Kind == "" || r.Name == "" {
			return citypes.ReconcileOutput{}, fmt.Errorf("reconciling: resource needs a kind and a name, got %+v", r)
		}

		if prev, ok := owners[r.ID()]; ok && prev != in.Manager {
			return citypes.ReconcileOutput{}, fmt.Errorf(
				"reconciling %s: %w: recorded owner is %q, declared owner is %q. import it or destroy it first",
				r.ID(), ErrOwnedElsewhere, prev, in.Manager)
		}

		action, err := c.realizer.Realize(r)
		if err != nil {
			return citypes.ReconcileOutput{}, fmt.Errorf("realizing %s: %w", r.ID(), err)
		}

		out.Actions = append(out.Actions, action)
		out.Owned = append(out.Owned, citypes.Ownership{Resource: r.ID(), Manager: in.Manager})
	}

	sort.Slice(out.Owned, func(i, j int) bool { return out.Owned[i].Resource < out.Owned[j].Resource })

	if err := c.record(in, out); err != nil {
		return citypes.ReconcileOutput{}, err
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
