package reconcilecontroller

import (
	"context"
	"fmt"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactcontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

// A release is a substage.
//
// It used to be a key on a stage, which bought nothing and cost two things.
// It needed its own phase, so a whole apply fired a stage's release the moment
// that stage advanced while a phased apply fired every release at the end -
// one config, two behaviours, chosen by nobody. And it made publishing a
// special kind of thing when it is an ordinary one: a substage that names an
// engine, a manager and a spec, and answers a run record like any other.
//
// What an artifact engine needs, a substage already carries. The version and
// the revision travel to every substage; the artifacts are what the stages
// before this one recorded. Ordering is stage order, the same tool that orders
// everything else - a release substage in a later stage cannot run unless the
// stages in front of it advanced, because the loop stops at the first that
// does not.

// publishSubstage runs one substage whose engine publishes. It answers a run
// like a compute substage does, so the promotion, the gates and the reuse
// record all read the same shape whatever the substage did.
func (c *Controller) publishSubstage(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	stage config.Stage,
	sub config.Substage,
	engine config.Engine,
	revision citypes.Revision,
	decision releaseDecision,
	root string,
	carried []forge.Artifact,
) (citypes.Run, bool, citypes.ArtifactOutput, error) {
	where := fmt.Sprintf("stage %q substage %q", stage.Name, sub.Name)
	started := c.now()

	run := citypes.Run{
		Revision:  revision.ID,
		Stage:     stage.Name,
		Substage:  sub.Name,
		Engine:    sub.Engine,
		Status:    citypes.StatusPassed,
		StartedAt: started,
	}

	out, err := c.publish(ctx, p, index, engine, revision, decision, root, carried)
	if err != nil {
		return citypes.Run{}, false, citypes.ArtifactOutput{}, fmt.Errorf("%s: %w", where, err)
	}

	run.Duration = c.now().Sub(started).Seconds()
	run.Message = out.Reason

	if out.Reason == "" {
		run.Message = "published " + decision.Version
	}

	gates, err := c.evaluateGates(ctx, index, sub, run)
	if err != nil {
		return citypes.Run{}, false, citypes.ArtifactOutput{}, fmt.Errorf("%s: %w", where, err)
	}

	run.Gates = gates

	if err := c.putJSON(ctx, index, KindRun, runKey(revision.ID, stage.Name, sub.Name), run); err != nil {
		return citypes.Run{}, false, citypes.ArtifactOutput{}, err
	}

	return run, false, out, nil
}

// publish hands the revision to one artifact engine and records what came
// back. Nothing here decides whether to publish: the stages in front of this
// substage decided it by advancing, and the evaluate phase decided the number.
func (c *Controller) publish(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	engine config.Engine,
	revision citypes.Revision,
	decision releaseDecision,
	root string,
	carried []forge.Artifact,
) (citypes.ArtifactOutput, error) {
	version := decision.Version

	spec := map[string]any{}
	for k, v := range engine.Spec {
		spec[k] = v
	}

	if _, ok := spec["root"]; !ok {
		spec["root"] = root
	}

	// The engine tags every repo it is handed, so what it must leave alone is
	// enforced by handing it less: a factory can hold members released
	// elsewhere, or a repo this pipeline writes into every run, and the
	// revision keeps pinning their shas while the release never touches them.
	ignored := map[string]bool{}
	for _, name := range config.IgnoreRepos(engine.Spec) {
		ignored[name] = true
	}

	repos := map[string]string{}

	for name, sha := range revision.Repos {
		if !ignored[name] {
			repos[name] = sha
		}
	}

	in := citypes.ArtifactInput{
		Revision:  revision.ID,
		Version:   version,
		TagPrefix: p.Versioning.TagPrefix,
		Repos:     repos,
		Artifacts: carried,
		Spec:      spec,
	}

	// The last check before anything is written: the bytes. What this run
	// would upload is digested and set against what the previous release
	// shipped. Identical, name for name and byte for byte, means the previous
	// release already is this release, and the revision is recorded under that
	// number so a rerun converges by the record.
	if decision.Previous != "" {
		previousTag := artifactcontroller.TagName(p.Versioning.TagPrefix, decision.Previous)

		prev, err := c.readRelease(ctx, index, "by-tag/"+previousTag)
		if err != nil {
			return citypes.ArtifactOutput{}, err
		}

		plan, err := artifactcontroller.New().Plan(in)
		if err != nil {
			return citypes.ArtifactOutput{}, err
		}

		same, err := sameBytes(root, plan.Uploads, prev)
		if err != nil {
			return citypes.ArtifactOutput{}, err
		}

		if same {
			rec := *prev
			rec.Revision = revision.ID
			rec.ReleasedAt = c.now()

			if err := c.putJSON(ctx, index, KindRelease, revision.ID, rec); err != nil {
				return citypes.ArtifactOutput{}, err
			}

			return citypes.ArtifactOutput{
				URL: prev.URL,
				Reason: fmt.Sprintf(
					"every asset is byte for byte what %s shipped; nothing to release", previousTag),
			}, nil
		}
	}

	var out citypes.ArtifactOutput

	if err := c.caller.Call(ctx, engine.Engine, ToolPublish, in, &out); err != nil {
		return citypes.ArtifactOutput{}, err
	}

	// Published or converged, the revision now carries this number. The record
	// is what stops the next run from deriving another.
	if out.Published || out.URL != "" {
		home, _ := spec["repo"].(string)

		err := c.writeRelease(ctx, index, releaseRecord{
			Revision:   revision.ID,
			Version:    version,
			Tag:        artifactcontroller.TagName(p.Versioning.TagPrefix, version),
			Repo:       home,
			Repos:      decision.Repos,
			Trees:      decision.Trees,
			Index:      out.Index,
			URL:        out.URL,
			ReleasedAt: c.now(),
		})
		if err != nil {
			return citypes.ArtifactOutput{}, err
		}
	}

	return out, nil
}

// stagesBefore is the stages in front of the named one. What they built is
// what a substage of this stage reads.
func stagesBefore(p config.Pipeline, name string) []config.Stage {
	for i, stage := range p.Stages {
		if stage.Name == name {
			return p.Stages[:i]
		}
	}

	return nil
}

// publishes answers whether this stage holds a substage that publishes, which
// is what decides whether a whole apply needs to bring the earlier stages'
// files back before running it. It has them on disk otherwise: put copies, so
// what a stage built is still where it built it.
func publishes(index engineIndex, stage config.Stage) bool {
	for _, sub := range stage.Substages {
		if _, err := index.require(sub.Engine, config.PortArtifact); err == nil {
			return true
		}
	}

	return false
}
