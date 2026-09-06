// Package workflowcontroller is the github compute engine's logic: it
// declares everything the pipeline needs on the GitHub side - workflow
// files templated from its spec, Actions secrets, workflow enablement -
// and runs a substage's targets remotely through a dispatched runner
// workflow.
//
// Nothing here names a project: every instance-specific value arrives in
// the spec.
package workflowcontroller

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/computecontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

// Spec is the engine's configuration: which repo, which workflows, which
// secrets, and how the runner behaves. It arrives as the free-form spec
// map and is parsed strictly enough that a typo fails loud.
type Spec struct {
	// Repo is the owner/name the workflows, secrets and runs live on.
	Repo string `json:"repo"`
	// Dir is the repo's checkout directory relative to the pipeline
	// root. Defaults to the repo basename.
	Dir string `json:"dir,omitempty"`
	// APIBaseURL overrides the GitHub API base; tests point it at a fake.
	APIBaseURL string `json:"apiBaseURL,omitempty"`
	// TokenEnv names the environment variable holding the API token for
	// the run tool. Defaults to GITHUB_TOKEN.
	TokenEnv string `json:"tokenEnv,omitempty"`
	// Ref is the branch dispatches target and pushes land on. Defaults
	// to main.
	Ref string `json:"ref,omitempty"`
	// Setup steps run first in every command workflow and in the runner,
	// as `uses:` actions. Whatever toolchain the targets need is the
	// instance's to name; forge-ci names none.
	Setup []SetupStep `json:"setup,omitempty"`
	// Workspace parameterizes the checkout-and-toolchain preamble shared
	// by every command workflow.
	Workspace Workspace `json:"workspace,omitzero"`
	// Container is the image every job that builds a workspace runs inside,
	// so the tools arrive prebuilt instead of being installed per run. It
	// replaces the toolchain install and NOT the workspace bootstrap: a
	// container supplies tools, the members still have to be cloned.
	//
	// Absent keeps the runner's own machine. That is what the factory
	// publishing the image needs: a pipeline that ran inside the image it
	// builds could not publish a fixed one after a bad release.
	Container ContainerSpec `json:"container,omitzero"`
	// RunsOn is the runner label every rendered job runs on. Absent means
	// ubuntu-latest, which is what every workflow rendered before the key
	// existed.
	RunsOn string `json:"runsOn,omitempty"`
	// Concurrency is the group every command workflow serializes on. Absent
	// means the platform's own name for the workflow, queued rather than
	// cancelled: an apply that is writing state must finish, and the next
	// one then sees what it wrote.
	Concurrency ConcurrencySpec `json:"concurrency,omitzero"`
	// Caches are what every workspace-building job restores before its
	// bootstrap and saves afterwards. Absent means one cache, the tool
	// store under ~/.cache/forge keyed by its content, which is what every
	// job rendered before the key existed; an empty list means no cache at
	// all. A cache keyed by repos is restored after the checkout and saved
	// after the toolchain script, under the heads of the pipeline's repos.
	Caches *[]CacheSpec `json:"caches,omitempty"`
	// Carry is how what a job built crosses to the jobs after it.
	Carry CarrySpec `json:"carry,omitzero"`
	// FailureReport is the issue a failing run of a workflow that asked for
	// one opens: its title, with {{.Workflow}} standing for the workflow's
	// name, and its body.
	FailureReport FailureReportSpec `json:"failureReport,omitzero"`
	// Actions pins the platform actions the rendered steps use. Absent means
	// the versions every workflow rendered before the key existed.
	Actions ActionsSpec `json:"actions,omitzero"`
	// Secrets are the Actions secrets the workflows read.
	Secrets []SecretSpec `json:"secrets,omitempty"`
	// Workflows are the files this engine owns under .github/workflows.
	Workflows []WorkflowSpec `json:"workflows,omitempty"`
	// Runner configures the run tool's dispatch target. Empty name means
	// the engine declares no runner and cannot run substages.
	Runner RunnerSpec `json:"runner,omitzero"`
	// Jobs is how a command workflow is cut: "whole" renders one job running
	// the whole apply, which is what every pipeline was before phases
	// existed; "stage" renders one job per phase and per pipeline stage, its
	// substages running beside each other inside it; "substage" renders one
	// job per substage, so a failure shows where it happened, at the cost of
	// one workspace checkout per job. Absent means whole. Cut into phases,
	// the run reads as what it is - self-reconcile, evaluate, the stages -
	// and a skipped evaluation shows the jobs after it as skipped, which is
	// GitHub's own word for it; the files a build produces cross between
	// the jobs as Actions artifacts.
	Jobs string `json:"jobs,omitempty"`

	// image is the container reference the jobs run in, once resolved:
	// container.ref verbatim, or the contents of container.file read at
	// declare time. Empty is the runner's own machine.
	image string

	// Stages are the pipeline's stages by name, handed in at declare time
	// and never read from the spec: the pipeline declares them once, and
	// the rendered jobs follow.
	Stages []citypes.DeclaredStage `json:"-"`
	// Repos is the pipeline's repositories by name, handed in by the core
	// beside the stages, for a cache keyed on what the run is built from.
	Repos []citypes.DeclaredRepo `json:"-"`
}

// Workspace is the checkout preamble: one command that stands the whole
// workspace up from nothing, and one that installs the toolchain into it.
// Both are the instance's own words - forge-ci names no toolchain.
type Workspace struct {
	// BootstrapCommand runs in the runner's working directory and leaves a
	// complete workspace there. It names its own factory, so it is one
	// command and not a recipe: forge-ci decides no argument, no path and
	// no order, which is what a recipe here got wrong three times.
	BootstrapCommand string `json:"bootstrapCommand"`
	// ToolchainScript is the verbatim run block installing whatever the
	// targets need. What it installs is cached by a caches entry keyed by
	// repos, restored before the script and saved after it.
	ToolchainScript string `json:"toolchainScript,omitempty"`
}

// ContainerSpec names the image the jobs run in: a reference typed here, or
// a file under the pipeline root holding one - the file a workspace sync
// generates from its resolved toolchain pin, so the resolver owns the
// version and this engine only reads it. Exactly one of the two.
type ContainerSpec struct {
	Ref  string `json:"ref,omitempty"`
	File string `json:"file,omitempty"`
}

// ConcurrencySpec is the group a command workflow's runs serialize on.
type ConcurrencySpec struct {
	Group            string `json:"group,omitempty"`
	CancelInProgress bool   `json:"cancelInProgress,omitempty"`
}

// CacheSpec is one cache a workspace-building job restores and saves: the
// paths it holds, and how its key is derived. "content" names the saved
// store by a listing of what it holds and restores the newest for this OS,
// which is right for a store that converges on its own; "repos" hashes the
// heads of the pipeline's repos, exact match only, which is right for a
// toolchain built from the checkout.
type CacheSpec struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
	Key   string   `json:"key,omitempty"`
}

// CarrySpec is how a job's built files cross to the jobs after it: the
// compression of the tarball, and how long the platform keeps it.
type CarrySpec struct {
	Compression string `json:"compression,omitempty"`
	Retention   int    `json:"retention,omitempty"`
}

// FailureReportSpec is the issue a failing run opens. {{.Workflow}} in the
// title stands for the workflow's name; the body is verbatim.
type FailureReportSpec struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

// ActionsSpec pins the platform actions the rendered steps use.
type ActionsSpec struct {
	Cache            string `json:"cache,omitempty"`
	DownloadArtifact string `json:"downloadArtifact,omitempty"`
	UploadArtifact   string `json:"uploadArtifact,omitempty"`
}

// OnSpec is what starts a command workflow: a cron, dispatch events, and
// pushes to the repo it lives on.
type OnSpec struct {
	Cron   string   `json:"cron,omitempty"`
	Events []string `json:"events,omitempty"`
	// Push also starts this workflow when the repo it lives on is pushed. A
	// factory's own pipeline wants it: nothing dispatches when the
	// workspace files themselves change, so without it an edit to the
	// pipeline's own config never runs it. IgnorePaths keeps a push that
	// touched only those paths from starting the workflow at all, GitHub's
	// own paths-ignore: it filters pushes to THIS repo only and is blind to
	// what a file is for, so list only what nothing embeds.
	Push *PushSpec `json:"push,omitempty"`
}

// PushSpec is the push half of an on-block.
type PushSpec struct {
	Branches    []string `json:"branches"`
	IgnorePaths []string `json:"ignorePaths,omitempty"`
}

// The keys of a CacheSpec, the cuts of a workflow, and the compressions of
// a carry.
const (
	CacheKeyContent = "content"
	CacheKeyRepos   = "repos"

	JobsWhole = "whole"

	CompressionGzip = "gzip"
	CompressionNone = "none"
)

// The defaults, each equal to what every workflow rendered before its key
// existed, so a spec that says nothing renders byte for byte what it did.
const (
	defaultRunsOn           = "ubuntu-latest"
	defaultConcurrencyGroup = "${{ github.workflow }}"
	defaultCarryRetention   = 7
	defaultFailureTitle     = "{{.Workflow}} is failing"
	defaultFailureBody      = "$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID failed. " +
		"Nothing else reports this run, so it reports itself. Close this once a run goes green; " +
		"a failure after that files a new one."
	defaultActionCache    = "actions/cache@v6"
	defaultActionDownload = "actions/download-artifact@v8"
	defaultActionUpload   = "actions/upload-artifact@v7"
)

// DefaultCaches is the one cache every job carried before caches existed:
// the tool store, named by its content.
func DefaultCaches() []CacheSpec {
	return []CacheSpec{{Name: "tool-store", Paths: []string{"~/.cache/forge"}, Key: CacheKeyContent}}
}

// SetupStep is one `uses:` action at the top of a generated workflow.
type SetupStep struct {
	Uses string            `json:"uses"`
	With map[string]string `json:"with,omitempty"`
}

// SecretSpec is one Actions secret and the environment variable its
// value is read from at realize time.
type SecretSpec struct {
	Name    string `json:"name"`
	FromEnv string `json:"fromEnv,omitempty"`
}

// WorkflowSpec is one owned workflow. Content wins verbatim when set;
// otherwise the kind's template renders from the other fields.
type WorkflowSpec struct {
	Name    string `json:"name"`
	Kind    string `json:"kind,omitempty"`
	Content string `json:"content,omitempty"`
	// Header is a verbatim leading comment block, newline-terminated.
	Header string `json:"header,omitempty"`

	// command kind.
	On OnSpec `json:"on,omitzero"`
	Job        string   `json:"job,omitempty"`
	StepName   string   `json:"stepName,omitempty"`
	Secret     string   `json:"secret,omitempty"`
	PayloadEnv []string `json:"payloadEnv,omitempty"`
	Command    string   `json:"command,omitempty"`
	Push       bool     `json:"push,omitempty"`

	// Token puts secrets.GITHUB_TOKEN into the step's environment. Nothing
	// else does: engines inherit forge-ci's environment and nothing else,
	// and the secrets a bootstrap seals into Actions are sealed on the
	// operator's laptop and put nothing into a running job. A pipeline whose
	// release stage reaches GitHub or a registry needs this.
	Token bool `json:"token,omitempty"`

	// Packages grants the job permission to write to the container
	// registry. Nothing else grants it, and without it the push fails at the
	// last step of a run that already built everything.
	Packages bool `json:"packages,omitempty"`

	// ReportFailure opens an issue when a run of this workflow fails, and
	// grants the issues: write the step needs.
	//
	// Set it on a workflow nobody is watching. A scheduled run has no
	// audience: nobody typed it and nobody is waiting on it, so a red run is
	// a red icon on a page nobody opens - one instance failed every morning
	// for eight days that way, and the first person to look found it by
	// listing runs on a hunch.
	//
	// A repository_dispatch run has no audience either, and that is the easy
	// one to get wrong. Somebody caused it, so it looks attended, but they
	// caused it from another repo: a member push that fans out to a
	// workspace pipeline is read by a person watching the member's own
	// checks, not the workspace's.
	//
	// A push to this repo and a workflow_dispatch somebody typed both have a
	// person already looking at these checks, so leave it off for those.
	ReportFailure bool `json:"reportFailure,omitempty"`

	// fan-out kind.
	TagPattern string   `json:"tagPattern,omitempty"`
	EventType  string   `json:"eventType,omitempty"`
	Consumers  []string `json:"consumers,omitempty"`
	// APIBaseURL is where the rendered workflow itself sends the
	// dispatch - the workflow runs on GitHub, so this defaults to the
	// public API and is independent of the engine's own client base.
	APIBaseURL string `json:"apiBaseURL,omitempty"`
}

// RunnerSpec configures the dispatched runner workflow the run tool
// correlates against.
type RunnerSpec struct {
	Name   string `json:"name"`
	Secret string `json:"secret,omitempty"`
	// SetupScript is a verbatim block run before the dispatched targets,
	// for whatever the instance's targets expect to exist.
	SetupScript         string `json:"setupScript,omitempty"`
	PollIntervalSeconds int    `json:"pollIntervalSeconds,omitempty"`
	TimeoutMinutes      int    `json:"timeoutMinutes,omitempty"`
}

// Workflow kinds.
const (
	KindCommand = "command"
	KindFanOut  = "fan-out"
	KindRelease = "release"
)

// ParseSpec reads the free-form spec map into a Spec, defaults it, and
// refuses what cannot render.
func ParseSpec(raw map[string]any) (Spec, error) {
	payload, err := json.Marshal(raw)
	if err != nil {
		return Spec{}, fmt.Errorf("encoding the spec: %w", err)
	}

	// A key this spec does not name is refused by name, and a key this spec
	// used to name is refused naming the key that replaced it. A silently
	// dropped key read as configuration that took effect.
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()

	var s Spec
	if err := dec.Decode(&s); err != nil {
		return Spec{}, fmt.Errorf("parsing the spec: %w", foldHint(err))
	}

	if s.Repo == "" {
		return Spec{}, errors.New("spec.repo is required, as owner/name")
	}

	if s.Dir == "" {
		s.Dir = path.Base(s.Repo)
	}

	if s.TokenEnv == "" {
		s.TokenEnv = "GITHUB_TOKEN"
	}

	if s.Ref == "" {
		s.Ref = "main"
	}

	for i, step := range s.Setup {
		if step.Uses == "" {
			return Spec{}, fmt.Errorf("setup[%d] has no uses", i)
		}
	}

	needsWorkspace := false

	for i, w := range s.Workflows {
		if w.Name == "" {
			return Spec{}, fmt.Errorf("workflows[%d] has no name", i)
		}

		if w.Content != "" {
			continue
		}

		switch w.Kind {
		case KindCommand:
			if w.Command == "" || w.Secret == "" {
				return Spec{}, fmt.Errorf("workflow %q: a command workflow needs command and secret", w.Name)
			}

			if w.On.Cron == "" && len(w.On.Events) == 0 && w.On.Push == nil {
				return Spec{}, fmt.Errorf(
					"workflow %q: a command workflow needs on.cron, on.events or on.push; an empty on-block is a workflow nothing can start", w.Name)
			}

			if w.On.Push != nil && len(w.On.Push.Branches) == 0 {
				return Spec{}, fmt.Errorf("workflow %q: on.push needs branches", w.Name)
			}

			needsWorkspace = true
		case KindFanOut:
			if w.EventType == "" || len(w.Consumers) == 0 || w.Secret == "" {
				return Spec{}, fmt.Errorf("workflow %q: a fan-out workflow needs eventType, consumers and secret", w.Name)
			}
		case KindRelease:
			// A release job stands the workspace up like every other, because
			// a toolchain script does `cd <member> && go install` and needs
			// the members on disk. That clone reaches private repos, so it
			// needs the same credential, and a workflow that named none used
			// to render `${{ secrets. }}` - an empty rewrite that fails on
			// the first private member.
			if w.Secret == "" {
				return Spec{}, fmt.Errorf(
					"workflow %q: a release workflow needs secret; it checks the workspace out to reach the toolchain", w.Name)
			}

			needsWorkspace = true
		default:
			return Spec{}, fmt.Errorf("workflow %q: unknown kind %q, want %s, %s or %s (or verbatim content)",
				w.Name, w.Kind, KindCommand, KindFanOut, KindRelease)
		}
	}

	if needsWorkspace || s.Runner.Name != "" {
		ws := s.Workspace

		// The bootstrap is never optional. A container carries tools, not a
		// checked-out workspace, so the members still have to be cloned
		// however the job gets its toolchain.
		if ws.BootstrapCommand == "" {
			return Spec{}, errors.New(
				"workspace.bootstrapCommand is required by command workflows and the runner; " +
					"a container image supplies tools and not a workspace, so the members still need cloning")
		}

		if ws.ToolchainScript == "" && s.Container.Ref == "" && s.Container.File == "" {
			return Spec{}, errors.New(
				"workspace.toolchainScript is required unless spec.container names " +
					"an image the jobs run in, which is what supplies the toolchain instead")
		}
	}

	switch s.Jobs {
	case "":
		s.Jobs = JobsWhole
	case JobsWhole, JobsPerStage, JobsPerSubstage:
	default:
		return Spec{}, fmt.Errorf("spec.jobs %q is not %s, %s or %s", s.Jobs, JobsWhole, JobsPerStage, JobsPerSubstage)
	}

	if s.Container.Ref != "" && s.Container.File != "" {
		return Spec{}, errors.New(
			"exactly one of spec.container.ref or spec.container.file pins the image: " +
				"a literal reference, or the file the workspace sync resolves one into")
	}

	for i, cache := range s.caches() {
		if cache.Name == "" || len(cache.Paths) == 0 {
			return Spec{}, fmt.Errorf("caches[%d] needs a name and paths", i)
		}

		switch cache.Key {
		case "", CacheKeyContent, CacheKeyRepos:
		default:
			return Spec{}, fmt.Errorf("caches[%d] (%s): key %q is not %s or %s",
				i, cache.Name, cache.Key, CacheKeyContent, CacheKeyRepos)
		}
	}

	switch s.Carry.Compression {
	case "", CompressionGzip, CompressionNone:
	default:
		return Spec{}, fmt.Errorf("spec.carry.compression %q is not %s or %s",
			s.Carry.Compression, CompressionGzip, CompressionNone)
	}

	s = s.withDefaults()

	if s.Runner.PollIntervalSeconds <= 0 {
		s.Runner.PollIntervalSeconds = 15
	}

	if s.Runner.TimeoutMinutes <= 0 {
		s.Runner.TimeoutMinutes = 30
	}

	return s, nil
}

// caches is the spec's cache list, or the one every job carried before the
// key existed when the spec says nothing. An empty list is an empty list.
func (s Spec) caches() []CacheSpec {
	if s.Caches == nil {
		return DefaultCaches()
	}

	return *s.Caches
}

// withDefaults fills every key the spec left unsaid with what every
// workflow rendered before that key existed, so a spec that says nothing
// renders byte for byte what it did. The parser applies it, and so does the
// renderer, so a spec built in code renders the same as one parsed.
func (s Spec) withDefaults() Spec {
	if s.image == "" {
		s.image = s.Container.Ref
	}

	if s.Jobs == "" {
		s.Jobs = JobsWhole
	}

	if s.RunsOn == "" {
		s.RunsOn = defaultRunsOn
	}

	if s.Concurrency.Group == "" {
		s.Concurrency.Group = defaultConcurrencyGroup
	}

	caches := s.caches()
	for i := range caches {
		if caches[i].Key == "" {
			caches[i].Key = CacheKeyContent
		}
	}

	s.Caches = &caches

	if s.Carry.Compression == "" {
		s.Carry.Compression = CompressionGzip
	}

	if s.Carry.Retention <= 0 {
		s.Carry.Retention = defaultCarryRetention
	}

	if s.FailureReport.Title == "" {
		s.FailureReport.Title = defaultFailureTitle
	}

	if s.FailureReport.Body == "" {
		s.FailureReport.Body = defaultFailureBody
	}

	if s.Actions.Cache == "" {
		s.Actions.Cache = defaultActionCache
	}

	if s.Actions.DownloadArtifact == "" {
		s.Actions.DownloadArtifact = defaultActionDownload
	}

	if s.Actions.UploadArtifact == "" {
		s.Actions.UploadArtifact = defaultActionUpload
	}

	return s
}

// folds names, for every key this spec used to carry, the key that carries
// it now. A strict parse refuses the old key; this turns the refusal into
// the edit.
var folds = map[string]string{
	"containerFile":       "container: {file: ...}",
	"phases":              "jobs: stage or jobs: substage (jobs: whole is the default)",
	"toolchainCachePaths": "caches: [{name, paths, key: repos}]",
	"cron":                "on: {cron: ...}",
	"events":              "on: {events: [...]}",
	"pushBranches":        "on: {push: {branches: [...]}}",
	"pushPathsIgnore":     "on: {push: {ignorePaths: [...]}}",
}

// foldHint rewrites a strict-parse refusal of a retired key into the fold
// that replaced it. Any other error is answered as it came.
func foldHint(err error) error {
	msg := err.Error()

	// A bare on: is the boolean true to a YAML 1.1 parser, which is what
	// forge's own reads, so the key arrives spelled "true". The fix is one
	// pair of quotes, and the refusal says so rather than naming a key
	// nobody wrote.
	if strings.Contains(msg, `unknown field "true"`) {
		return errors.New(`a bare on: key is read as the boolean true; write it quoted, "on":`)
	}

	for old, now := range folds {
		if strings.Contains(msg, `unknown field "`+old+`"`) {
			return fmt.Errorf("%s is no longer a key; write %s", old, now)
		}
	}

	// container was a string once, and a string cannot fill an object.
	if strings.Contains(msg, "cannot unmarshal string into Go struct field") &&
		strings.Contains(msg, ".container") {
		return errors.New("container is an object now; write container: {ref: ...} or container: {file: ...}")
	}

	return err
}

// Controller answers declare and run for the github compute engine.
type Controller struct {
	api   func(spec Spec) githubadapter.API
	fs    fsadapter.FS
	now   func() time.Time
	sleep func(time.Duration)
}

// New builds the controller. The api factory receives the parsed spec so
// the base URL and token variable come from the pipeline, not from here;
// nil now and sleep mean the real clock.
func New(
	api func(spec Spec) githubadapter.API, now func() time.Time, sleep func(time.Duration),
) *Controller {
	if now == nil {
		now = time.Now
	}

	if sleep == nil {
		sleep = time.Sleep
	}

	return &Controller{api: api, fs: fsadapter.New(), now: now, sleep: sleep}
}

// Declare answers every GitHub resource the spec implies: one converged
// file per owned workflow (runner included), one Actions secret per
// declared secret, and enablement for every owned workflow file. root is
// where the pipeline runs, so a containerFile resolves against the
// workspace the sync wrote it into; stages are the pipeline's stages by
// name, which a phased workflow renders as jobs.
func (c *Controller) Declare(
	raw map[string]any, root string, stages []citypes.DeclaredStage, repos []citypes.DeclaredRepo,
) (citypes.DeclareOutput, error) {
	spec, err := ParseSpec(raw)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	spec.Stages = stages
	spec.Repos = repos

	if spec.Container.File != "" {
		pin, err := c.fs.ReadFile(filepath.Join(root, filepath.FromSlash(spec.Container.File)))
		if err != nil {
			return citypes.DeclareOutput{}, fmt.Errorf(
				"reading spec.container.file %s: %w (the workspace sync writes it; sync first)",
				spec.Container.File, err)
		}

		spec.image = strings.TrimSpace(string(pin))
		if spec.image == "" {
			return citypes.DeclareOutput{}, fmt.Errorf(
				"spec.container.file %s is empty: it must hold one image reference, tag included",
				spec.Container.File)
		}
	}

	resources := []citypes.Resource{}

	files, err := RenderAll(spec)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	for _, f := range files {
		resources = append(resources, citypes.Resource{
			Kind: managercontroller.KindFileContent,
			Name: path.Join(spec.Dir, ".github", "workflows", f.Name+".yaml"),
			Spec: map[string]any{"content": f.Content},
		})
	}

	for _, s := range spec.Secrets {
		if s.Name == "" {
			return citypes.DeclareOutput{}, errors.New("a secrets entry has no name")
		}

		resources = append(resources, citypes.Resource{
			Kind:          managercontroller.KindActionsSecret,
			Name:          spec.Repo + "/" + s.Name,
			BootstrapOnly: true,
			Spec:          map[string]any{"repo": spec.Repo, "secret": s.Name, "fromEnv": s.FromEnv},
		})
	}

	for _, f := range files {
		resources = append(resources, citypes.Resource{
			Kind: managercontroller.KindWorkflowEnabled,
			Name: spec.Repo + "/" + f.Name + ".yaml",
			Spec: map[string]any{"repo": spec.Repo, "workflow": f.Name + ".yaml"},
		})
	}

	return citypes.DeclareOutput{Resources: resources}, nil
}

// Run executes a substage's targets remotely: it dispatches the runner
// workflow with a unique marker and the target script, correlates the
// run by the marker echoed in its name, and polls it to conclusion.
//
// A red conclusion is a run with status failed, never an error: an error
// here means the machinery broke, and getting that wrong makes a red
// build look like a broken runner.
func (c *Controller) Run(ctx context.Context, in citypes.RunInput) (citypes.RunOutput, error) {
	spec, err := ParseSpec(orEmptySpec(in.Spec))
	if err != nil {
		return citypes.RunOutput{}, err
	}

	if spec.Runner.Name == "" {
		return citypes.RunOutput{}, errors.New("running remotely: spec.runner.name is required")
	}

	if len(in.Targets) == 0 {
		return citypes.RunOutput{}, errors.New("running: no targets given")
	}

	if strings.HasSuffix(in.Revision, "-dirty") {
		return citypes.RunOutput{}, fmt.Errorf(
			"revision %s covers uncommitted changes; a remote run cannot execute what was never pushed", in.Revision)
	}

	script, err := scriptFor(in)
	if err != nil {
		return citypes.RunOutput{}, err
	}

	marker, err := markerFor(in)
	if err != nil {
		return citypes.RunOutput{}, err
	}

	api := c.api(spec)
	runnerFile := spec.Runner.Name + ".yaml"
	dispatched := c.now()

	err = api.Dispatch(ctx, spec.Repo, runnerFile, spec.Ref,
		map[string]string{"marker": marker, "script": script})
	if err != nil {
		return citypes.RunOutput{}, err
	}

	run, err := c.await(ctx, api, spec, runnerFile, marker, dispatched)
	if err != nil {
		return citypes.RunOutput{}, err
	}

	out := citypes.RunOutput{
		Status: citypes.StatusPassed,
		Output: fmt.Sprintf("run %d (%s) concluded %s: %s", run.ID, marker, run.Conclusion, run.HTMLURL),
	}

	if run.Conclusion != "success" {
		out.Status = citypes.StatusFailed
		out.Message = fmt.Sprintf("remote run concluded %s: %s", run.Conclusion, run.HTMLURL)
	}

	return out, nil
}

// await correlates the dispatched run by its marker and polls it to
// completion. Not appearing or not finishing inside the timeout is an
// error: the machinery, not the build.
func (c *Controller) await(
	ctx context.Context,
	api githubadapter.API,
	spec Spec,
	runnerFile, marker string,
	dispatched time.Time,
) (githubadapter.RunInfo, error) {
	deadline := dispatched.Add(time.Duration(spec.Runner.TimeoutMinutes) * time.Minute)
	interval := time.Duration(spec.Runner.PollIntervalSeconds) * time.Second

	var id int64

	for {
		if c.now().After(deadline) {
			return githubadapter.RunInfo{}, fmt.Errorf(
				"waiting for run %q on %s: no completed run inside %d minutes",
				marker, spec.Repo, spec.Runner.TimeoutMinutes)
		}

		if id == 0 {
			runs, err := api.ListRuns(ctx, spec.Repo, runnerFile, dispatched.Add(-time.Minute))
			if err != nil {
				return githubadapter.RunInfo{}, err
			}

			for _, r := range runs {
				if strings.Contains(r.DisplayTitle, marker) {
					id = r.ID

					break
				}
			}
		}

		if id != 0 {
			run, err := api.Run(ctx, spec.Repo, id)
			if err != nil {
				return githubadapter.RunInfo{}, err
			}

			if run.Status == "completed" {
				return run, nil
			}
		}

		c.sleep(interval)
	}
}

// scriptFor turns the substage's targets into the shell script the
// runner executes at the workspace root, through the same target-to-
// command mapping the local compute engine uses. Every token is
// single-quoted so the runner shell sees the same argv the local
// compute would exec, and the revision's member shas are pinned first
// so the remote run executes the code the revision names, not whatever
// the branches moved to.
func scriptFor(in citypes.RunInput) (string, error) {
	var b strings.Builder

	b.WriteString("set -eu\n")

	// The revision and the version reach the targets exactly as the local
	// compute engine hands them: the tuple a build was proven with, and the
	// number the release will carry. A remote run that stamped a different
	// version from a local one would make the two builds incomparable.
	fmt.Fprintf(&b, "export FORGE_CI_REVISION=%s\n", quote(in.Revision))

	if in.Version != "" {
		fmt.Fprintf(&b, "export FORGE_CI_VERSION=%s\n", quote(in.Version))
	}

	for _, repo := range in.Repos {
		if repo.SHA == "" || repo.Name == "" {
			continue
		}

		fmt.Fprintf(&b, "git -C %s fetch origin %s\n", quote(repo.Name), quote(repo.SHA))
		fmt.Fprintf(&b, "git -C %s checkout --detach %s\n", quote(repo.Name), quote(repo.SHA))
	}

	// The workspace converges on the runner before anything builds:
	// manifests first, then the dependency closure. The bootstrap synced at
	// checkout, but the pins above may have moved members under it, and the
	// closure is never resolved by a sync at all.
	if in.Sync {
		b.WriteString("forge-factory sync\nforge-factory lock\n")
	}

	for _, target := range in.Targets {
		binary, args, err := computecontroller.CommandFor(target, in.Params)
		if err != nil {
			return "", err
		}

		argv := []string{quote(binary)}
		for _, arg := range args {
			argv = append(argv, quote(arg))
		}

		waves, err := citypes.WavesFor(target, in)
		if err != nil {
			return "", err
		}

		for _, wave := range waves {
			// A wave of one is written as a plain subshell, which is what
			// every target rendered before dependencies existed. A pipeline
			// that declares no needs therefore renders the identical script,
			// byte for byte.
			if len(wave) == 1 {
				fmt.Fprintf(&b, "(cd %s && %s)\n", quote(dirOf(wave[0])), strings.Join(argv, " "))

				continue
			}

			// A real wave backgrounds its members and waits. `set -eu` does
			// not fail the script on a background job, so the exit status of
			// each is collected and the first non-zero one is re-raised after
			// every member has finished - a wave reports every repo that
			// broke, not the one that broke first.
			b.WriteString("wave_rc=0\n")

			for _, name := range wave {
				fmt.Fprintf(&b, "(cd %s && %s) & \n", quote(dirOf(name)), strings.Join(argv, " "))
			}

			b.WriteString("for job in $(jobs -p); do wait \"$job\" || wave_rc=$?; done\n")
			b.WriteString("[ \"$wave_rc\" -eq 0 ] || exit \"$wave_rc\"\n")
		}
	}

	return b.String(), nil
}

// quote single-quotes one shell token, the one quoting the POSIX shell
// cannot reinterpret.
func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// markerFor builds the unique correlation marker the runner echoes in
// its run name. Substages run concurrently, so the nonce matters.
func markerFor(in citypes.RunInput) (string, error) {
	nonce := make([]byte, 4)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("building the run marker: %w", err)
	}

	revision := in.Revision
	if len(revision) > 12 {
		revision = revision[:12]
	}

	return fmt.Sprintf("%s-%s-%s-%s", revision, in.Stage, in.Substage, hex.EncodeToString(nonce)), nil
}

func orEmptySpec(spec map[string]any) map[string]any {
	if spec == nil {
		return map[string]any{}
	}

	return spec
}

// dirOf is the directory one wave member runs in. The empty name is the
// workspace root, which is what a target naming no repo runs in.
func dirOf(name string) string {
	if name == "" {
		return "."
	}

	return name
}
