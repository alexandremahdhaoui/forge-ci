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

type Action struct {
	Text    string
	Changed bool
}

func Kept(text string) Action { return Action{Text: text} }

func Did(text string) Action { return Action{Text: text, Changed: true} }

type Options struct {
	DryRun bool
	Force  bool
}

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

type Settler interface {
	Settle(paths []string, message string) (Action, bool, error)
}

func Realizes(in citypes.ReconcileInput, r citypes.Resource) bool {
	return in.Bootstrap || !r.BootstrapOnly
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

		out.Owned = append(out.Owned, citypes.Ownership{Resource: r.ID(), Manager: in.Manager})

		if !Realizes(in, r) {
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

	if settler, ok := c.realizer.(Settler); ok && !in.DryRun && len(changed) > 0 {
		action, published, err := settler.Settle(changed, citypes.SelfReconcileMessage(in.CommitPrefix))
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

	if have, err := c.fs.ReadFile(path); err == nil && bytes.Equal(have, payload) {
		return nil
	}

	if err := c.fs.WriteFile(path, payload); err != nil {
		return fmt.Errorf("recording manager state: %w", err)
	}

	return nil
}
