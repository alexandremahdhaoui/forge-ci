package reconcilecontroller

import (
	"fmt"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
)

type engineIndex struct {
	engines    map[string]config.Engine
	managers   map[string]config.Manager
	targetsBy  map[string]config.Target
	stateAlias string
	stateURI   string
	stateSpec  map[string]any
}

// newIndex reads the pipeline into lookups, and hands the state engine the
// pipeline root the same way the manager gets it.
//
// Without that the state engine resolved spec.path against the process's
// working directory while every managed resource resolved against --root, so
// running from inside a member wrote the state repo one level too deep. The
// generated CI does exactly that - `cd <member>` then `--root ..` - and the
// member's own .gitignore then swallowed the records, silently.
func newIndex(p config.Pipeline, root string) engineIndex {
	idx := engineIndex{
		engines:   map[string]config.Engine{},
		managers:  map[string]config.Manager{},
		targetsBy: map[string]config.Target{},
	}

	for _, e := range p.Engines {
		e.Spec = orEmpty(e.Spec)
		idx.engines[e.Alias] = e
	}

	for _, m := range p.Managers {
		m.Spec = orEmpty(m.Spec)
		idx.managers[m.Alias] = m
	}

	for _, t := range p.Targets {
		idx.targetsBy[t.Alias] = t
	}

	if state, ok := idx.engines[p.State]; ok {
		idx.stateAlias = state.Alias
		idx.stateURI = state.Engine
		idx.stateSpec = state.Spec
	}

	idx.stateSpec = orEmpty(idx.stateSpec)

	// An explicit root in the spec wins, so an instance can still say where
	// its store is. Otherwise the pipeline's own root, which is "." for the
	// ordinary case and joins to a no-op there.
	if _, ok := idx.stateSpec["root"]; !ok && root != "" {
		idx.stateSpec["root"] = root
	}

	return idx
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}

	return m
}

func (i engineIndex) require(alias string, want config.Port) (config.Engine, error) {
	engine, ok := i.engines[alias]
	if !ok {
		return config.Engine{}, fmt.Errorf("%q: %w", alias, ErrEngine)
	}

	if engine.Type != want {
		return config.Engine{}, fmt.Errorf("%q is a %s engine, want %s", alias, engine.Type, want)
	}

	return engine, nil
}

func (i engineIndex) manager(alias string) (config.Manager, error) {
	manager, ok := i.managers[alias]
	if !ok {
		return config.Manager{}, fmt.Errorf("manager %q: %w", alias, ErrEngine)
	}

	return manager, nil
}

func (i engineIndex) targets(_ config.Pipeline, aliases []string) []citypes.Target {
	out := make([]citypes.Target, 0, len(aliases))

	for _, alias := range aliases {
		t, ok := i.targetsBy[alias]
		if !ok {
			continue
		}

		out = append(out, citypes.Target{
			Alias:  t.Alias,
			Binary: t.Binary,
			Args:   t.Args,
			In:     t.In,
		})
	}

	return out
}
