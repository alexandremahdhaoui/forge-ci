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

func newIndex(p config.Pipeline) engineIndex {
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
			Alias:   t.Alias,
			Forge:   t.Forge,
			ForgeCI: t.ForgeCI,
			In:      t.In,
		})
	}

	return out
}
