package config

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"

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
	uriPattern   = regexp.MustCompile(`^(forge|alias)://.+`)
)

type Pipeline struct {
	Name       string     `json:"name"`
	Versioning Versioning `json:"versioning,omitempty"`
	Repos      []Repo     `json:"repos,omitempty"`
	Managers   []Manager  `json:"managers,omitempty"`
	Engines    []Engine   `json:"engines"`
	State      string     `json:"state"`
	Triggers   []string   `json:"triggers,omitempty"`
	Targets    []Target   `json:"targets,omitempty"`
	Stages     []Stage    `json:"stages"`
}

type Versioning struct {
	TagPrefix string `json:"tagPrefix,omitempty"`

	Strategy string `json:"strategy,omitempty"`

	Cap string `json:"cap,omitempty"`

	Semantic Semantic `json:"semantic,omitempty"`

	IgnorePaths []string `json:"ignorePaths,omitempty"`

	SelfReconcileCommitPrefix string `json:"selfReconcileCommitPrefix,omitempty"`
}

func (v Versioning) CommitPrefix() string {
	if strings.TrimSpace(v.SelfReconcileCommitPrefix) == "" {
		return DefaultCommitPrefix
	}

	return v.SelfReconcileCommitPrefix
}

const DefaultCommitPrefix = "forge-ci:"

type Semantic struct {
	Major  []string `json:"major,omitempty"`
	Minor  []string `json:"minor,omitempty"`
	Patch  []string `json:"patch,omitempty"`
	Ignore []string `json:"ignore,omitempty"`

	Unmatched string `json:"unmatched,omitempty"`
}

const UnmatchedError = "error"

type Repo struct {
	Name  string   `json:"name"`
	URL   string   `json:"url"`
	Ref   string   `json:"ref,omitempty"`
	Needs []string `json:"needs,omitempty"`
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
	Alias  string   `json:"alias"`
	Binary string   `json:"binary"`
	Args   []string `json:"args,omitempty"`
	In     []string `json:"in,omitempty"`
}

type Stage struct {
	Name        string     `json:"name"`
	DisplayName string     `json:"displayName,omitempty"`
	Promotion   string     `json:"promotion,omitempty"`
	Substages   []Substage `json:"substages"`
}

type Substage struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"displayName,omitempty"`
	Engine      string            `json:"engine"`
	Targets     []string          `json:"targets,omitempty"`
	Gates       []string          `json:"gates,omitempty"`
	Params      map[string]string `json:"params,omitempty"`
	Sync        bool              `json:"sync,omitempty"`
	Needs       []string          `json:"needs,omitempty"`
	Uses        []string          `json:"uses,omitempty"`
}

func (s Stage) HasSubstage(name string) bool {
	for _, sub := range s.Substages {
		if sub.Name == name {
			return true
		}
	}

	return false
}

func (s Stage) SubstageNames() []string {
	names := make([]string, 0, len(s.Substages))
	for _, sub := range s.Substages {
		names = append(names, sub.Name)
	}

	return names
}

func (s Stage) SubstageNeeds() map[string][]string {
	needs := map[string][]string{}
	for _, sub := range s.Substages {
		if len(sub.Needs) > 0 {
			needs[sub.Name] = sub.Needs
		}
	}

	return needs
}

func retiredKeyHint(err error) error {
	if strings.Contains(err.Error(), `unknown field "artifactStorePath"`) {
		return fmt.Errorf("%w; artifactStorePath is retired: the store path is forge's, declared in each repo's forge.yaml, and forge-ci reads no store path of its own", err)
	}

	return err
}

func Parse(data []byte) (Pipeline, error) {
	var p Pipeline

	if err := yaml.UnmarshalStrict(data, &p); err != nil {
		return Pipeline{}, fmt.Errorf("reading pipeline: %w", retiredKeyHint(err))
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

	for _, msg := range p.Versioning.problems() {
		add("versioning: %s", msg)
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

	needs := map[string][]string{}
	names := make([]string, 0, len(p.Repos))

	for i, r := range p.Repos {
		for _, need := range r.Needs {
			switch {
			case need == r.Name:
				add("repos[%d] (%s): needs itself", i, r.Name)
			case !repos[need]:
				add("repos[%d] (%s): needs %q, which is not a declared repo", i, r.Name, need)
			}
		}

		if len(r.Needs) > 0 {
			needs[r.Name] = r.Needs
		}

		names = append(names, r.Name)
	}

	if _, err := citypes.Waves(names, needs); err != nil {
		add("repos: %s", err)
	}

	managers := map[string]bool{}
	for i, m := range p.Managers {
		where := fmt.Sprintf("managers[%d] (%s)", i, m.Alias)

		if !aliasPattern.MatchString(m.Alias) {
			add("%s: alias must be lowercase kebab-case", where)
		}

		if strings.HasPrefix(m.Engine, "go://") {
			add("%s: the go:// scheme is removed; use forge://", where)
		} else if !uriPattern.MatchString(m.Engine) {
			add("%s: engine must start with forge:// or alias://", where)
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
	namedManagers := map[string]bool{}

	for i, e := range p.Engines {
		where := fmt.Sprintf("engines[%d] (%s)", i, e.Alias)

		namedManagers[e.Manager] = true

		if !aliasPattern.MatchString(e.Alias) {
			add("%s: alias must be lowercase kebab-case", where)
		}

		if !ports[e.Type] {
			add("%s: type %q is not a known port", where, e.Type)
		}

		if strings.HasPrefix(e.Engine, "go://") {
			add("%s: the go:// scheme is removed; use forge://", where)
		} else if !uriPattern.MatchString(e.Engine) {
			add("%s: engine must start with forge:// or alias://", where)
		}

		if !managers[e.Manager] {
			add("%s: manager %q is not declared", where, e.Manager)
		}

		if _, seen := engines[e.Alias]; seen {
			add("%s: duplicate engine alias", where)
		}

		engines[e.Alias] = e.Type

		if e.Type == PortArtifact {
			ignored := map[string]bool{}

			for _, name := range IgnoreRepos(e.Spec) {
				if !repos[name] {
					add("%s: spec.ignoreRepos names %q, which is not a declared repo", where, name)
				}

				if ignored[name] {
					add("%s: spec.ignoreRepos repeats %q", where, name)
				}

				ignored[name] = true
			}
		}
	}

	for i, m := range p.Managers {
		if !namedManagers[m.Alias] {
			add("managers[%d] (%s): manager %q is named by no engine, so it is never called",
				i, m.Alias, m.Alias)
		}
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

		if strings.TrimSpace(t.Binary) == "" {
			add("%s: binary must name the executable to run", where)
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

			publishes := engines[sub.Engine] == PortArtifact
			if !publishes {
				requirePort(subWhere, sub.Engine, PortCompute)
			}

			switch {
			case publishes && len(sub.Targets) > 0:
				add("%s: an artifact substage publishes what the stages before it built "+
					"and runs no target, so it must declare none", subWhere)
			case !publishes && len(sub.Targets) == 0:
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

			for _, n := range sub.Needs {
				if n == sub.Name {
					add("%s: needs names itself", subWhere)
				} else if !s.HasSubstage(n) {
					add("%s: needs names %q, which is not a substage of stage %q", subWhere, n, s.Name)
				}
			}
		}

		for j, sub := range s.Substages {
			subWhere := fmt.Sprintf("%s: substages[%d] (%s)", where, j, sub.Name)

			for _, u := range sub.Uses {
				stageName, subName, ok := strings.Cut(u, "/")
				if !ok {
					add("%s: uses %q is not <stage>/<substage>", subWhere, u)

					continue
				}

				before, found := p.stageBefore(i, stageName)
				if !found {
					add("%s: uses names %q, which is not a stage before %q", subWhere, stageName, s.Name)
				} else if !before.HasSubstage(subName) {
					add("%s: uses names %q, which is not a substage of stage %q", subWhere, u, stageName)
				}
			}
		}

		if _, err := citypes.Waves(s.SubstageNames(), s.SubstageNeeds()); err != nil {
			add("%s: substages: %v", where, err)
		}
	}

	if len(p.Repos) > 1 {
		synced := false

		for _, s := range p.Stages {
			for _, sub := range s.Substages {
				if sub.Sync {
					synced = true
				}
			}
		}

		if !synced {
			add("stages: a pipeline over %d repos converges its workspace nowhere; "+
				"set sync: true on the substage that builds", len(p.Repos))
		}
	}

	if len(errs) == 0 {
		return nil
	}

	return fmt.Errorf("invalid pipeline:\n  %s", strings.Join(errs, "\n  "))
}

const (
	StrategyPatch    = "bump-patch-version"
	StrategyMinor    = "bump-minor-version"
	StrategySemantic = "semantic"
)

var strategies = map[string]bool{
	StrategyPatch:    true,
	StrategyMinor:    true,
	StrategySemantic: true,
}

var levels = map[string]bool{
	"major":        true,
	"minor":        true,
	"patch":        true,
	"ignore":       true,
	UnmatchedError: true,
}

var capPattern = regexp.MustCompile(`^v(0|[1-9]\d*)(\.(0|[1-9]\d*))?$`)

var tagPrefixPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func (v Versioning) problems() []string {
	var out []string

	if v.TagPrefix != "" && !tagPrefixPattern.MatchString(v.TagPrefix) {
		out = append(out, fmt.Sprintf("tagPrefix %q must be lowercase kebab-case", v.TagPrefix))
	}

	if v.Strategy != "" && !strategies[v.Strategy] {
		out = append(out, fmt.Sprintf(
			"strategy %q is not one of %s, %s, %s",
			v.Strategy, StrategyPatch, StrategyMinor, StrategySemantic))
	}

	if v.Cap != "" && !capPattern.MatchString(v.Cap) {
		out = append(out, fmt.Sprintf("cap %q must be a major or a major.minor, like v0 or v0.50", v.Cap))
	}

	if v.Semantic.Unmatched != "" && !levels[v.Semantic.Unmatched] {
		out = append(out, fmt.Sprintf(
			"semantic.unmatched %q must be major, minor, patch, ignore or error", v.Semantic.Unmatched))
	}

	for _, pattern := range v.IgnorePaths {
		if strings.TrimSpace(pattern) == "" {
			out = append(out, "ignorePaths carries an empty pattern")
		}
	}

	if v.SelfReconcileCommitPrefix != "" && strings.TrimSpace(v.SelfReconcileCommitPrefix) == "" {
		out = append(out, "selfReconcileCommitPrefix must not be blank: every commit would read as a self reconcile")
	}

	if v.Strategy != StrategySemantic && !v.Semantic.empty() {
		out = append(out, "semantic is set but strategy is not "+StrategySemantic)
	}

	return out
}

func (s Semantic) empty() bool {
	return len(s.Major) == 0 && len(s.Minor) == 0 && len(s.Patch) == 0 &&
		len(s.Ignore) == 0 && s.Unmatched == ""
}

func IgnoreRepos(spec map[string]any) []string {
	raw, ok := spec["ignoreRepos"].([]any)
	if !ok {
		return nil
	}

	out := make([]string, 0, len(raw))

	for _, v := range raw {
		if name, ok := v.(string); ok {
			out = append(out, name)
		}
	}

	return out
}

func (p Pipeline) stageBefore(at int, name string) (Stage, bool) {
	for i, s := range p.Stages {
		if i >= at {
			return Stage{}, false
		}

		if s.Name == name {
			return s, true
		}
	}

	return Stage{}, false
}
