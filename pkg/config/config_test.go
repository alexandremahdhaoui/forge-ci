package config_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/stretchr/testify/require"
)

const vectorPath = "../../.forge/spec-cache/cases.json"

type vectors struct {
	Valid []struct {
		Case string          `json:"case"`
		Why  string          `json:"why"`
		Doc  json.RawMessage `json:"doc"`
	} `json:"valid"`
	Invalid []struct {
		Case     string          `json:"case"`
		Why      string          `json:"why"`
		Error    string          `json:"error"`
		Semantic bool            `json:"semantic"`
		Doc      json.RawMessage `json:"doc"`
	} `json:"invalid"`
}

func load(t *testing.T) vectors {
	t.Helper()

	raw, err := os.ReadFile(vectorPath)
	require.NoError(t, err, "run the resolve-spec build stage first")

	var v vectors
	require.NoError(t, json.Unmarshal(raw, &v))
	require.NotEmpty(t, v.Valid)
	require.NotEmpty(t, v.Invalid)

	return v
}

func TestValidVectorsParse(t *testing.T) {
	for _, c := range load(t).Valid {
		t.Run(c.Case, func(t *testing.T) {
			_, err := config.Parse(c.Doc)
			require.NoError(t, err, c.Why)
		})
	}
}

func TestInvalidVectorsAreRejected(t *testing.T) {
	for _, c := range load(t).Invalid {
		t.Run(c.Case, func(t *testing.T) {
			_, err := config.Parse(c.Doc)
			require.Error(t, err, c.Why)
			require.Contains(t, err.Error(), c.Error)
		})
	}
}

func TestSemanticVectorsNeedTheSecondPass(t *testing.T) {
	for _, c := range load(t).Invalid {
		if !c.Semantic {
			continue
		}

		t.Run(c.Case, func(t *testing.T) {
			var p config.Pipeline
			require.NoError(t, json.Unmarshal(c.Doc, &p),
				"a semantic vector must be structurally well formed")
			require.Error(t, p.Validate())
		})
	}
}

func TestUnknownKeyIsRejected(t *testing.T) {
	_, err := config.Parse([]byte(`{"name":"x","pipelines":[]}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "pipelines")
}

func TestEveryPortIsAccepted(t *testing.T) {
	for _, p := range []config.Port{
		config.PortCompute,
		config.PortState,
		config.PortTrigger,
		config.PortGate,
		config.PortPromotion,
		config.PortArtifact,
	} {
		require.NotEmpty(t, string(p))
		require.Equal(t, strings.ToLower(string(p)), string(p))
	}
}

func TestAReleasingStageNeedsNoVersionOnThePipeline(t *testing.T) {
	t.Parallel()

	err := config.Pipeline{
		Name:     "demo",
		State:    "st",
		Managers: []config.Manager{{Alias: "local", Engine: "forge://m"}},
		Engines: []config.Engine{
			{Alias: "st", Type: config.PortState, Engine: "forge://x", Manager: "local"},
			{Alias: "gh", Type: config.PortArtifact, Engine: "forge://x", Manager: "local"},
			{Alias: "here", Type: config.PortCompute, Engine: "forge://x", Manager: "local"},
		},
		Targets: []config.Target{{Alias: "t", Binary: "forge", Args: []string{"test-all"}}},
		Stages: []config.Stage{{
			Name: "prod",
			Substages: []config.Substage{
				{Name: "default", Engine: "here", Targets: []string{"t"}},
				{Name: "publish", Engine: "gh"},
			},
		}},
	}.Validate()
	require.NoError(t, err)
}

func TestNoVersioningAtAllIsValid(t *testing.T) {
	t.Parallel()

	require.NoError(t, base().Validate())
}

func TestVersioningRejectsWhatItCannotActOn(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		set  func(*config.Pipeline)
		want string
	}{
		"a strategy nobody implements": {
			func(p *config.Pipeline) { p.Versioning.Strategy = "bump-everything" },
			"strategy \"bump-everything\" is not one of",
		},
		"a cap that is a full semver": {
			func(p *config.Pipeline) { p.Versioning.Cap = "v0.50.0" },
			"must be a major or a major.minor",
		},
		"a cap with no v": {
			func(p *config.Pipeline) { p.Versioning.Cap = "0.50" },
			"must be a major or a major.minor",
		},
		"a prefix that is not kebab-case": {
			func(p *config.Pipeline) { p.Versioning.TagPrefix = "Forge Toolchain" },
			"must be lowercase kebab-case",
		},
		"an unmatched level nobody scores": {
			func(p *config.Pipeline) {
				p.Versioning.Strategy = config.StrategySemantic
				p.Versioning.Semantic.Unmatched = "huge"
			},
			"must be major, minor, patch, ignore or error",
		},
		"a blank self reconcile prefix": {
			func(p *config.Pipeline) { p.Versioning.SelfReconcileCommitPrefix = "   " },
			"selfReconcileCommitPrefix must not be blank",
		},
		"a vocabulary nothing reads": {
			func(p *config.Pipeline) {
				p.Versioning.Strategy = config.StrategyPatch
				p.Versioning.Semantic.Minor = []string{"feat:"}
			},
			"semantic is set but strategy is not semantic",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := base()
			tc.set(&p)

			err := p.Validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestEveryStrategyAndCapShapeIsAccepted(t *testing.T) {
	t.Parallel()

	for _, v := range []config.Versioning{
		{Strategy: config.StrategyPatch},
		{Strategy: config.StrategyMinor},
		{Strategy: config.StrategySemantic, Semantic: config.Semantic{Minor: []string{"feat:"}}},
		{Cap: "v0"},
		{Cap: "v0.50"},
		{TagPrefix: "forge"},
		{TagPrefix: "forge-self"},
		{SelfReconcileCommitPrefix: "🤖"},
	} {
		p := base()
		p.Versioning = v
		require.NoError(t, p.Validate(), "%+v", v)
	}
}

func TestThereIsNoVersionFieldToTypeInto(t *testing.T) {
	t.Parallel()

	_, err := config.Parse([]byte(`
name: demo
version: v9.9.9
state: st
managers: [{alias: local, engine: "forge://m"}]
engines:
  - {alias: st, type: state, engine: "forge://x", manager: local}
  - {alias: here, type: compute, engine: "forge://x", manager: local}
targets: [{alias: t, binary: forge, args: [test-all]}]
stages:
  - name: prod
    substages: [{name: default, engine: here, targets: [t]}]
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "version")
}

func needsPipeline(repos ...config.Repo) config.Pipeline {
	return config.Pipeline{
		Name:     "demo",
		State:    "st",
		Repos:    repos,
		Managers: []config.Manager{{Alias: "local", Engine: "forge://m"}},
		Engines: []config.Engine{
			{Alias: "st", Type: config.PortState, Engine: "forge://x", Manager: "local"},
			{Alias: "here", Type: config.PortCompute, Engine: "forge://x", Manager: "local"},
		},
		Targets: []config.Target{{Alias: "t", Binary: "forge", Args: []string{"test-all"}}},
		Stages: []config.Stage{{
			Name: "build",
			Substages: []config.Substage{
				{Name: "default", Engine: "here", Targets: []string{"t"}, Sync: true},
			},
		}},
	}
}

func TestAMultiRepoPipelineThatNeverSyncsIsRejected(t *testing.T) {
	t.Parallel()

	p := needsPipeline(
		config.Repo{Name: "one", URL: "u"},
		config.Repo{Name: "two", URL: "u"},
	)
	p.Stages[0].Substages[0].Sync = false

	err := p.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "converges its workspace nowhere")
}

func TestASingleRepoPipelineNeedsNoSync(t *testing.T) {
	t.Parallel()

	p := needsPipeline(config.Repo{Name: "one", URL: "u"})
	p.Stages[0].Substages[0].Sync = false

	require.NoError(t, p.Validate())
}

func TestAValidNeedsGraphParses(t *testing.T) {
	t.Parallel()

	err := needsPipeline(
		config.Repo{Name: "spec", URL: "u"},
		config.Repo{Name: "impl", URL: "u", Needs: []string{"spec"}},
	).Validate()
	require.NoError(t, err)
}

func TestNeedingAnUndeclaredRepoIsRejected(t *testing.T) {
	t.Parallel()

	err := needsPipeline(
		config.Repo{Name: "impl", URL: "u", Needs: []string{"spec"}},
	).Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), `needs "spec"`)
}

func TestNeedingItselfIsRejected(t *testing.T) {
	t.Parallel()

	err := needsPipeline(
		config.Repo{Name: "impl", URL: "u", Needs: []string{"impl"}},
	).Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "needs itself")
}

func TestACycleBetweenReposIsRejected(t *testing.T) {
	t.Parallel()

	err := needsPipeline(
		config.Repo{Name: "one", URL: "u", Needs: []string{"two"}},
		config.Repo{Name: "two", URL: "u", Needs: []string{"one"}},
	).Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "cycle")
}

func TestNoNeedsAtAllIsValid(t *testing.T) {
	t.Parallel()

	err := needsPipeline(
		config.Repo{Name: "one", URL: "u"},
		config.Repo{Name: "two", URL: "u"},
	).Validate()
	require.NoError(t, err)
}

func substageNeedsPipeline(needs ...string) config.Pipeline {
	p := needsPipeline(config.Repo{Name: "one", URL: "u"})
	p.Stages[0].Substages = []config.Substage{
		{Name: "artifacts", Engine: "here", Targets: []string{"t"}},
		{Name: "container", Engine: "here", Targets: []string{"t"}, Needs: []string{"artifacts"}},
		{Name: "revision", Engine: "here", Targets: []string{"t"}, Needs: needs},
	}

	return p
}

func TestASubstageMayNeedAnotherOfItsStage(t *testing.T) {
	t.Parallel()

	require.NoError(t, substageNeedsPipeline("container").Validate())
}

func TestNeedingAnUndeclaredSubstageIsRejected(t *testing.T) {
	t.Parallel()

	err := substageNeedsPipeline("nope").Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), `needs names "nope"`)
	require.Contains(t, err.Error(), `not a substage of stage "build"`)
}

func TestASubstageCycleIsRejected(t *testing.T) {
	t.Parallel()

	self := substageNeedsPipeline("revision")
	err := self.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "needs names itself")

	loop := substageNeedsPipeline("container")
	loop.Stages[0].Substages[0].Needs = []string{"revision"}
	err = loop.Validate()
	require.Error(t, err)
	require.Contains(t, err.Error(), "cycle")
}

func TestTheOldTargetKeysAreRefused(t *testing.T) {
	t.Parallel()

	_, err := config.Parse([]byte(`
name: demo
state: st
managers: [{alias: local, engine: "forge://m"}]
engines:
  - {alias: st, type: state, engine: "forge://x", manager: local}
  - {alias: here, type: compute, engine: "forge://x", manager: local}
targets: [{alias: t, forge: test-all}]
stages:
  - name: prod
    substages: [{name: default, engine: here, targets: [t]}]
`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "forge")
}
