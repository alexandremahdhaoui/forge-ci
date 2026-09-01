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
	Name string `json:"name"`
	// Versioning decides the one number every member is released under.
	// There is no field to type a version into, on purpose: a version is
	// derived or it is nothing, so it can never be re-typed onto a release
	// that already exists.
	Versioning        Versioning `json:"versioning,omitempty"`
	ArtifactStorePath string     `json:"artifactStorePath,omitempty"`
	Repos             []Repo     `json:"repos,omitempty"`
	Managers          []Manager  `json:"managers,omitempty"`
	Engines           []Engine   `json:"engines"`
	State             string     `json:"state"`
	Triggers          []string   `json:"triggers,omitempty"`
	Targets           []Target   `json:"targets,omitempty"`
	Stages            []Stage    `json:"stages"`
}

// Versioning is how one release number is derived for a whole factory. Every
// member is tagged with that number, so a workspace has one version line
// rather than one per repo.
type Versioning struct {
	// TagPrefix goes in front of the semver, with a dash. Empty means no
	// prefix, which is what every factory does today: v0.50.0. Set it only
	// when one repo is released by more than one factory, so the two lines
	// do not read each other's tags: "forge" gives forge-v0.50.0.
	TagPrefix string `json:"tagPrefix,omitempty"`

	// Strategy picks the bump. Empty means bump-patch-version.
	Strategy string `json:"strategy,omitempty"`

	// Cap is the ceiling the bump may not cross, inclusive. "v0" holds the
	// major at 0; "v0.50" holds major and minor. A bump that would cross it
	// drops one level and retries, so a factory that is not ready for v1
	// keeps releasing rather than stopping.
	Cap string `json:"cap,omitempty"`

	// Semantic is the vocabulary the semantic strategy reads commit
	// subjects with. It is ignored by the other strategies.
	Semantic Semantic `json:"semantic,omitempty"`
}

// Semantic is the vocabulary, not a standard. A team writes the prefixes it
// actually uses, emoji included, and nothing here assumes conventional
// commits.
type Semantic struct {
	Major  []string `json:"major,omitempty"`
	Minor  []string `json:"minor,omitempty"`
	Patch  []string `json:"patch,omitempty"`
	Ignore []string `json:"ignore,omitempty"`

	// Unmatched is the level a subject scores when no list claims it.
	// Empty means patch. Set it to "ignore" to make the vocabulary
	// exhaustive, so an unrecognised subject releases nothing.
	Unmatched string `json:"unmatched,omitempty"`
}

type Repo struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Ref  string `json:"ref,omitempty"`
	// Needs names the repos that must finish before this one starts, when a
	// target runs in both. It is a dependency and not an order: a substage
	// builds everything it can at once and holds back only what is waiting,
	// so adding a repo cannot silently reorder the others.
	//
	// A need on a repo the target does not name costs nothing. The
	// declaration says what must be built first when both are being built,
	// not that the other must be built at all.
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
	Alias   string   `json:"alias"`
	Forge   string   `json:"forge,omitempty"`
	ForgeCI string   `json:"forgeCI,omitempty"`
	In      []string `json:"in,omitempty"`
}

type Stage struct {
	Name    string `json:"name"`
	Mint    bool   `json:"mint,omitempty"`
	Release string `json:"release,omitempty"`
	// ReleaseRepos is the set of repos this stage's release publishes:
	// only these are handed to the artifact engine, so only these are
	// tagged. Empty means every repo, which is what a factory that owns
	// all its members wants. A factory that also carries repos released
	// elsewhere - a toolchain developed inside a consumer's checkout -
	// names its own here, and the revision keeps pinning the rest.
	ReleaseRepos []string   `json:"releaseRepos,omitempty"`
	Promotion    string     `json:"promotion,omitempty"`
	Substages    []Substage `json:"substages"`
}

type Substage struct {
	Name    string            `json:"name"`
	Engine  string            `json:"engine"`
	Manager string            `json:"manager"`
	Targets []string          `json:"targets"`
	Gates   []string          `json:"gates,omitempty"`
	Params  map[string]string `json:"params,omitempty"`
	// Sync makes the compute engine converge the workspace - manifests,
	// then the dependency closure - before this substage's targets run. A
	// multi-repo workspace builds against generated manifests, so at least
	// one substage must set it; Validate enforces that.
	Sync bool `json:"sync,omitempty"`
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

	// The needs graph, once every name is known. A repo that waits on
	// something no repos entry declares would wait forever and read as a
	// build that never started, so a typo is refused here rather than at
	// the substage that meets it.
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

	// A cycle is checked against every repo at once, which is the widest any
	// target can be. A subset of a graph with no cycle has none either, so
	// nothing that passes here can fail at a substage.
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
	for i, e := range p.Engines {
		where := fmt.Sprintf("engines[%d] (%s)", i, e.Alias)

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

		if len(s.ReleaseRepos) > 0 && s.Release == "" {
			add("%s: releaseRepos needs a release engine on the same stage", where)
		}

		released := map[string]bool{}
		for _, name := range s.ReleaseRepos {
			if !repos[name] {
				add("%s: releaseRepos names %q, which is not a declared repo", where, name)
			}

			if released[name] {
				add("%s: releaseRepos repeats %q", where, name)
			}

			released[name] = true
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

	// A multi-repo workspace builds against generated manifests, so a
	// pipeline that never converges them is building what nobody wrote. The
	// rule is structural: it knows a sync must happen somewhere, not which
	// substage needs it. A single-repo pipeline is exempt - there is no
	// workspace to converge.
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

// The strategies. A pipeline that names none bumps the patch, which is what
// every factory did before this field existed.
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

// levels are what Semantic.Unmatched may name. "ignore" makes the vocabulary
// exhaustive: a subject nothing claims releases nothing.
var levels = map[string]bool{
	"major":  true,
	"minor":  true,
	"patch":  true,
	"ignore": true,
}

// capPattern is a ceiling, not a version: "v0" or "v0.50". A full semver is
// refused because a cap on the patch would stop the only bump that always
// works, and a factory that cannot bump is a factory that cannot release.
var capPattern = regexp.MustCompile(`^v(0|[1-9]\d*)(\.(0|[1-9]\d*))?$`)

// tagPrefixPattern keeps a prefix to something a tag can carry and a shell
// can type. It joins the semver with a dash: "forge" -> forge-v0.50.0.
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
			"semantic.unmatched %q must be major, minor, patch or ignore", v.Semantic.Unmatched))
	}

	// A vocabulary nothing reads is a vocabulary somebody wrote expecting it
	// to work. Say so rather than ignoring it.
	if v.Strategy != StrategySemantic && !v.Semantic.empty() {
		out = append(out, "semantic is set but strategy is not "+StrategySemantic)
	}

	return out
}

func (s Semantic) empty() bool {
	return len(s.Major) == 0 && len(s.Minor) == 0 && len(s.Patch) == 0 &&
		len(s.Ignore) == 0 && s.Unmatched == ""
}
