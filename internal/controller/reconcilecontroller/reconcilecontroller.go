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

	maxOutput = 16384
)

var ErrEngine = errors.New("engine is not declared")

type StageReport struct {
	Name    string        `json:"name"`
	Runs    []citypes.Run `json:"runs"`
	Advance bool          `json:"advance"`
	Reason  string        `json:"reason"`

	// Reused counts the runs answered from the recorded state instead of
	// being executed: the revision already carried a green record for the
	// substage, so nothing ran.
	Reused int `json:"reused,omitempty"`

	// Released is what the substages of this stage that publish answered.
	Released []citypes.ArtifactOutput `json:"released,omitempty"`
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

	// Released is where each release landed, one per substage that published.
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

	// NothingNew says every run this apply reports was reused from the
	// recorded state: the revision had already run green, so nothing
	// executed. A serialized duplicate - two dispatches from one push wave,
	// the second starting after the first finished - lands here, and the
	// report says so instead of reading like a build that did work.
	NothingNew bool `json:"nothingNew,omitempty"`

	// Skipped says the release decision found nothing to release for this
	// revision, and the run ended before any stage: the revision was
	// already released, every commit since the last tag is one the
	// vocabulary ignores, or the release set did not move. Reason says
	// which. Not a failure: the next real change runs everything.
	Skipped bool   `json:"skipped,omitempty"`
	Reason  string `json:"reason,omitempty"`

	// Evaluation is the one word the evaluate phase answers, skip or
	// proceed, which a rendered workflow reads to decide whether the phases
	// after it run at all. Empty outside that phase.
	Evaluation string `json:"evaluation,omitempty"`
}

// The words the evaluate phase answers.
const (
	EvaluationSkip    = "skip"
	EvaluationProceed = "proceed"
)

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

	// Phase is which part of the apply to run: empty for the whole thing,
	// or one of self-reconcile, evaluate, stages. Each phase reads
	// and writes state, so a phase can run in a process of its own and the
	// next phase carries on from the record. That is what lets a compute
	// engine render one apply as several jobs, each visible on its own.
	Phase string

	// Stage narrows the stages phase to one stage by name, so a compute
	// engine can render one job per stage. It refuses to run before the
	// stage in front of it advanced, which it asks that stage's promotion
	// over the run records its substages left. Substage narrows it further
	// to one substage and its gates, for one job per substage.
	Stage    string
	Substage string

	// Revision is the revision this phase is bound to: the one the evaluate
	// phase decided for, handed on by whatever rendered the jobs. A run has
	// exactly one revision, fixed before its first stage, and a phase in a
	// process of its own has no other way to learn it - its own checkout
	// answers whatever the repos hold NOW, which is a different question.
	//
	// A stage that publishes into a repo this pipeline writes moves that
	// repo mid-run, so without this the next job resolves another revision
	// and finds none of the records the run wrote under the first one.
	Revision string
}

// The phases of an apply. PhaseAll is the whole loop in one process,
// which is what every apply was before phases existed.
//
// There is no release phase. A release is a substage of a stage, so it runs
// where the pipeline says it runs, under PhaseStages like everything else.
// Hoisting it into a phase of its own was what made a whole apply and a
// phased apply fire the same releases at different points.
const (
	PhaseAll           = ""
	PhaseSelfReconcile = "self-reconcile"
	PhaseEvaluate      = "evaluate"
	PhaseStages        = "stages"
)

// Phases is every phase name a caller may ask for.
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

	// The real filesystem, not an injection point: the only read is the lock
	// manifest at a root the caller already owns, and an absent file is the
	// ordinary case a fixture wants anyway.
	return &Controller{caller: caller, git: git, fs: fsadapter.New(), now: now}
}

func (c *Controller) Apply(
	ctx context.Context, p config.Pipeline, root string, opts Options,
) (Report, error) {
	index := newIndex(p, root)

	phase := opts.Phase

	var actions []string

	// A phase after the reconcile trusts the reconcile that ran before it,
	// in the process that ran it: the phases are one apply cut into jobs,
	// and the first job is the one that converged the surface.
	if phase == PhaseAll || phase == PhaseSelfReconcile {
		return c.applyFrom(ctx, p, index, root, opts, phase)
	}

	return c.applyPhases(ctx, p, index, root, opts, actions)
}

// applyFrom is an apply that starts at the reconcile: a whole run, or the
// reconcile phase alone.
func (c *Controller) applyFrom(
	ctx context.Context,
	p config.Pipeline,
	index engineIndex,
	root string,
	opts Options,
	phase string,
) (Report, error) {
	// false: an apply converges what can be converged and leaves credentials
	// alone. Only a bootstrap is responsible for those.
	actions, changed, published, err := c.reconcileResources(ctx, p, index, root, false, opts)
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
	//
	// Superseding is a promise that the superseding run exists, so it is
	// allowed only when the settle PUBLISHED the changes - a push is what
	// re-fires the pipeline. A change nobody published cannot re-trigger
	// anything: stopping for one strands the pipeline forever, which is
	// exactly what an empty state directory did to a live pipeline for two
	// runs straight. Such a change is already durable on this machine, so
	// the run continues and measures the converged state; if it dirtied a
	// member tree the -dirty refusal at release stays the honest guard.
	if changed && published {
		return Report{Actions: actions, Superseded: true}, nil
	}

	if changed {
		actions = append(actions,
			"reconcile changed resources nothing publishes, so no push will re-fire this pipeline; continuing on the converged state")
	}

	if phase == PhaseSelfReconcile {
		return Report{Actions: actions}, nil
	}

	return c.applyPhases(ctx, p, index, root, opts, actions)
}

// applyPhases is everything after the reconcile: the revision, the release
// decision, the stages and the release, from the phase asked for onward.
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

	// The revision is resolved here because a run record is keyed by it. It is
	// not written yet. A revision in state is a claim that this tuple of
	// commits was proven, and minting it before anything runs hands a broken
	// build a revision that can propagate.
	//
	// Unless this phase was handed the one its run is bound to, in which case
	// it reads it rather than deriving it: see pinned.go.
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

	// The release decision is made ONCE, before any stage runs, and carried
	// from here. The build stamp and the release tag are then the same
	// number by construction rather than by two computations agreeing: a
	// binary that reports a different version from the release it shipped
	// in is a lie the operator acts on. And a revision with nothing to
	// release ends here, before anything builds: a docs commit is proven by
	// the run that follows a real one.
	var decision releaseDecision

	if phase == PhaseAll || phase == PhaseEvaluate {
		decision, err = c.decideRelease(ctx, p, index, root, revision)
		if err != nil {
			return Report{}, err
		}

		// A phased apply hands the decision to the phases that follow in
		// other processes through state, and the revision travels with it:
		// a later phase resolving its own would answer for whatever the
		// repos hold by then. A whole apply keeps both in hand.
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

	// The revision is minted here, before the first stage, and nowhere else.
	//
	// It is the run's identity, not its verdict. Every job of a phased run
	// answers to it, the artifacts a stage keeps are filed under it, and a
	// consumer that resolves a module by revision gets an alias that is fixed
	// from the moment the pipeline decided to build. Waiting for green made
	// the identity depend on the outcome, which is backwards: whether these
	// commits were PROVEN is answered by the run records and by the release,
	// each of which says so in its own words. Nothing reads a revision record
	// as proof.
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

	// One stage, or one substage, or one stage's promotion: a job of its
	// own, carried on from the records the jobs before it wrote.
	if opts.Stage != "" {
		return c.applyNamedStage(ctx, p, index, root, revision, decision, opts, report)
	}

	for _, stage := range p.Stages {
		// Everything a stage of this apply built is still on this disk, where
		// it built it, so only a stage that PUBLISHES needs the records
		// brought back: an artifact engine is handed locations, and put
		// rewrote them to ones only it can serve.
		var carried []forge.Artifact

		if publishes(index, stage) {
			carried, err = c.carryForward(ctx, index, revision, root, stagesBefore(p, stage.Name))
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

	// Every run answered from the recorded state means this apply proved
	// nothing new: the revision had already run green. The serialized
	// duplicate of a push wave lands here, and the report says so rather
	// than reading like a build that did work.
	total, reused := 0, 0
	for _, s := range report.Stages {
		total += len(s.Runs)
		reused += s.Reused
	}

	report.NothingNew = total > 0 && reused == total

	return report, nil
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
	engine, ok := firstArtifactEngine(p, index)
	if !ok {
		return "", false
	}

	if repo, _ := engine.Spec["repo"].(string); repo != "" {
		return filepath.Join(root, path.Base(repo)), true
	}

	return root, true
}

// firstArtifactEngine is the engine of the first substage that publishes, in
// stage then substage order. A pipeline that declares several releases - one
// for the assets, one for the image - shares one version line, and the first
// is where the pipeline's own words about it live: the repo the tags are read
// from, and the members it leaves alone.
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

// commitScan is what the semantic strategy read: how far the release moves,
// how many commits it read, the commit that decided the level, and one it
// ignored - the words the decision is explained with.
type commitScan struct {
	Level    artifactcontroller.Level
	Count    int
	Deciding string
	Ignored  string
	Semantic bool
}

// bumpLevel is how far the release moves. The semantic strategy reads the
// release set's commit subjects since the last release, because a factory
// releases its members together and a breaking change in any one of them is
// breaking for the number they all carry. Only the release set is read: a
// pipeline repo the release never tags, a register that gains a commit every
// run say, must not decide the number.
//
// A subject no list claims, under unmatched: error, fails here, before any
// build: the vocabulary is a rule the team wrote, and a subject outside it
// is a mistake to report rather than a patch to release quietly. Only the
// newest commit of each repo is held to it. A bad message is fixed by
// pushing a good commit on top, never by rewriting history, so an older
// unmatched commit scores patch and is not reported: it will never change,
// and a rule that blocked on it would block forever.
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

		// The subjects come newest first. Under unmatched: error the newest
		// one is the rule; the ones behind it are scored as if unmatched
		// meant patch, because a commit nobody can change must not decide
		// that nothing ever releases again.
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

// knownKinds is every prefix the vocabulary and the self reconcile commit
// use, the list a refused subject is told to pick from.
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

// mint records the revision as proven. Writing it twice is harmless, because
// the id is derived from the tuple and the record is the same.
func (c *Controller) mint(ctx context.Context, index engineIndex, revision citypes.Revision) error {
	return c.putJSON(ctx, index, KindRevision, revision.ID, toWire(revision))
}

// KindDependencyLock is the record family that pins a revision to the exact
// dependency closure it was proven with. The store learns nothing about it -
// the pipeline opts in by declaring the kind on its state engine.
const KindDependencyLock = "dependency-lock"

// lockManifestPath is where forge-factory's lock records what it resolved.
// The path is the whole contract: this core never learns what a lockfile is,
// only which bytes were locked and what they hashed to.
const lockManifestPath = ".forge/dependency-locks.json"

// generatedManifestPath is where forge-factory records the files it
// generates, root-relative. The path is the whole contract, exactly like
// the lock manifest's: this core never learns what those files are, only
// that their churn is the factory's own doing and stays out of the dirty
// measurement.
const generatedManifestPath = ".forge/factory-generated.json"

type generatedManifest struct {
	Version int      `json:"version"`
	Files   []string `json:"files"`
}

// generatedExclusions maps each repo name to the repo-relative paths the
// factory generates inside it. An absent or unreadable manifest answers
// nothing: a factory that recorded nothing excludes nothing, and the
// measurement is then what it always was. Root-level entries name no repo
// and are dropped - the root is not a repo.
//
// The same reading lives in the trigger controller: a poll and an apply
// that disagree on what counts as a move re-split two decisions one
// commit already unified.
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

// dependencyLockRecord is one stored lockfile: the revision it belongs to,
// the file it came from, the hash that lets a reader verify the content, and
// the content itself. The payload is a string - a byte array marshals to
// base64 and the generated MCP schema refuses it.
type dependencyLockRecord struct {
	Revision string `json:"revision"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Lockfile string `json:"lockfile"`
}

// recordDependencyLocks stores the closure a minted revision was proven
// with. Nothing happens unless the pipeline opted in - the state engine
// declares the kind - and nothing happens when the workspace's lock never
// ran: an absent manifest is the ordinary single-repo case.
//
// A manifest hash that disagrees with the file refuses the record: the tree
// moved between the lock and the mint, and recording either version would
// pin the revision to bytes it was not proven with.
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

// stateDeclaresKind answers whether the state engine's spec names a kind in
// spec.kinds. The core reads the list and nothing else: what a kind means
// stays between the pipeline and its store.
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

	// Everything in a wave runs at once; a wave starts once the one before
	// it is done. A substage whose need did not advance is not run, and a
	// failed record stands in its place, because a promotion that never
	// saw it would advance a stage half of which never happened.
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

// substageWaves orders a stage's substages by their needs. With no need
// declared anywhere it is one wave of everything, which is what a stage
// always was: citypes.Waves answers one wave PER name for an edgeless
// graph, because a repo list ran one at a time before dependencies
// existed, and a stage never did.
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

// heldBy names the first need of this substage that did not advance, or
// nothing when every need did.
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
		// A passed record is reused only when what it built is at hand.
		// On the machine that ran it, it is. On a fresh runner it never is:
		// a run of the same revision inherits the record without the files,
		// and the stage after it fails on a file nothing carried. Golden run
		// 50 died there, on a 0-second build and an empty tarball.
		atHand, err := c.artifactsAtHand(ctx, index, sub, revision.ID, root, existing)
		if err != nil {
			return citypes.Run{}, false, citypes.ArtifactOutput{}, err
		}

		if atHand {
			return *existing, true, citypes.ArtifactOutput{}, nil
		}
	}

	// A substage either runs targets or publishes what the stages before it
	// built. Both are ordinary substages: the same reuse record, the same
	// gates, the same promotion. Only the tool differs.
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

	// What a green run built is handed to the engine that built it before
	// the run is recorded, so the record carries locations the engine can
	// serve again rather than paths on a disk that is gone with the runner.
	if out.Status == citypes.StatusPassed {
		if err := c.putArtifacts(ctx, engine, revision.ID, root, out.Forge); err != nil {
			return citypes.Run{}, false, citypes.ArtifactOutput{}, fmt.Errorf("stage %q substage %q: keeping what it built: %w", stage.Name, sub.Name, err)
		}

		// The closure this run was proven with, recorded by the job that
		// holds it. This used to hang off the mint, and the mint has moved to
		// the evaluate phase - which is the wrong place for it: a revision is
		// an identity, decided before anything is fetched, and the lock
		// manifest exists only on the machine that resolved the closure.
		//
		// So a substage records whatever manifest the root holds when it
		// finishes. A machine that never locked has none and this is a no-op;
		// a machine that did writes the same bytes under the same key however
		// many substages it runs.
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
		Output:    tail(out.Output, maxOutput),
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
) ([]string, bool, bool, error) {
	owned, err := c.readOwnership(ctx, index)
	if err != nil {
		return nil, false, false, err
	}

	byManager := map[string][]citypes.Resource{}

	for _, engine := range p.Engines {
		var declared citypes.DeclareOutput

		// The root rides along so an engine whose spec names workspace
		// files - a resolved pin a sync generated - can read them at
		// declare time, and the stage names so an engine that renders the
		// run as jobs knows them. Both optional on the wire: an engine that
		// reads nothing validates without them.
		if err := c.caller.Call(ctx, engine.Engine, ToolDeclare,
			citypes.DeclareInput{Spec: orEmpty(engine.Spec), Root: root, Stages: declaredStages(p)}, &declared); err != nil {
			return nil, false, false, fmt.Errorf("asking engine %q what it needs: %w", engine.Alias, err)
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
	published := false
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
			return nil, false, false, err
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
			// The commit a settle writes starts with the pipeline's prefix,
			// so the release decision recognizes the commit it caused.
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

	// The ownership record is a write like any other, and a plan performs
	// none. Writing it would also commit and push the state repo, which is
	// the loudest thing a dry run could possibly do.
	if !opts.DryRun {
		if err := c.writeOwnership(ctx, index, merged); err != nil {
			return nil, false, false, err
		}
	}

	return actions, changed, published, nil
}

// declaredStages is the pipeline's stages with their substages, the shape an
// engine that renders jobs is handed. The display names travel with them, so
// what a person reads is decided by the factory rather than by the engine.
func declaredStages(p config.Pipeline) []citypes.DeclaredStage {
	out := make([]citypes.DeclaredStage, 0, len(p.Stages))

	for _, stage := range p.Stages {
		subs := make([]citypes.DeclaredSubstage, 0, len(stage.Substages))
		for _, sub := range stage.Substages {
			subs = append(subs, citypes.DeclaredSubstage{Name: sub.Name, DisplayName: sub.DisplayName})
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

	// The factory's generated files are excluded from the dirty
	// measurement: the register moves them by design, so their churn is
	// the system working rather than drift. The list comes from the
	// factory itself - only a language engine knows those names - and an
	// absent manifest excludes nothing.
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
