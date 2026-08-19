package config

import (
	"fmt"
	"regexp"
	"strings"

	"sigs.k8s.io/yaml"
)

type Port string

const (
	PortCompute   Port = "compute"
	PortState     Port = "state"
	PortTrigger   Port = "trigger"
	PortGate      Port = "gate"
	PortPromotion Port = "promotion"
	PortArtifact  Port = "artifact"
)

var ports = map[Port]bool{
	PortCompute:   true,
	PortState:     true,
	PortTrigger:   true,
	PortGate:      true,
	PortPromotion: true,
	PortArtifact:  true,
}

var (
	aliasPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	uriPattern   = regexp.MustCompile(`^(go|alias)://.+`)
)

type Pipeline struct {
	Name              string    `json:"name"`
	Version           string    `json:"version,omitempty"`
	ArtifactStorePath string    `json:"artifactStorePath,omitempty"`
	Repos             []Repo    `json:"repos,omitempty"`
	Managers          []Manager `json:"managers,omitempty"`
	Engines           []Engine  `json:"engines"`
	State             string    `json:"state"`
	Triggers          []string  `json:"triggers,omitempty"`
	Targets           []Target  `json:"targets,omitempty"`
	Stages            []Stage   `json:"stages"`
}

type Repo struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Ref  string `json:"ref,omitempty"`
}

type Manager struct {
	Alias  string         `json:"alias"`
	Engine string         `json:"engine"`
	Spec   map[string]any `json:"spec,omitempty"`
}

type Engine struct {
	Alias   string         `json:"alias"`
	Type    Port           `json:"type"`
	Engine  string         `json:"engine"`
	Manager string         `json:"manager"`
	Spec    map[string]any `json:"spec,omitempty"`
}

type Target struct {
	Alias   string   `json:"alias"`
	Forge   string   `json:"forge,omitempty"`
	ForgeCI string   `json:"forgeCI,omitempty"`
	In      []string `json:"in,omitempty"`
}

type Stage struct {
	Name      string     `json:"name"`
	Mint      bool       `json:"mint,omitempty"`
	Release   string     `json:"release,omitempty"`
	Promotion string     `json:"promotion,omitempty"`
	Substages []Substage `json:"substages"`
}

type Substage struct {
	Name    string            `json:"name"`
	Engine  string            `json:"engine"`
	Manager string            `json:"manager"`
	Targets []string          `json:"targets"`
	Gates   []string          `json:"gates,omitempty"`
	Params  map[string]string `json:"params,omitempty"`
}

func Parse(data []byte) (Pipeline, error) {
	var p Pipeline

	if err := yaml.UnmarshalStrict(data, &p); err != nil {
		return Pipeline{}, fmt.Errorf("reading pipeline: %w", err)
	}

	if err := p.Validate(); err != nil {
		return Pipeline{}, err
	}

	return p, nil
}

func (p Pipeline) Validate() error {
	var errs []string

	add := func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	}

	if strings.TrimSpace(p.Name) == "" {
		add("name is required")
	}

	repos := map[string]bool{}
	for i, r := range p.Repos {
		if r.Name == "" {
			add("repos[%d]: name is required", i)
		}

		if r.URL == "" {
			add("repos[%d] (%s): url is required", i, r.Name)
		}

		if repos[r.Name] {
			add("repos[%d] (%s): duplicate repo name", i, r.Name)
		}

		repos[r.Name] = true
	}

	managers := map[string]bool{}
	for i, m := range p.Managers {
		where := fmt.Sprintf("managers[%d] (%s)", i, m.Alias)

		if !aliasPattern.MatchString(m.Alias) {
			add("%s: alias must be lowercase kebab-case", where)
		}

		if !uriPattern.MatchString(m.Engine) {
			add("%s: engine must start with go:// or alias://", where)
		}

		if managers[m.Alias] {
			add("%s: duplicate manager alias", where)
		}

		managers[m.Alias] = true
	}

	if len(p.Engines) == 0 {
		add("engines: at least one engine is required")
	}

	engines := map[string]Port{}
	for i, e := range p.Engines {
		where := fmt.Sprintf("engines[%d] (%s)", i, e.Alias)

		if !aliasPattern.MatchString(e.Alias) {
			add("%s: alias must be lowercase kebab-case", where)
		}

		if !ports[e.Type] {
			add("%s: type %q is not a known port", where, e.Type)
		}

		if !uriPattern.MatchString(e.Engine) {
			add("%s: engine must start with go:// or alias://", where)
		}

		if !managers[e.Manager] {
			add("%s: manager %q is not declared", where, e.Manager)
		}

		if _, seen := engines[e.Alias]; seen {
			add("%s: duplicate engine alias", where)
		}

		engines[e.Alias] = e.Type
	}

	requirePort := func(where, alias string, want Port) {
		got, ok := engines[alias]
		if !ok {
			add("%s: %q is not a declared engine", where, alias)

			return
		}

		if got != want {
			add("%s: %q is a %s engine, want %s", where, alias, got, want)
		}
	}

	if p.State == "" {
		add("state is required")
	} else {
		requirePort("state", p.State, PortState)
	}

	for i, t := range p.Triggers {
		requirePort(fmt.Sprintf("triggers[%d]", i), t, PortTrigger)
	}

	targets := map[string]bool{}
	for i, t := range p.Targets {
		where := fmt.Sprintf("targets[%d] (%s)", i, t.Alias)

		if !aliasPattern.MatchString(t.Alias) {
			add("%s: alias must be lowercase kebab-case", where)
		}

		if (t.Forge == "") == (t.ForgeCI == "") {
			add("%s: target needs exactly one of forge or forgeCI", where)
		}

		for _, r := range t.In {
			if !repos[r] {
				add("%s: in names unknown repo %q", where, r)
			}
		}

		if targets[t.Alias] {
			add("%s: duplicate target alias", where)
		}

		targets[t.Alias] = true
	}

	if len(p.Stages) == 0 {
		add("stages: at least one stage is required")
	}

	stages := map[string]bool{}
	for i, s := range p.Stages {
		where := fmt.Sprintf("stages[%d] (%s)", i, s.Name)

		if !aliasPattern.MatchString(s.Name) {
			add("%s: name must be lowercase kebab-case", where)
		}

		if stages[s.Name] {
			add("%s: duplicate stage name", where)
		}

		stages[s.Name] = true

		if s.Promotion != "" {
			requirePort(where+": promotion", s.Promotion, PortPromotion)
		}

		if s.Release != "" {
			requirePort(where+": release", s.Release, PortArtifact)
		}

		if len(s.Substages) == 0 {
			add("%s: at least one substage is required", where)
		}

		subs := map[string]bool{}
		for j, sub := range s.Substages {
			subWhere := fmt.Sprintf("%s: substages[%d] (%s)", where, j, sub.Name)

			if !aliasPattern.MatchString(sub.Name) {
				add("%s: name must be lowercase kebab-case", subWhere)
			}

			if subs[sub.Name] {
				add("%s: duplicate substage name", subWhere)
			}

			subs[sub.Name] = true

			requirePort(subWhere, sub.Engine, PortCompute)

			if !managers[sub.Manager] {
				add("%s: manager %q is not declared", subWhere, sub.Manager)
			}

			if len(sub.Targets) == 0 {
				add("%s: targets must name at least one target", subWhere)
			}

			for _, t := range sub.Targets {
				if !targets[t] {
					add("%s: targets names unknown target %q", subWhere, t)
				}
			}

			for _, g := range sub.Gates {
				requirePort(subWhere+": gates", g, PortGate)
			}
		}
	}

	if len(errs) == 0 {
		return nil
	}

	return fmt.Errorf("invalid pipeline:\n  %s", strings.Join(errs, "\n  "))
}
