package reconcilecontroller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactcontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

// The release decision, in one place.
//
// A release is a function of the release set: the shas of the repos the
// release tags. So the question every apply must answer before it derives
// a number is whether this set was already released, and under which
// number. Four things answer it, in order:
//
//  1. the release record for this revision, which says it was;
//  2. the commit subjects since the last tag, which may say nothing in
//     them releases, or that one of them follows no convention;
//  3. the release record for the last tag, which says whether the set, or
//     the set minus the paths the factory ignores, moved at all;
//  4. after the build, the digests of what was built against the digests
//     the last tag shipped.
//
// Each answer that says "nothing new" ends the run converged, with no
// version derived, no tag cut and no engine called. golden run 19 did the
// opposite on all four counts and left six stray tags.

const (
	// KindRelease is the built-in state kind a released revision is written
	// under: once by revision id and once by tag.
	KindRelease = "release"

	// evaluationKeyPrefix is where a phased apply keeps the decision the
	// evaluate phase reached, so the stages and release phases in other
	// processes carry the same number.
	evaluationKeyPrefix = "evaluate-"
)

// ErrUnclaimedSubject is a commit subject no vocabulary list claims, under
// unmatched: error. The run cannot say what to release, so it says that.
var ErrUnclaimedSubject = errors.New("cannot decide what to release")

// ErrNoEvaluation means a stages or release phase ran before the evaluate
// phase recorded a decision for this revision.
var ErrNoEvaluation = errors.New("no evaluation recorded for this revision; run the evaluate phase first")

// releaseRecord is what a revision was released as. Repos are the shas of
// the release set; Trees are their tree hashes minus the ignored paths;
// Index is the distribution index the engine staged, as JSON text.
type releaseRecord struct {
	Revision   string            `json:"revision"`
	Version    string            `json:"version"`
	Tag        string            `json:"tag"`
	Repo       string            `json:"repo,omitempty"`
	Repos      map[string]string `json:"repos,omitempty"`
	Trees      map[string]string `json:"trees,omitempty"`
	Index      string            `json:"index,omitempty"`
	URL        string            `json:"url,omitempty"`
	ReleasedAt time.Time         `json:"releasedAt"`
}

// releaseDecision is what the evaluate phase decided for one revision. Skip
// says the run ends here, converged, for Reason. Otherwise Version is the
// number every stage stamps and the release tags, and Reason says why.
type releaseDecision struct {
	// Revision is the revision this decision was made for, whole: the id
	// every later record is keyed by, and the sha each repo stood at when
	// the run started. A phase running in a process of its own is handed
	// the id and reads the rest from here, so the run keeps one answer to
	// "which commits am I proving" from its first job to its last.
	Revision citypes.Revision  `json:"revision,omitempty"`
	Version  string            `json:"version,omitempty"`
	Tag      string            `json:"tag,omitempty"`
	Previous string            `json:"previous,omitempty"`
	Skip     bool              `json:"skip,omitempty"`
	Reason   string            `json:"reason,omitempty"`
	URL      string            `json:"url,omitempty"`
	Repos    map[string]string `json:"repos,omitempty"`
	Trees    map[string]string `json:"trees,omitempty"`
}

// decideRelease answers whether this revision has anything to release and,
// when it does, under which number. It runs before the first stage, because
// the build stamp and the release tag have to be the same number and a
// build cannot wait for a release to decide.
//
// A pipeline that releases nothing gets no version and never skips: there
// is no line to read and nothing to converge on.
func (c *Controller) decideRelease(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	root string,
	revision citypes.Revision,
) (releaseDecision, error) {
	home, releases := releaseHome(p, index, root)
	if !releases {
		return releaseDecision{}, nil
	}

	// 1. This exact revision was released. Its number is its number.
	if rec, err := c.readRelease(ctx, index, revision.ID); err != nil {
		return releaseDecision{}, err
	} else if rec != nil {
		return releaseDecision{
			Version: rec.Version, Tag: rec.Tag, Skip: true, URL: rec.URL,
			Reason: fmt.Sprintf("This revision was released as %s. Nothing to do.", rec.Version),
			Repos:  rec.Repos, Trees: rec.Trees,
		}, nil
	}

	set := releaseSet(p, revision)

	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}

	sort.Strings(names)

	previous, err := c.git.LatestTag(ctx, home, p.Versioning.TagPrefix)
	if err != nil {
		return releaseDecision{}, fmt.Errorf("reading the last released version: %w", err)
	}

	// 2. The subjects since the last tag. A range nothing in claims a
	// release for ends here; a subject outside the vocabulary fails here.
	scan, err := c.bumpLevel(ctx, p, root, previous, names)
	if err != nil {
		return releaseDecision{}, err
	}

	previousTag := artifactcontroller.TagName(p.Versioning.TagPrefix, previous)

	if previous != "" && scan.Level == artifactcontroller.LevelNone {
		return releaseDecision{
			Skip: true, Previous: previous, Reason: nothingReleasable(previousTag, scan),
		}, nil
	}

	trees, err := c.treeHashes(ctx, root, names, p.Versioning.IgnorePaths)
	if err != nil {
		return releaseDecision{}, err
	}

	// 3. The last release's set. Same shas, or same trees under the paths
	// that count, is the same release.
	if previous != "" {
		prev, err := c.readRelease(ctx, index, "by-tag/"+previousTag)
		if err != nil {
			return releaseDecision{}, err
		}

		if prev != nil && len(prev.Repos) > 0 && sameMap(prev.Repos, set) {
			return releaseDecision{
				Version: prev.Version, Tag: prev.Tag, Previous: previous, Skip: true, URL: prev.URL,
				Reason: fmt.Sprintf("Nothing to release. The release set holds the same code as %s.", previousTag),
				Repos:  set, Trees: trees,
			}, nil
		}

		if prev != nil && len(prev.Trees) > 0 && len(p.Versioning.IgnorePaths) > 0 && sameMap(prev.Trees, trees) {
			return releaseDecision{
				Version: prev.Version, Tag: prev.Tag, Previous: previous, Skip: true, URL: prev.URL,
				Reason: fmt.Sprintf("Nothing to release. The release set holds the same code as %s. Only ignored paths changed.", previousTag),
				Repos:  set, Trees: trees,
			}, nil
		}
	}

	next, err := artifactcontroller.Bump(previous, scan.Level, p.Versioning.Cap)
	if err != nil {
		return releaseDecision{}, fmt.Errorf("deciding the next version: %w", err)
	}

	return releaseDecision{
		Version: next, Tag: artifactcontroller.TagName(p.Versioning.TagPrefix, next),
		Previous: previous, Repos: set, Trees: trees,
		Reason: releasing(next, previous, scan),
	}, nil
}

// nothingReleasable says, in plain words, why a range since the last tag
// releases nothing: no commit at all, or only commits of a kind that never
// releases, with one of them named so the reader can see the kind.
func nothingReleasable(previousTag string, scan commitScan) string {
	if scan.Count == 0 {
		return fmt.Sprintf("Nothing to release. No repo in the release set has a new commit since %s.", previousTag)
	}

	if scan.Count == 1 {
		return fmt.Sprintf("Nothing to release. 1 commit since %s, and it is a kind that never releases: %s.",
			previousTag, scan.Ignored)
	}

	return fmt.Sprintf("Nothing to release. %d commits since %s, and each one is a kind that never releases. Example: %s.",
		scan.Count, previousTag, scan.Ignored)
}

// releasing says what the next release is and, when a commit decided the
// level, which one.
func releasing(next, previous string, scan commitScan) string {
	if previous == "" {
		return fmt.Sprintf("Release %s: the first release.", next)
	}

	if scan.Semantic && scan.Deciding != "" {
		return fmt.Sprintf("Release %s (%s): %s.", next, scan.Level, scan.Deciding)
	}

	return fmt.Sprintf("Release %s (%s).", next, scan.Level)
}

// releaseSet is the repos a release tags, at the shas the revision pinned:
// the first releasing stage's releaseRepos, or every pipeline repo when it
// names none. The release call enforces the same filter.
func releaseSet(p config.Pipeline, revision citypes.Revision) map[string]string {
	for _, stage := range p.Stages {
		if stage.Release == "" {
			continue
		}

		if len(stage.ReleaseRepos) == 0 {
			break
		}

		set := map[string]string{}

		for _, name := range stage.ReleaseRepos {
			if sha, ok := revision.Repos[name]; ok {
				set[name] = sha
			}
		}

		return set
	}

	set := make(map[string]string, len(revision.Repos))
	for name, sha := range revision.Repos {
		set[name] = sha
	}

	return set
}

func (c *Controller) treeHashes(
	ctx context.Context,
	root string,
	names []string,
	ignore []string,
) (map[string]string, error) {
	out := make(map[string]string, len(names))

	for _, name := range names {
		hash, err := c.git.TreeHash(ctx, filepath.Join(root, name), ignore...)
		if err != nil {
			return nil, fmt.Errorf("hashing the tree of %q: %w", name, err)
		}

		out[name] = hash
	}

	return out, nil
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v := range a {
		if b[k] != v {
			return false
		}
	}

	return true
}

func (c *Controller) readRelease(ctx context.Context, index engineIndex, key string) (*releaseRecord, error) {
	var out citypes.StateGetOutput

	if err := c.callState(ctx, index, ToolGet, citypes.StateGetInput{
		Kind: KindRelease, Key: key, Spec: index.stateSpec,
	}, &out); err != nil {
		return nil, err
	}

	if !out.Found {
		return nil, nil
	}

	var rec releaseRecord
	if err := json.Unmarshal([]byte(out.Payload), &rec); err != nil {
		return nil, fmt.Errorf("reading release %q: %w", key, err)
	}

	return &rec, nil
}

// writeRelease records what a revision was released as, once by revision
// so a rerun reuses the number, and once by tag so the next revision can
// ask what the last release carried.
func (c *Controller) writeRelease(ctx context.Context, index engineIndex, rec releaseRecord) error {
	if err := c.putJSON(ctx, index, KindRelease, rec.Revision, rec); err != nil {
		return err
	}

	if rec.Tag == "" {
		return nil
	}

	return c.putJSON(ctx, index, KindRelease, "by-tag/"+rec.Tag, rec)
}

func (c *Controller) readEvaluation(ctx context.Context, index engineIndex, revision string) (releaseDecision, error) {
	var out citypes.StateGetOutput

	if err := c.callState(ctx, index, ToolGet, citypes.StateGetInput{
		Kind: KindOwned, Key: evaluationKeyPrefix + revision, Spec: index.stateSpec,
	}, &out); err != nil {
		return releaseDecision{}, err
	}

	if !out.Found {
		return releaseDecision{}, fmt.Errorf("%w: %s", ErrNoEvaluation, revision)
	}

	var d releaseDecision
	if err := json.Unmarshal([]byte(out.Payload), &d); err != nil {
		return releaseDecision{}, fmt.Errorf("reading the evaluation for %s: %w", revision, err)
	}

	return d, nil
}

func (c *Controller) writeEvaluation(ctx context.Context, index engineIndex, revision string, d releaseDecision) error {
	return c.putJSON(ctx, index, KindOwned, evaluationKeyPrefix+revision, d)
}

// putArtifacts hands what a run built to the engine that built it, and
// answers the records with the locations the engine will serve them from.
// A run that built nothing is left alone.
func (c *Controller) putArtifacts(
	ctx context.Context,
	engine config.Engine,
	revision string,
	root string,
	result *citypes.ForgeResult,
) error {
	if result == nil || len(result.Artifacts) == 0 {
		return nil
	}

	var out citypes.ArtifactPutOutput

	err := c.caller.Call(ctx, engine.Engine, ToolPut, citypes.ArtifactPutInput{
		Revision: revision, Artifacts: result.Artifacts, Root: root, Spec: orEmpty(engine.Spec),
	}, &out)
	if err != nil {
		return err
	}

	result.Artifacts = out.Artifacts

	return nil
}

// restoreArtifacts brings every run's artifacts back through the engine
// that put them, so the release reads files on this machine whether or not
// this machine built them.
func (c *Controller) restoreArtifacts(
	ctx context.Context,
	index engineIndex,
	revision string,
	root string,
	stages []StageReport,
) ([]forge.Artifact, error) {
	out := []forge.Artifact{}

	for _, stage := range stages {
		for _, run := range stage.Runs {
			if run.Forge == nil || len(run.Forge.Artifacts) == 0 {
				continue
			}

			engine, err := index.require(run.Engine, config.PortCompute)
			if err != nil {
				return nil, fmt.Errorf("restoring what %s/%s built: %w", stage.Name, run.Substage, err)
			}

			var got citypes.ArtifactGetOutput

			err = c.caller.Call(ctx, engine.Engine, ToolGet, citypes.ArtifactGetInput{
				Revision: revision, Artifacts: run.Forge.Artifacts, Root: root, Spec: orEmpty(engine.Spec),
			}, &got)
			if err != nil {
				return nil, fmt.Errorf("restoring what %s/%s built: %w", stage.Name, run.Substage, err)
			}

			out = append(out, got.Artifacts...)
		}
	}

	return out, nil
}

// sameBytes answers whether what this run built is byte for byte what the
// previous release shipped: every asset the plan would upload, by name and
// digest, and no asset more or fewer. It speaks only when there is at least
// one asset; a pipeline that tags libraries and ships no file decides by
// the set and the subjects alone.
func sameBytes(root string, uploads []string, previous *releaseRecord) (bool, error) {
	if previous == nil || previous.Index == "" || len(uploads) == 0 {
		return false, nil
	}

	var index artifactcontroller.Index
	if err := json.Unmarshal([]byte(previous.Index), &index); err != nil {
		return false, fmt.Errorf("reading the index %s shipped: %w", previous.Tag, err)
	}

	shipped := map[string]string{}

	for _, tool := range index.Tools {
		for _, platform := range tool.Platforms {
			shipped[platform.Asset] = strings.TrimPrefix(platform.Digest, "sha256:")
		}
	}

	if len(shipped) != len(uploads) {
		return false, nil
	}

	for _, upload := range uploads {
		path := upload
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}

		digest, _, err := fsadapter.Digest(path)
		if err != nil {
			return false, err
		}

		if shipped[filepath.Base(path)] != digest {
			return false, nil
		}
	}

	return true, nil
}
