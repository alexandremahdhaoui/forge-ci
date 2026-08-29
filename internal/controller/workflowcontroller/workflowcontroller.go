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
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

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
	// Secrets are the Actions secrets the workflows read.
	Secrets []SecretSpec `json:"secrets,omitempty"`
	// Workflows are the files this engine owns under .github/workflows.
	Workflows []WorkflowSpec `json:"workflows,omitempty"`
	// Runner configures the run tool's dispatch target. Empty name means
	// the engine declares no runner and cannot run substages.
	Runner RunnerSpec `json:"runner,omitzero"`
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
	// targets need.
	ToolchainScript string `json:"toolchainScript"`
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
	Cron       string   `json:"cron,omitempty"`
	Events     []string `json:"events,omitempty"`
	Job        string   `json:"job,omitempty"`
	StepName   string   `json:"stepName,omitempty"`
	Secret     string   `json:"secret,omitempty"`
	PayloadEnv []string `json:"payloadEnv,omitempty"`
	Command    string   `json:"command,omitempty"`
	Push       bool     `json:"push,omitempty"`

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

	var s Spec
	if err := json.Unmarshal(payload, &s); err != nil {
		return Spec{}, fmt.Errorf("parsing the spec: %w", err)
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

			if w.Cron == "" && len(w.Events) == 0 {
				return Spec{}, fmt.Errorf(
					"workflow %q: a command workflow needs a cron or events; an empty on-block is a workflow nothing can start", w.Name)
			}

			needsWorkspace = true
		case KindFanOut:
			if w.EventType == "" || len(w.Consumers) == 0 || w.Secret == "" {
				return Spec{}, fmt.Errorf("workflow %q: a fan-out workflow needs eventType, consumers and secret", w.Name)
			}
		case KindRelease:
		default:
			return Spec{}, fmt.Errorf("workflow %q: unknown kind %q, want %s, %s or %s (or verbatim content)",
				w.Name, w.Kind, KindCommand, KindFanOut, KindRelease)
		}
	}

	if needsWorkspace || s.Runner.Name != "" {
		ws := s.Workspace
		if ws.BootstrapCommand == "" || ws.ToolchainScript == "" {
			return Spec{}, errors.New(
				"workspace.bootstrapCommand and toolchainScript are required by command workflows and the runner")
		}
	}

	if s.Runner.PollIntervalSeconds <= 0 {
		s.Runner.PollIntervalSeconds = 15
	}

	if s.Runner.TimeoutMinutes <= 0 {
		s.Runner.TimeoutMinutes = 30
	}

	return s, nil
}

// Controller answers declare and run for the github compute engine.
type Controller struct {
	api   func(spec Spec) githubadapter.API
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

	return &Controller{api: api, now: now, sleep: sleep}
}

// Declare answers every GitHub resource the spec implies: one converged
// file per owned workflow (runner included), one Actions secret per
// declared secret, and enablement for every owned workflow file.
func (c *Controller) Declare(raw map[string]any) (citypes.DeclareOutput, error) {
	spec, err := ParseSpec(raw)
	if err != nil {
		return citypes.DeclareOutput{}, err
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
			Kind: managercontroller.KindActionsSecret,
			Name: spec.Repo + "/" + s.Name,
			Spec: map[string]any{"repo": spec.Repo, "secret": s.Name, "fromEnv": s.FromEnv},
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

	for _, target := range in.Targets {
		binary, expanded, err := computecontroller.CommandFor(target, in.Params)
		if err != nil {
			return "", err
		}

		argv := []string{quote(binary)}
		for _, arg := range strings.Fields(expanded) {
			argv = append(argv, quote(arg))
		}

		dirs := target.In
		if len(dirs) == 0 {
			dirs = []string{"."}
		}

		for _, dir := range dirs {
			fmt.Fprintf(&b, "(cd %s && %s)\n", quote(dir), strings.Join(argv, " "))
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
