package reconcilecontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/engineadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactcontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

const (
	ToolReconcile = "reconcile"
	ToolDeclare   = "declare"
	ToolRun       = "run"
	ToolGet       = "get"
	ToolPut       = "put"
	ToolEvaluate  = "evaluate"
	ToolPoll      = "poll"

	KindRevision = "revision"

	ToolPublish = "publish"
	KindRun     = "run"
	KindOwned   = "owned"

	OwnedKey = "resources"
)

var ErrEngine = errors.New("engine is not declared")

type StageReport struct {
	Name    string        `json:"name"`
	Runs    []citypes.Run `json:"runs"`
	Advance bool          `json:"advance"`
	Reason  string        `json:"reason"`

	Reused int `json:"reused,omitempty"`

	Released []citypes.ArtifactOutput `json:"released,omitempty"`
}

type Report struct {
	Revision citypes.Revision `json:"revision"`

	Version string `json:"version,omitempty"`

	Actions []string      `json:"actions"`
	Stages  []StageReport `json:"stages"`

	Minted bool `json:"minted"`

	Released []citypes.ArtifactOutput `json:"released,omitempty"`

	Planned bool `json:"planned,omitempty"`

	Superseded bool `json:"superseded,omitempty"`

	NothingNew bool `json:"nothingNew,omitempty"`

	Skipped bool   `json:"skipped,omitempty"`
	Reason  string `json:"reason,omitempty"`

	Evaluation string `json:"evaluation,omitempty"`

	Reconciliation string `json:"reconciliation,omitempty"`
}

const (
	EvaluationSkip    = "skip"
	EvaluationProceed = "proceed"
)

const (
	ReconciliationSuperseded = "superseded"
	ReconciliationConverged  = "converged"
)

func (r Report) Advanced() bool {
	for _, s := range r.Stages {
		if !s.Advance {
			return false
		}
	}

	return true
}

type Options struct {
	DryRun bool
	Force  bool

	Phase string

	Stage    string
	Substage string

	Revision string
}

const (
	PhaseAll           = ""
	PhaseSelfReconcile = "self-reconcile"
	PhaseEvaluate      = "evaluate"
	PhaseStages        = "stages"
)

var Phases = []string{PhaseSelfReconcile, PhaseEvaluate, PhaseStages}

type Controller struct {
	caller engineadapter.Caller
	git    gitadapter.Git
	fs     fsadapter.FS
	now    func() time.Time

	state sync.Mutex
}

func New(caller engineadapter.Caller, git gitadapter.Git, now func() time.Time) *Controller {
	if now == nil {
		now = time.Now
	}

	return &Controller{caller: caller, git: git, fs: fsadapter.New(), now: now}
}

func (c *Controller) Apply(
	ctx context.Context, p config.Pipeline, root string, opts Options,
) (Report, error) {
	index := newIndex(p, root)

	phase := opts.Phase

	var actions []string

	if phase == PhaseAll || phase == PhaseSelfReconcile {
		return c.applyFrom(ctx, p, index, root, opts, phase)
	}

	return c.applyPhases(ctx, p, index, root, opts, actions)
}

func (c *Controller) applyFrom(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	root string,
	opts Options,
	phase string,
) (Report, error) {
	actions, changed, published, err := c.reconcileResources(ctx, p, index, root, false, opts)
	if err != nil {
		return Report{}, err
	}

	if opts.DryRun {
		return Report{Actions: actions, Planned: true}, nil
	}

	if changed && published {
		return Report{Actions: actions, Superseded: true, Reconciliation: ReconciliationSuperseded}, nil
	}

	if changed {
		actions = append(actions,
			"reconcile changed resources nothing publishes, so no push will re-fire this pipeline; continuing on the converged state")
	}

	if phase == PhaseSelfReconcile {
		return Report{Actions: actions, Reconciliation: ReconciliationConverged}, nil
	}

	return c.applyPhases(ctx, p, index, root, opts, actions)
}

func (c *Controller) applyPhases(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	root string,
	opts Options,
	actions []string,
) (Report, error) {
	var err error

	phase := opts.Phase

	var (
		revision citypes.Revision
		pinned   releaseDecision
	)

	if opts.Revision != "" {
		revision, pinned, err = c.pinnedRevision(ctx, p, index, root, opts.Revision)
	} else {
		revision, err = c.resolveRevision(ctx, p, root)
	}

	if err != nil {
		return Report{}, err
	}

	var decision releaseDecision

	if phase == PhaseAll || phase == PhaseEvaluate {
		decision, err = c.decideRelease(ctx, p, index, root, revision)
		if err != nil {
			return Report{}, err
		}

		if phase == PhaseEvaluate {
			decision.Revision = revision

			if err := c.writeEvaluation(ctx, index, revision.ID, decision); err != nil {
				return Report{}, err
			}
		}
	} else if opts.Revision != "" {
		decision = pinned
	} else {
		decision, err = c.readEvaluation(ctx, index, revision.ID)
		if err != nil {
			return Report{}, err
		}
	}

	report := Report{Revision: revision, Version: decision.Version, Actions: actions}

	if decision.Skip {
		report.Skipped = true
		report.Reason = decision.Reason
		report.Evaluation = EvaluationSkip
		report.Released = []citypes.ArtifactOutput{{URL: decision.URL, Reason: decision.Reason}}

		return report, nil
	}

	if phase == PhaseEvaluate || phase == PhaseAll {
		if err := c.mint(ctx, index, revision); err != nil {
			return Report{}, err
		}

		report.Minted = true
	}

	if phase == PhaseEvaluate {
		report.Evaluation = EvaluationProceed

		if decision.Reason != "" {
			report.Actions = append(report.Actions, decision.Reason)
		}

		return report, nil
	}

	if opts.Stage != "" {
		return c.applyNamedStage(ctx, p, index, root, revision, decision, opts, report)
	}

	for _, stage := range p.Stages {
		var carried []forge.Artifact

		if publishes(index, stage) {
			carried, err = c.carryForward(ctx, index, revision, root, stagesBefore(p, stage.Name), usesOf(stage))
			if err != nil {
				return Report{}, fmt.Errorf("carrying what the stages before %q built: %w", stage.Name, err)
			}
		}

		stageReport, err := c.applyStage(ctx, p, index, stage, revision, decision, root, carried)
		if err != nil {
			return Report{}, err
		}

		report.Stages = append(report.Stages, stageReport)
		report.Released = append(report.Released, stageReport.Released...)

		if !stageReport.Advance {
			break
		}
	}

	total, reused := 0, 0
	for _, s := range report.Stages {
		total += len(s.Runs)
		reused += s.Reused
	}

	report.NothingNew = total > 0 && reused == total

	return report, nil
}

func releaseHome(p config.Pipeline, index engineIndex, root string) (string, bool) {
	engine, ok := firstArtifactEngine(p, index)
	if !ok {
		return "", false
	}

	if repo, _ := engine.Spec["repo"].(string); repo != "" {
		return filepath.Join(root, path.Base(repo)), true
	}

	return root, true
}

func firstArtifactEngine(p config.Pipeline, index engineIndex) (config.Engine, bool) {
	for _, stage := range p.Stages {
		for _, sub := range stage.Substages {
			engine, err := index.require(sub.Engine, config.PortArtifact)
			if err != nil {
				continue
			}

			return engine, true
		}
	}

	return config.Engine{}, false
}

type commitScan struct {
	Level    artifactcontroller.Level
	Count    int
	Deciding string
	Ignored  string
	Semantic bool
}

func (c *Controller) bumpLevel(
	ctx context.Context,
	p config.Pipeline,
	root string,
	previous string,
	set []string,
) (commitScan, error) {
	switch p.Versioning.Strategy {
	case config.StrategyMinor:
		return commitScan{Level: artifactcontroller.LevelMinor}, nil

	case config.StrategySemantic:
		tag := artifactcontroller.TagName(p.Versioning.TagPrefix, previous)
		vocab := p.Versioning.Semantic
		prefix := p.Versioning.CommitPrefix()

		scan := commitScan{Semantic: true}

		scoring := vocab
		if scoring.Unmatched == config.UnmatchedError {
			scoring.Unmatched = "patch"
		}

		for _, name := range set {
			got, err := c.git.SubjectsSince(ctx, filepath.Join(root, name), tag)
			if err != nil {
				return commitScan{}, fmt.Errorf("reading what changed in %q: %w", name, err)
			}

			if vocab.Unmatched == config.UnmatchedError && len(got) > 0 {
				if unclaimed := artifactcontroller.Unclaimed(vocab, prefix, got[:1]); len(unclaimed) > 0 {
					return commitScan{}, fmt.Errorf(
						"%w: the newest commit in %s, %q, starts with no known release kind. Use one of: %s. Push a commit with a known kind on top; older commits are not held to the rule",
						ErrUnclaimedSubject, name, unclaimed[0], strings.Join(knownKinds(vocab, prefix), ", "))
				}
			}

			for _, subject := range got {
				scan.Count++

				level := artifactcontroller.Classify(scoring, prefix, subject)
				where := fmt.Sprintf("%q in %s", subject, name)

				if level == artifactcontroller.LevelNone && scan.Ignored == "" {
					scan.Ignored = where
				}

				if level > scan.Level {
					scan.Level, scan.Deciding = level, where
				}
			}
		}

		return scan, nil

	default:
		return commitScan{Level: artifactcontroller.LevelPatch}, nil
	}
}

func knownKinds(vocab config.Semantic, prefix string) []string {
	out := []string{}

	for _, tokens := range [][]string{vocab.Major, vocab.Minor, vocab.Patch, vocab.Ignore} {
		out = append(out, tokens...)
	}

	if prefix != "" {
		out = append(out, prefix)
	}

	return out
}

func (c *Controller) mint(ctx context.Context, index engineIndex, revision citypes.Revision) error {
	var out citypes.StateGetOutput

	if err := c.callState(ctx, index, ToolGet, citypes.StateGetInput{
		Kind: KindRevision, Key: revision.ID, Spec: index.stateSpec,
	}, &out); err != nil {
		return err
	}

	if out.Found {
		same, err := sameBeyondItsTimestamp(out.Payload, revision)
		if err != nil {
			return fmt.Errorf("reading revision %q: %w", revision.ID, err)
		}

		if same {
			return nil
		}
	}

	return c.putJSON(ctx, index, KindRevision, revision.ID, toWire(revision))
}

const KindDependencyLock = "dependency-lock"

const lockManifestPath = ".forge/dependency-locks.json"

const generatedManifestPath = ".forge/factory-generated.json"

type generatedManifest struct {
	Version int      `json:"version"`
	Files   []string `json:"files"`
}

func generatedExclusions(fs fsadapter.FS, root string) map[string][]string {
	raw, err := fs.ReadFile(filepath.Join(root, filepath.FromSlash(generatedManifestPath)))
	if err != nil {
		return map[string][]string{}
	}

	var manifest generatedManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return map[string][]string{}
	}

	exclusions := map[string][]string{}

	for _, file := range manifest.Files {
		repo, rest, found := strings.Cut(path.Clean(file), "/")
		if !found || repo == "" || rest == "" || strings.HasPrefix(repo, ".") {
			continue
		}

		exclusions[repo] = append(exclusions[repo], rest)
	}

	return exclusions
}

type lockManifest struct {
	Version int `json:"version"`
	Locks   []struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	} `json:"locks"`
}

type dependencyLockRecord struct {
	Revision string `json:"revision"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Lockfile string `json:"lockfile"`
}

func (c *Controller) recordDependencyLocks(
	ctx context.Context,
	index engineIndex,
	revision citypes.Revision,
	root string,
) error {
	if !stateDeclaresKind(index.stateSpec, KindDependencyLock) {
		return nil
	}

	manifestPath := filepath.Join(root, filepath.FromSlash(lockManifestPath))

	exists, err := c.fs.Exists(manifestPath)
	if err != nil {
		return fmt.Errorf("reading the lock manifest: %w", err)
	}

	if !exists {
		return nil
	}

	raw, err := c.fs.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("reading the lock manifest: %w", err)
	}

	var manifest lockManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("reading the lock manifest: %w", err)
	}

	for _, lock := range manifest.Locks {
		content, err := c.fs.ReadFile(filepath.Join(root, filepath.FromSlash(lock.Path)))
		if err != nil {
			return fmt.Errorf("recording dependency lock %s: %w", lock.Path, err)
		}

		sum := sha256.Sum256(content)
		if got := hex.EncodeToString(sum[:]); got != lock.SHA256 {
			return fmt.Errorf(
				"recording dependency lock %s: the file changed since the lock resolved it "+
					"(manifest says %s, the file hashes to %s)", lock.Path, lock.SHA256, got)
		}

		err = c.putJSON(ctx, index, KindDependencyLock, revision.ID+"/"+lock.Path,
			dependencyLockRecord{
				Revision: revision.ID,
				Path:     lock.Path,
				SHA256:   lock.SHA256,
				Lockfile: string(content),
			})
		if err != nil {
			return err
		}
	}

	return nil
}

func stateDeclaresKind(spec map[string]any, kind string) bool {
	list, ok := spec["kinds"].([]any)
	if !ok {
		return false
	}

	for _, e := range list {
		if name, ok := e.(string); ok && name == kind {
			return true
		}
	}

	return false
}

func (c *Controller) applyStage(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	stage config.Stage,
	revision citypes.Revision,
	decision releaseDecision,
	root string,
	carried []forge.Artifact,
) (StageReport, error) {
	report := StageReport{Name: stage.Name, Runs: make([]citypes.Run, len(stage.Substages))}

	at := make(map[string]int, len(stage.Substages))
	for i, sub := range stage.Substages {
		at[sub.Name] = i
	}

	waves, err := substageWaves(stage)
	if err != nil {
		return StageReport{}, err
	}

	failures := make([]error, len(stage.Substages))
	reused := make([]bool, len(stage.Substages))
	published := make([]citypes.ArtifactOutput, len(stage.Substages))
	advanced := make(map[string]bool, len(stage.Substages))

	for _, wave := range waves {
		var wg sync.WaitGroup

		for _, name := range wave {
			i := at[name]
			sub := stage.Substages[i]

			if held := heldBy(sub, advanced); held != "" {
				report.Runs[i] = citypes.Run{
					Revision: revision.ID, Stage: stage.Name, Substage: sub.Name, Engine: sub.Engine,
					Status: citypes.StatusFailed, StartedAt: c.now(),
					Message: fmt.Sprintf("not run: needs %q, which did not pass", held),
				}

				continue
			}

			wg.Add(1)

			go func() {
				defer wg.Done()

				run, wasReused, out, err := c.applySubstage(ctx, p, index, stage, sub, revision, decision, root, carried)
				report.Runs[i] = run
				reused[i] = wasReused
				published[i] = out
				failures[i] = err
			}()
		}

		wg.Wait()

		for _, err := range failures {
			if err != nil {
				return StageReport{}, err
			}
		}

		for _, name := range wave {
			run := report.Runs[at[name]]
			advanced[name] = run.Status == citypes.StatusPassed && allGatesPassed(run)
		}
	}

	for _, r := range reused {
		if r {
			report.Reused++
		}
	}

	for _, out := range published {
		if out.Published || out.URL != "" {
			report.Released = append(report.Released, out)
		}
	}

	advance, reason, err := c.promote(ctx, index, stage, report.Runs)
	if err != nil {
		return StageReport{}, err
	}

	report.Advance = advance
	report.Reason = reason

	return report, nil
}

func substageWaves(stage config.Stage) ([][]string, error) {
	names := stage.SubstageNames()
	needs := stage.SubstageNeeds()

	if len(needs) == 0 {
		return [][]string{names}, nil
	}

	waves, err := citypes.Waves(names, needs)
	if err != nil {
		return nil, fmt.Errorf("stage %q: %w", stage.Name, err)
	}

	return waves, nil
}

func heldBy(sub config.Substage, advanced map[string]bool) string {
	for _, n := range sub.Needs {
		if !advanced[n] {
			return n
		}
	}

	return ""
}

func (c *Controller) applySubstage(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	stage config.Stage,
	sub config.Substage,
	revision citypes.Revision,
	decision releaseDecision,
	root string,
	carried []forge.Artifact,
) (citypes.Run, bool, citypes.ArtifactOutput, error) {
	key := runKey(revision.ID, stage.Name, sub.Name)

	existing, err := c.getRun(ctx, index, key)
	if err != nil {
		return citypes.Run{}, false, citypes.ArtifactOutput{}, err
	}

	if existing != nil && existing.Status == citypes.StatusPassed && allGatesPassed(*existing) {
		atHand, err := c.artifactsAtHand(ctx, index, sub, revision.ID, root, existing)
		if err != nil {
			return citypes.Run{}, false, citypes.ArtifactOutput{}, err
		}

		if atHand {
			return *existing, true, citypes.ArtifactOutput{}, nil
		}
	}

	if engine, err := index.require(sub.Engine, config.PortArtifact); err == nil {
		return c.publishSubstage(ctx, p, index, stage, sub, engine, revision, decision, root, carried)
	}

	engine, err := index.require(sub.Engine, config.PortCompute)
	if err != nil {
		return citypes.Run{}, false, citypes.ArtifactOutput{}, fmt.Errorf("stage %q substage %q: %w", stage.Name, sub.Name, err)
	}

	started := c.now()

	out, err := c.run(ctx, engine, citypes.RunInput{
		Revision: revision.ID,
		Version:  decision.Version,
		Stage:    stage.Name,
		Substage: sub.Name,
		Sync:     sub.Sync,
		Targets:  index.targets(p, sub.Targets),
		Params:   sub.Params,
		Repos:    checkouts(p, root, revision),
		Root:     root,
		Spec:     orEmpty(engine.Spec),
	})
	if err != nil {
		return citypes.Run{}, false, citypes.ArtifactOutput{}, fmt.Errorf("stage %q substage %q: %w", stage.Name, sub.Name, err)
	}

	if out.Status == citypes.StatusPassed {
		if err := c.putArtifacts(ctx, engine, revision.ID, root, out.Forge); err != nil {
			return citypes.Run{}, false, citypes.ArtifactOutput{}, fmt.Errorf("stage %q substage %q: keeping what it built: %w", stage.Name, sub.Name, err)
		}

		if err := c.recordDependencyLocks(ctx, index, revision, root); err != nil {
			return citypes.Run{}, false, citypes.ArtifactOutput{}, fmt.Errorf(
				"stage %q substage %q: recording the closure it was proven with: %w",
				stage.Name, sub.Name, err)
		}
	}

	run := citypes.Run{
		Revision:  revision.ID,
		Stage:     stage.Name,
		Substage:  sub.Name,
		Engine:    sub.Engine,
		Status:    out.Status,
		StartedAt: started,
		Duration:  c.now().Sub(started).Seconds(),
		Message:   out.Message,
		Output:    out.Output,
		Forge:     out.Forge,
	}

	gates, err := c.evaluateGates(ctx, index, sub, run)
	if err != nil {
		return citypes.Run{}, false, citypes.ArtifactOutput{}, fmt.Errorf("stage %q substage %q: %w", stage.Name, sub.Name, err)
	}

	run.Gates = gates

	if err := c.putJSON(ctx, index, KindRun, key, run); err != nil {
		return citypes.Run{}, false, citypes.ArtifactOutput{}, err
	}

	return run, false, citypes.ArtifactOutput{}, nil
}

func (c *Controller) evaluateGates(
	ctx context.Context,
	index engineIndex,
	sub config.Substage,
	run citypes.Run,
) ([]citypes.GateResult, error) {
	results := make([]citypes.GateResult, 0, len(sub.Gates))

	for _, alias := range sub.Gates {
		engine, err := index.require(alias, config.PortGate)
		if err != nil {
			return nil, err
		}

		var result citypes.GateResult

		if err := c.caller.Call(ctx, engine.Engine, ToolEvaluate,
			citypes.GateInput{Run: run, Spec: orEmpty(engine.Spec)}, &result); err != nil {
			return nil, err
		}

		result.Alias = alias
		results = append(results, result)
	}

	return results, nil
}

func (c *Controller) promote(
	ctx context.Context,
	index engineIndex,
	stage config.Stage,
	runs []citypes.Run,
) (bool, string, error) {
	if stage.Promotion == "" {
		for _, run := range runs {
			if run.Status != citypes.StatusPassed || !allGatesPassed(run) {
				return false, fmt.Sprintf("stage %q is not finished", stage.Name), nil
			}
		}

		return true, fmt.Sprintf("stage %q passed every substage", stage.Name), nil
	}

	engine, err := index.require(stage.Promotion, config.PortPromotion)
	if err != nil {
		return false, "", fmt.Errorf("stage %q: %w", stage.Name, err)
	}

	var out citypes.PromotionOutput

	if err := c.caller.Call(ctx, engine.Engine, ToolEvaluate,
		citypes.PromotionInput{Stage: stage.Name, Runs: runs, Spec: orEmpty(engine.Spec)}, &out); err != nil {
		return false, "", fmt.Errorf("stage %q: %w", stage.Name, err)
	}

	return out.Advance, out.Reason, nil
}

func (c *Controller) reconcileResources(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	root string,
	bootstrap bool,
	opts Options,
) ([]string, bool, bool, error) {
	owned, err := c.readOwnership(ctx, index)
	if err != nil {
		return nil, false, false, err
	}

	byManager := map[string][]citypes.Resource{}

	for _, engine := range p.Engines {
		var declared citypes.DeclareOutput

		if err := c.caller.Call(ctx, engine.Engine, ToolDeclare,
			citypes.DeclareInput{
				Spec: orEmpty(engine.Spec), Root: root, Stages: declaredStages(p), Repos: declaredRepos(p),
			}, &declared); err != nil {
			return nil, false, false, fmt.Errorf("asking engine %q what it needs: %w", engine.Alias, err)
		}

		byManager[engine.Manager] = append(byManager[engine.Manager], declared.Resources...)
	}

	aliases := make([]string, 0, len(byManager))
	for alias := range byManager {
		aliases = append(aliases, alias)
	}

	sort.Strings(aliases)

	actions := []string{}
	changed := false
	published := false
	merged := map[string]citypes.Ownership{}

	for _, o := range owned {
		merged[o.Resource] = o
	}

	for _, alias := range aliases {
		manager, err := index.manager(alias)
		if err != nil {
			return nil, false, false, err
		}

		var out citypes.ReconcileOutput

		spec := orEmpty(manager.Spec)
		spec["root"] = root

		if err := c.caller.Call(ctx, manager.Engine, ToolReconcile, citypes.ReconcileInput{
			Manager:      alias,
			Resources:    byManager[alias],
			Owned:        owned,
			Bootstrap:    bootstrap,
			Spec:         spec,
			DryRun:       opts.DryRun,
			Force:        opts.Force,
			CommitPrefix: p.Versioning.CommitPrefix(),
		}, &out); err != nil {
			return nil, false, false, fmt.Errorf("manager %q: %w", alias, err)
		}

		actions = append(actions, out.Actions...)

		if out.Changed {
			changed = true
		}

		if out.Published {
			published = true
		}

		for _, o := range out.Owned {
			merged[o.Resource] = o
		}
	}

	if !opts.DryRun {
		if err := c.writeOwnership(ctx, index, merged); err != nil {
			return nil, false, false, err
		}
	}

	return actions, changed, published, nil
}

func declaredStages(p config.Pipeline) []citypes.DeclaredStage {
	out := make([]citypes.DeclaredStage, 0, len(p.Stages))

	for _, stage := range p.Stages {
		subs := make([]citypes.DeclaredSubstage, 0, len(stage.Substages))
		for _, sub := range stage.Substages {
			subs = append(subs, citypes.DeclaredSubstage{
				Name: sub.Name, DisplayName: sub.DisplayName, Needs: sub.Needs, Uses: sub.Uses,
			})
		}

		out = append(out, citypes.DeclaredStage{
			Name: stage.Name, DisplayName: stage.DisplayName, Substages: subs,
		})
	}

	return out
}

func (c *Controller) resolveRevision(
	ctx context.Context,
	p config.Pipeline,
	root string,
) (citypes.Revision, error) {
	revision := citypes.Revision{CreatedAt: c.now(), Repos: map[string]string{}}

	names := make([]string, 0, len(p.Repos))

	worktrees := map[string]string{}

	exclusions := generatedExclusions(c.fs, root)

	for _, repo := range p.Repos {
		dir := filepath.Join(root, repo.Name)

		sha, err := c.git.HeadSHA(ctx, dir)
		if err != nil {
			return citypes.Revision{}, fmt.Errorf("resolving the revision of %s: %w", repo.Name, err)
		}

		worktree, err := c.git.WorktreeHash(ctx, dir, exclusions[repo.Name]...)
		if err != nil {
			return citypes.Revision{}, fmt.Errorf("resolving the revision of %s: %w", repo.Name, err)
		}

		revision.Repos[repo.Name] = sha
		worktrees[repo.Name] = worktree

		if worktree != "" {
			revision.Dirty = append(revision.Dirty, repo.Name)
		}

		names = append(names, repo.Name)
	}

	sort.Strings(names)

	parts := make([]string, 0, len(names)+1)
	parts = append(parts, p.Name)

	for _, name := range names {
		parts = append(parts, name+"="+revision.Repos[name]+"+"+worktrees[name])
	}

	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	revision.ID = hex.EncodeToString(sum[:])[:12]

	sort.Strings(revision.Dirty)

	if len(revision.Dirty) > 0 {
		revision.ID += citypes.DirtySuffix
	}

	return revision, nil
}

func (c *Controller) readOwnership(ctx context.Context, index engineIndex) ([]citypes.Ownership, error) {
	var out citypes.StateGetOutput

	if err := c.callState(ctx, index, ToolGet, citypes.StateGetInput{
		Kind: KindOwned, Key: OwnedKey, Spec: index.stateSpec,
	}, &out); err != nil {
		return nil, err
	}

	if !out.Found {
		return nil, nil
	}

	var owned []citypes.Ownership
	if err := json.Unmarshal([]byte(out.Payload), &owned); err != nil {
		return nil, fmt.Errorf("reading the ownership record: %w", err)
	}

	return owned, nil
}

func (c *Controller) writeOwnership(
	ctx context.Context,
	index engineIndex,
	merged map[string]citypes.Ownership,
) error {
	owned := make([]citypes.Ownership, 0, len(merged))
	for _, o := range merged {
		owned = append(owned, o)
	}

	sort.Slice(owned, func(i, j int) bool { return owned[i].Resource < owned[j].Resource })

	return c.putJSON(ctx, index, KindOwned, OwnedKey, owned)
}

func (c *Controller) getRun(ctx context.Context, index engineIndex, key string) (*citypes.Run, error) {
	var out citypes.StateGetOutput

	if err := c.callState(ctx, index, ToolGet, citypes.StateGetInput{
		Kind: KindRun, Key: key, Spec: index.stateSpec,
	}, &out); err != nil {
		return nil, err
	}

	if !out.Found {
		return nil, nil
	}

	var run citypes.Run
	if err := json.Unmarshal([]byte(out.Payload), &run); err != nil {
		return nil, fmt.Errorf("reading run %q: %w", key, err)
	}

	return &run, nil
}

func (c *Controller) putJSON(ctx context.Context, index engineIndex, kind, key string, value any) error {
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding %s %q: %w", kind, key, err)
	}

	return c.callState(ctx, index, ToolPut, citypes.StatePutInput{
		Kind: kind, Key: key, Payload: string(payload), Spec: index.stateSpec,
	}, nil)
}

func (c *Controller) callState(ctx context.Context, index engineIndex, tool string, in, out any) error {
	c.state.Lock()
	defer c.state.Unlock()

	if err := c.caller.Call(ctx, index.stateURI, tool, in, out); err != nil {
		return fmt.Errorf("state engine %q: %w", index.stateAlias, err)
	}

	return nil
}

func (c *Controller) run(ctx context.Context, engine config.Engine, in citypes.RunInput) (citypes.RunOutput, error) {
	var out citypes.RunOutput

	if err := c.caller.Call(ctx, engine.Engine, ToolRun, in, &out); err != nil {
		return citypes.RunOutput{}, err
	}

	return out, nil
}

func allGatesPassed(run citypes.Run) bool {
	for _, gate := range run.Gates {
		if gate.Status != citypes.StatusPassed {
			return false
		}
	}

	return true
}

func runKey(revision, stage, substage string) string {
	return revision + "/" + stage + "/" + substage
}

func checkouts(p config.Pipeline, root string, revision citypes.Revision) []citypes.RepoCheckout {
	out := make([]citypes.RepoCheckout, 0, len(p.Repos))

	for _, repo := range p.Repos {
		out = append(out, citypes.RepoCheckout{
			Name:  repo.Name,
			Path:  filepath.Join(root, repo.Name),
			SHA:   revision.Repos[repo.Name],
			Needs: repo.Needs,
		})
	}

	return out
}

func declaredRepos(p config.Pipeline) []citypes.DeclaredRepo {
	out := make([]citypes.DeclaredRepo, 0, len(p.Repos))

	for _, repo := range p.Repos {
		out = append(out, citypes.DeclaredRepo{Name: repo.Name})
	}

	return out
}
