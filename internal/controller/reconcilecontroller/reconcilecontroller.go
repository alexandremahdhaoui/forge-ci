package reconcilecontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/engineadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
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
	KindRun      = "run"
	KindOwned    = "owned"

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
	Actions  []string         `json:"actions"`
	Stages   []StageReport    `json:"stages"`
}

func (r Report) Advanced() bool {
	for _, s := range r.Stages {
		if !s.Advance {
			return false
		}
	}

	return true
}

type Controller struct {
	caller engineadapter.Caller
	git    gitadapter.Git
	now    func() time.Time
}

func New(caller engineadapter.Caller, git gitadapter.Git, now func() time.Time) *Controller {
	if now == nil {
		now = time.Now
	}

	return &Controller{caller: caller, git: git, now: now}
}

func (c *Controller) Apply(ctx context.Context, p config.Pipeline, root string) (Report, error) {
	index := newIndex(p)

	actions, err := c.reconcileResources(ctx, p, index)
	if err != nil {
		return Report{}, err
	}

	revision, err := c.resolveRevision(ctx, p, root)
	if err != nil {
		return Report{}, err
	}

	if err := c.putJSON(ctx, index, KindRevision, revision.ID, revision); err != nil {
		return Report{}, err
	}

	report := Report{Revision: revision, Actions: actions}

	for _, stage := range p.Stages {
		stageReport, err := c.applyStage(ctx, p, index, stage, revision, root)
		if err != nil {
			return Report{}, err
		}

		report.Stages = append(report.Stages, stageReport)

		if !stageReport.Advance {
			break
		}
	}

	return report, nil
}

func (c *Controller) applyStage(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	stage config.Stage,
	revision citypes.Revision,
	root string,
) (StageReport, error) {
	report := StageReport{Name: stage.Name}

	for _, sub := range stage.Substages {
		run, err := c.applySubstage(ctx, p, index, stage, sub, revision, root)
		if err != nil {
			return StageReport{}, err
		}

		report.Runs = append(report.Runs, run)
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

func (c *Controller) reconcileResources(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
) ([]string, error) {
	owned, err := c.readOwnership(ctx, index)
	if err != nil {
		return nil, err
	}

	byManager := map[string][]citypes.Resource{}

	for _, engine := range p.Engines {
		var declared citypes.DeclareOutput

		if err := c.caller.Call(ctx, engine.Engine, ToolDeclare,
			map[string]any{"spec": orEmpty(engine.Spec)}, &declared); err != nil {
			return nil, fmt.Errorf("asking engine %q what it needs: %w", engine.Alias, err)
		}

		byManager[engine.Manager] = append(byManager[engine.Manager], declared.Resources...)
	}

	aliases := make([]string, 0, len(byManager))
	for alias := range byManager {
		aliases = append(aliases, alias)
	}

	sort.Strings(aliases)

	actions := []string{}
	merged := map[string]citypes.Ownership{}

	for _, o := range owned {
		merged[o.Resource] = o
	}

	for _, alias := range aliases {
		manager, err := index.manager(alias)
		if err != nil {
			return nil, err
		}

		var out citypes.ReconcileOutput

		if err := c.caller.Call(ctx, manager.Engine, ToolReconcile, citypes.ReconcileInput{
			Manager:   alias,
			Resources: byManager[alias],
			Owned:     owned,
			Spec:      orEmpty(manager.Spec),
		}, &out); err != nil {
			return nil, fmt.Errorf("manager %q: %w", alias, err)
		}

		actions = append(actions, out.Actions...)

		for _, o := range out.Owned {
			merged[o.Resource] = o
		}
	}

	if err := c.writeOwnership(ctx, index, merged); err != nil {
		return nil, err
	}

	return actions, nil
}

func (c *Controller) resolveRevision(
	ctx context.Context,
	p config.Pipeline,
	root string,
) (citypes.Revision, error) {
	revision := citypes.Revision{CreatedAt: c.now(), Repos: map[string]string{}}

	names := make([]string, 0, len(p.Repos))

	for _, repo := range p.Repos {
		sha, err := c.git.HeadSHA(ctx, filepath.Join(root, repo.Name))
		if err != nil {
			return citypes.Revision{}, fmt.Errorf("resolving the revision of %s: %w", repo.Name, err)
		}

		revision.Repos[repo.Name] = sha
		names = append(names, repo.Name)
	}

	sort.Strings(names)

	parts := make([]string, 0, len(names)+1)
	parts = append(parts, p.Name)

	for _, name := range names {
		parts = append(parts, name+"="+revision.Repos[name])
	}

	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	revision.ID = hex.EncodeToString(sum[:])[:12]

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
		})
	}

	return out
}
