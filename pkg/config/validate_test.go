package config_test

import (
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/stretchr/testify/require"
)

func base() config.Pipeline {
	return config.Pipeline{
		Name:     "demo",
		Repos:    []config.Repo{{Name: "a", URL: "git@example.com:a.git"}},
		Managers: []config.Manager{{Alias: "local", Engine: "go://x@v1"}},
		Engines: []config.Engine{
			{Alias: "here", Type: config.PortCompute, Engine: "go://x@v1", Manager: "local"},
			{Alias: "st", Type: config.PortState, Engine: "go://x@v1", Manager: "local"},
			{Alias: "watch", Type: config.PortTrigger, Engine: "go://x@v1", Manager: "local"},
		},
		State:    "st",
		Triggers: []string{"watch"},
		Targets:  []config.Target{{Alias: "build", Forge: "test-all", In: []string{"a"}}},
		Stages: []config.Stage{{
			Name: "build",
			Substages: []config.Substage{{
				Name: "default", Engine: "here", Manager: "local", Targets: []string{"build"},
			}},
		}},
	}
}

func requireInvalid(t *testing.T, p config.Pipeline, want string) {
	t.Helper()

	err := p.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), want)
}

func TestTheBaselineIsValid(t *testing.T) {
	require.NoError(t, base().Validate())
}

func TestNameIsRequired(t *testing.T) {
	p := base()
	p.Name = "   "

	requireInvalid(t, p, "name is required")
}

func TestARepoNeedsANameAndAURL(t *testing.T) {
	p := base()
	p.Repos = []config.Repo{{}}

	requireInvalid(t, p, "repos[0]: name is required")

	p.Repos = []config.Repo{{Name: "a"}}
	requireInvalid(t, p, "url is required")
}

func TestDuplicateRepoNamesAreRejected(t *testing.T) {
	p := base()
	p.Repos = append(p.Repos, config.Repo{Name: "a", URL: "git@example.com:a.git"})

	requireInvalid(t, p, "duplicate repo name")
}

func TestDuplicateAliasesAreRejectedEverywhere(t *testing.T) {
	p := base()
	p.Managers = append(p.Managers, config.Manager{Alias: "local", Engine: "go://x@v1"})
	requireInvalid(t, p, "duplicate manager alias")

	p = base()
	p.Targets = append(p.Targets, config.Target{Alias: "build", Forge: "x"})
	requireInvalid(t, p, "duplicate target alias")

	p = base()
	p.Stages = append(p.Stages, p.Stages[0])
	requireInvalid(t, p, "duplicate stage name")

	p = base()
	p.Stages[0].Substages = append(p.Stages[0].Substages, p.Stages[0].Substages[0])
	requireInvalid(t, p, "duplicate substage name")
}

func TestAManagerNeedsAValidAliasAndURI(t *testing.T) {
	p := base()
	p.Managers[0].Alias = "Local"
	requireInvalid(t, p, "alias must be lowercase kebab-case")

	p = base()
	p.Managers[0].Engine = "https://example.com"
	requireInvalid(t, p, "must start with go:// or alias://")
}

func TestAStageNeedsAKebabCaseName(t *testing.T) {
	p := base()
	p.Stages[0].Name = "Build"

	requireInvalid(t, p, "name must be lowercase kebab-case")
}

func TestASubstageNeedsAKebabCaseName(t *testing.T) {
	p := base()
	p.Stages[0].Substages[0].Name = "Default"

	requireInvalid(t, p, "name must be lowercase kebab-case")
}

func TestAStageNeedsASubstage(t *testing.T) {
	p := base()
	p.Stages[0].Substages = nil

	requireInvalid(t, p, "at least one substage is required")
}

func TestASubstageNeedsATarget(t *testing.T) {
	p := base()
	p.Stages[0].Substages[0].Targets = nil

	requireInvalid(t, p, "targets must name at least one target")
}

func TestASubstageNeedsADeclaredManager(t *testing.T) {
	p := base()
	p.Stages[0].Substages[0].Manager = "ghost"

	requireInvalid(t, p, `manager "ghost" is not declared`)
}

func TestATriggerMustBeATriggerEngine(t *testing.T) {
	p := base()
	p.Triggers = []string{"here"}

	requireInvalid(t, p, "is a compute engine, want trigger")

	p = base()
	p.Triggers = []string{"ghost"}
	requireInvalid(t, p, `"ghost" is not a declared engine`)
}

func TestStateIsRequired(t *testing.T) {
	p := base()
	p.State = ""

	requireInvalid(t, p, "state is required")
}

func TestAPromotionMustBeAPromotionEngine(t *testing.T) {
	p := base()
	p.Stages[0].Promotion = "here"

	requireInvalid(t, p, "is a compute engine, want promotion")
}

func TestATargetAliasMustBeKebabCase(t *testing.T) {
	p := base()
	p.Targets[0].Alias = "Build"

	requireInvalid(t, p, "alias must be lowercase kebab-case")
}

func TestEveryErrorIsReportedNotJustTheFirst(t *testing.T) {
	p := base()
	p.Name = ""
	p.State = ""
	p.Stages[0].Substages[0].Targets = []string{"ghost"}

	err := p.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "name is required")
	require.Contains(t, err.Error(), "state is required")
	require.Contains(t, err.Error(), "unknown target")
}
