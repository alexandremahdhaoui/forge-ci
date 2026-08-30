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

	maxOutput = 16384
)

var ErrEngine = errors.New("engine is not declared")

type StageReport struct {
	Name    string        `json:"name"`
	Runs    []citypes.Run `json:"runs"`
	Advance bool          `json:"advance"`
	Reason  string        `json:"reason"`
}

type Report struct {
	Revision citypes.Revision `json:"revision"`

	// Version is the one number this apply would release under, derived
	// before the first stage so the build stamp and the release tag cannot
	// disagree.
	Version string `json:"version,omitempty"`

	Actions []string      `json:"actions"`
	Stages  []StageReport `json:"stages"`

	// Minted says the revision reached state. A revision nobody minted was
	// never proven, so nothing downstream may act on it.
	Minted bool `json:"minted"`

	// Released is where each release landed, one per stage that declared one.
	Released []citypes.ArtifactOutput `json:"released,omitempty"`

	// Planned says this was a dry run. Nothing was written, anywhere, and
	// Actions is what a real run would do instead.
	Planned bool `json:"planned,omitempty"`

	// Superseded says a manager found drift in the pipeline's own resources,
	// corrected it, and made the correction durable. Nothing ran after that.
	//
	// This is not a failure. The run was measuring a tree that the reconcile
	// it just performed had rewritten, so continuing would prove the wrong
	// thing: the revision would carry the pipeline's own uncommitted output
	// and the release would refuse it. The change is already durable, so the
	// run it triggers reads the corrected state and reconciles to no change.
	Superseded bool `json:"superseded,omitempty"`
}

// Advanced reports whether anything blocked. A superseded report did not
// block: no stage failed, no gate refused, and the run that follows is the
// one carrying the work.
func (r Report) Advanced() bool {
	for _, s := range r.Stages {
		if !s.Advance {
			return false
		}
	}

	return true
}

// Options is how one verb was asked for.
//
// DryRun forbids every write and ends the run after the reconcile: no
// revision is resolved, no stage runs, nothing is minted or released. It
// takes the same code path a real run takes, with the write suppressed, so a
// plan that says nothing would change is a promise the next run keeps.
//
// Force rewrites what cannot be compared, which today is one thing: an
// Actions secret, whose value the API never returns.
type Options struct {
	DryRun bool
	Force  bool
}

type Controller struct {
	caller engineadapter.Caller
	git    gitadapter.Git
	now    func() time.Time

	state sync.Mutex
}

func New(caller engineadapter.Caller, git gitadapter.Git, now func() time.Time) *Controller {
	if now == nil {
		now = time.Now
	}

	return &Controller{caller: caller, git: git, now: now}
}

func (c *Controller) Apply(
	ctx context.Context, p config.Pipeline, root string, opts Options,
) (Report, error) {
	index := newIndex(p, root)

	// false: an apply converges what can be converged and leaves credentials
	// alone. Only a bootstrap is responsible for those.
	actions, changed, err := c.reconcileResources(ctx, p, index, root, false, opts)
	if err != nil {
		return Report{}, err
	}

	// A plan ends here. Resolving a revision would be harmless and running a
	// stage would not: a stage builds, writes and records, and none of that
	// is a plan.
	if opts.DryRun {
		return Report{Actions: actions, Planned: true}, nil
	}

	// A reconcile that changed something ends the run here, before the
	// revision is resolved.
	//
	// The reconcile above writes into the member checkouts, and the revision
	// below hashes each member's HEAD plus its uncommitted changes. Resolving
	// after converging measures the tree this run just rewrote: the revision
	// comes out dirty, the release refuses it, and every run repeats that
	// forever because a fresh clone starts from the same drift. Live run
	// 33309087584 died exactly this way.
	//
	// So the reconcile is complete - every resource, never one per run - and
	// the manager has already made its changes durable. The run those changes
	// trigger starts from the corrected state, finds no drift, and does the
	// work.
	if changed {
		return Report{Actions: actions, Superseded: true}, nil
	}

	// The revision is resolved here because a run record is keyed by it. It is
	// not written yet. A revision in state is a claim that this tuple of
	// commits was proven, and minting it before anything runs hands a broken
	// build a revision that can propagate.
	revision, err := c.resolveRevision(ctx, p, root)
	if err != nil {
		return Report{}, err
	}

	// The version is derived ONCE, before any stage runs, and carried from
	// here. The build stamp and the release tag are then the same number by
	// construction rather than by two computations agreeing: a binary that
	// reports a different version from the release it shipped in is a lie
	// the operator acts on.
	version, err := c.releaseVersion(ctx, p, index, root)
	if err != nil {
		return Report{}, err
	}

	report := Report{Revision: revision, Version: version, Actions: actions}

	for _, stage := range p.Stages {
		stageReport, err := c.applyStage(ctx, p, index, stage, revision, version, root)
		if err != nil {
			return Report{}, err
		}

		report.Stages = append(report.Stages, stageReport)

		if !stageReport.Advance {
			break
		}

		if stage.Mint {
			if err := c.mint(ctx, index, revision); err != nil {
				return Report{}, err
			}

			report.Minted = true
		}

		if stage.Release != "" {
			released, err := c.release(ctx, p, index, stage, revision, version, root, report.Stages)
			if err != nil {
				return Report{}, err
			}

			report.Released = append(report.Released, released)
		}
	}

	return report, nil
}

// release hands a proven revision to the artifact engine. It runs after the
// stage advanced, so nothing is published for a build that did not pass.
func (c *Controller) release(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	stage config.Stage,
	revision citypes.Revision,
	version string,
	root string,
	stages []StageReport,
) (citypes.ArtifactOutput, error) {
	engine, err := index.require(stage.Release, config.PortArtifact)
	if err != nil {
		return citypes.ArtifactOutput{}, err
	}

	spec := map[string]any{}
	for k, v := range engine.Spec {
		spec[k] = v
	}

	if _, ok := spec["root"]; !ok {
		spec["root"] = root
	}

	in := citypes.ArtifactInput{
		Revision:  revision.ID,
		Version:   version,
		TagPrefix: p.Versioning.TagPrefix,
		Repos:     revision.Repos,
		Artifacts: runArtifacts(stages),
		Spec:      spec,
	}

	var out citypes.ArtifactOutput

	if err := c.caller.Call(ctx, engine.Engine, ToolPublish, in, &out); err != nil {
		return citypes.ArtifactOutput{}, fmt.Errorf("releasing stage %q: %w", stage.Name, err)
	}

	return out, nil
}

// runArtifacts gathers everything the apply's runs built, in stage order.
// This is the channel a distribution rides: what the substages produced is
// exactly what a release may publish.
func runArtifacts(stages []StageReport) []forge.Artifact {
	out := []forge.Artifact{}

	for _, stage := range stages {
		for _, run := range stage.Runs {
			if run.Forge == nil {
				continue
			}

			out = append(out, run.Forge.Artifacts...)
		}
	}

	return out
}

// releaseVersion is the one number the whole factory is released under. It is
// derived here and nowhere else: there is no field to type a version into, so
// a number can never be re-pointed at a release that already exists, and no
// engine downstream may compute one of its own.
//
// It runs before the first stage, because the build stamp and the release tag
// have to be the same number and a build cannot wait for a release to decide.
//
// A pipeline that releases nothing gets no version. There is no line to read:
// a workspace root is not a repo and carries no tags, and asking it for one
// fails outright rather than answering empty. Builds then stamp what they
// always did.
func (c *Controller) releaseVersion(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	root string,
) (string, error) {
	home, releases := releaseHome(p, index, root)
	if !releases {
		return "", nil
	}

	previous, err := c.git.LatestTag(ctx, home, p.Versioning.TagPrefix)
	if err != nil {
		return "", fmt.Errorf("reading the last released version: %w", err)
	}

	level, err := c.bumpLevel(ctx, p, root, previous)
	if err != nil {
		return "", err
	}

	next, err := artifactcontroller.Bump(previous, level, p.Versioning.Cap)
	if err != nil {
		return "", fmt.Errorf("deciding the next version: %w", err)
	}

	return next, nil
}

// releaseHome is the checkout the version line lives in: the one the release
// is created in, because a workspace root is not a repo. It reports whether
// this pipeline releases at all; one that does not has no line and needs no
// version.
//
// The artifact engine names the remote as owner/name, and the checkout is
// that name's last segment under the root - the same derivation the compute
// engine makes from its own repo key. One declaration, so the repo the tags
// are read from and the repo the release is created in can never disagree.
func releaseHome(p config.Pipeline, index engineIndex, root string) (string, bool) {
	for _, stage := range p.Stages {
		if stage.Release == "" {
			continue
		}

		engine, err := index.require(stage.Release, config.PortArtifact)
		if err != nil {
			continue
		}

		if repo, _ := engine.Spec["repo"].(string); repo != "" {
			return filepath.Join(root, path.Base(repo)), true
		}

		return root, true
	}

	return "", false
}

// bumpLevel is how far the release moves. The semantic strategy reads every
// member's commit subjects since the last release, because a factory releases
// its members together and a breaking change in any one of them is breaking
// for the number they all carry.
func (c *Controller) bumpLevel(
	ctx context.Context,
	p config.Pipeline,
	root string,
	previous string,
) (artifactcontroller.Level, error) {
	switch p.Versioning.Strategy {
	case config.StrategyMinor:
		return artifactcontroller.LevelMinor, nil

	case config.StrategySemantic:
		tag := artifactcontroller.TagName(p.Versioning.TagPrefix, previous)

		subjects := []string{}

		for _, repo := range p.Repos {
			got, err := c.git.SubjectsSince(ctx, filepath.Join(root, repo.Name), tag)
			if err != nil {
				return 0, fmt.Errorf("reading what changed in %q: %w", repo.Name, err)
			}

			subjects = append(subjects, got...)
		}

		return artifactcontroller.HighestLevel(p.Versioning.Semantic, subjects), nil

	default:
		return artifactcontroller.LevelPatch, nil
	}
}

// mint records the revision as proven. Writing it twice is harmless, because
// the id is derived from the tuple and the record is the same.
func (c *Controller) mint(ctx context.Context, index engineIndex, revision citypes.Revision) error {
	return c.putJSON(ctx, index, KindRevision, revision.ID, toWire(revision))
}

func (c *Controller) applyStage(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	stage config.Stage,
	revision citypes.Revision,
	version string,
	root string,
) (StageReport, error) {
	report := StageReport{Name: stage.Name, Runs: make([]citypes.Run, len(stage.Substages))}

	failures := make([]error, len(stage.Substages))

	var wg sync.WaitGroup

	for i, sub := range stage.Substages {
		wg.Add(1)

		go func() {
			defer wg.Done()

			run, err := c.applySubstage(ctx, p, index, stage, sub, revision, version, root)
			report.Runs[i] = run
			failures[i] = err
		}()
	}

	wg.Wait()

	for _, err := range failures {
		if err != nil {
			return StageReport{}, err
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

func (c *Controller) applySubstage(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	stage config.Stage,
	sub config.Substage,
	revision citypes.Revision,
	version string,
	root string,
) (citypes.Run, error) {
	key := runKey(revision.ID, stage.Name, sub.Name)

	existing, err := c.getRun(ctx, index, key)
	if err != nil {
		return citypes.Run{}, err
	}

	if existing != nil && existing.Status == citypes.StatusPassed && allGatesPassed(*existing) {
		return *existing, nil
	}

	engine, err := index.require(sub.Engine, config.PortCompute)
	if err != nil {
		return citypes.Run{}, fmt.Errorf("stage %q substage %q: %w", stage.Name, sub.Name, err)
	}

	started := c.now()

	out, err := c.run(ctx, engine, citypes.RunInput{
		Revision: revision.ID,
		Version:  version,
		Stage:    stage.Name,
		Substage: sub.Name,
		Targets:  index.targets(p, sub.Targets),
		Params:   sub.Params,
		Repos:    checkouts(p, root, revision),
		Root:     root,
		Spec:     orEmpty(engine.Spec),
	})
	if err != nil {
		return citypes.Run{}, fmt.Errorf("stage %q substage %q: %w", stage.Name, sub.Name, err)
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
		Output:    tail(out.Output, maxOutput),
		Forge:     out.Forge,
	}

	gates, err := c.evaluateGates(ctx, index, sub, run)
	if err != nil {
		return citypes.Run{}, fmt.Errorf("stage %q substage %q: %w", stage.Name, sub.Name, err)
	}

	run.Gates = gates

	if err := c.putJSON(ctx, index, KindRun, key, run); err != nil {
		return citypes.Run{}, err
	}

	return run, nil
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

// reconcileResources realizes what every engine declares. bootstrap says
// whether this is the provisioning ceremony: when it is not, a resource the
// declaring engine marked bootstrapOnly is left alone.
//
// That is not an optimisation. A credential is written blind - nothing can
// be read back to compare against - so reconciling one is only writing it
// again, and a run that did so would have to hold the rights to rewrite the
// secrets it runs under. The platform draws the same line: a workflow's own
// token is excluded from the secrets API whatever its permissions say.
func (c *Controller) reconcileResources(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	root string,
	bootstrap bool,
	opts Options,
) ([]string, bool, error) {
	owned, err := c.readOwnership(ctx, index)
	if err != nil {
		return nil, false, err
	}

	byManager := map[string][]citypes.Resource{}

	for _, engine := range p.Engines {
		var declared citypes.DeclareOutput

		if err := c.caller.Call(ctx, engine.Engine, ToolDeclare,
			map[string]any{"spec": orEmpty(engine.Spec)}, &declared); err != nil {
			return nil, false, fmt.Errorf("asking engine %q what it needs: %w", engine.Alias, err)
		}

		// Everything declared is handed over, bootstrapOnly included. The
		// manager decides what to realize; dropping a resource here would
		// drop it from the ownership record too, and that record is what
		// stops another manager claiming it.
		byManager[engine.Manager] = append(byManager[engine.Manager], declared.Resources...)
	}

	aliases := make([]string, 0, len(byManager))
	for alias := range byManager {
		aliases = append(aliases, alias)
	}

	sort.Strings(aliases)

	actions := []string{}
	changed := false
	merged := map[string]citypes.Ownership{}

	for _, o := range owned {
		merged[o.Resource] = o
	}

	// Every manager is reconciled, and a manager that reported a change does
	// not cut the loop short. Converging one resource per run would need one
	// run per resource: a pipeline declaring a thousand resources would take
	// a thousand runs to update, each one reporting the same thing.
	for _, alias := range aliases {
		manager, err := index.manager(alias)
		if err != nil {
			return nil, false, err
		}

		var out citypes.ReconcileOutput

		// The pipeline root rides in the manager spec so a relative
		// resource name resolves against the root wherever forge-ci was
		// started from, while ownership ids stay root-relative.
		spec := orEmpty(manager.Spec)
		spec["root"] = root

		if err := c.caller.Call(ctx, manager.Engine, ToolReconcile, citypes.ReconcileInput{
			Manager:   alias,
			Resources: byManager[alias],
			Owned:     owned,
			Bootstrap: bootstrap,
			Spec:      spec,
			DryRun:    opts.DryRun,
			Force:     opts.Force,
		}, &out); err != nil {
			return nil, false, fmt.Errorf("manager %q: %w", alias, err)
		}

		actions = append(actions, out.Actions...)

		if out.Changed {
			changed = true
		}

		for _, o := range out.Owned {
			merged[o.Resource] = o
		}
	}

	// The ownership record is a write like any other, and a plan performs
	// none. Writing it would also commit and push the state repo, which is
	// the loudest thing a dry run could possibly do.
	if !opts.DryRun {
		if err := c.writeOwnership(ctx, index, merged); err != nil {
			return nil, false, err
		}
	}

	return actions, changed, nil
}

func (c *Controller) resolveRevision(
	ctx context.Context,
	p config.Pipeline,
	root string,
) (citypes.Revision, error) {
	revision := citypes.Revision{CreatedAt: c.now(), Repos: map[string]string{}}

	names := make([]string, 0, len(p.Repos))

	worktrees := map[string]string{}

	for _, repo := range p.Repos {
		dir := filepath.Join(root, repo.Name)

		sha, err := c.git.HeadSHA(ctx, dir)
		if err != nil {
			return citypes.Revision{}, fmt.Errorf("resolving the revision of %s: %w", repo.Name, err)
		}

		worktree, err := c.git.WorktreeHash(ctx, dir)
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
		revision.ID += "-dirty"
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

func tail(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	return "... earlier output dropped ...\n" + s[len(s)-limit:]
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
			Name: repo.Name,
			Path: filepath.Join(root, repo.Name),
			SHA:  revision.Repos[repo.Name],
			// The needs graph rides the checkout list every substage already
			// receives, so a local run and a remote one order the work from
			// the same declaration.
			Needs: repo.Needs,
		})
	}

	return out
}
