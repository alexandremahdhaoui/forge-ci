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

type Spec struct {
	Repo          string            `json:"repo"`
	Dir           string            `json:"dir,omitempty"`
	APIBaseURL    string            `json:"apiBaseURL,omitempty"`
	TokenEnv      string            `json:"tokenEnv,omitempty"`
	Ref           string            `json:"ref,omitempty"`
	Setup         []SetupStep       `json:"setup,omitempty"`
	Workspace     Workspace         `json:"workspace,omitzero"`
	Container     ContainerSpec     `json:"container,omitzero"`
	RunsOn        string            `json:"runsOn,omitempty"`
	Concurrency   ConcurrencySpec   `json:"concurrency,omitzero"`
	Caches        *[]CacheSpec      `json:"caches,omitempty"`
	Carry         CarrySpec         `json:"carry,omitzero"`
	FailureReport FailureReportSpec `json:"failureReport,omitzero"`
	Actions       ActionsSpec       `json:"actions,omitzero"`
	Secrets       []SecretSpec      `json:"secrets,omitempty"`
	Workflows     []WorkflowSpec    `json:"workflows,omitempty"`
	Runner        RunnerSpec        `json:"runner,omitzero"`
	Jobs          string            `json:"jobs,omitempty"`

	image string

	Stages []citypes.DeclaredStage `json:"-"`
	Repos  []citypes.DeclaredRepo  `json:"-"`
}

type Workspace struct {
	BootstrapCommand string `json:"bootstrapCommand"`
	ToolchainScript  string `json:"toolchainScript,omitempty"`
}

type ContainerSpec struct {
	Ref  string `json:"ref,omitempty"`
	File string `json:"file,omitempty"`
}

type ConcurrencySpec struct {
	Group            string `json:"group,omitempty"`
	CancelInProgress bool   `json:"cancelInProgress,omitempty"`
}

type CacheSpec struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
	Key   string   `json:"key,omitempty"`
}

type CarrySpec struct {
	Compression string `json:"compression,omitempty"`
	Retention   int    `json:"retention,omitempty"`
}

type FailureReportSpec struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
}

type ActionsSpec struct {
	Cache            string `json:"cache,omitempty"`
	DownloadArtifact string `json:"downloadArtifact,omitempty"`
	UploadArtifact   string `json:"uploadArtifact,omitempty"`
}

type OnSpec struct {
	Cron   string    `json:"cron,omitempty"`
	Events []string  `json:"events,omitempty"`
	Push   *PushSpec `json:"push,omitempty"`
}

type PushSpec struct {
	Branches    []string `json:"branches"`
	IgnorePaths []string `json:"ignorePaths,omitempty"`
}

const (
	CacheKeyContent = "content"
	CacheKeyRepos   = "repos"

	JobsWhole = "whole"

	CompressionGzip = "gzip"
	CompressionNone = "none"
)

const (
	defaultRunsOn           = "ubuntu-latest"
	defaultAPIBaseURL       = "https://api.github.com"
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

func DefaultCaches() []CacheSpec {
	return []CacheSpec{{Name: "tool-store", Paths: []string{"~/.cache/forge"}, Key: CacheKeyContent}}
}

type SetupStep struct {
	Uses string            `json:"uses"`
	With map[string]string `json:"with,omitempty"`
}

type SecretSpec struct {
	Name    string `json:"name"`
	FromEnv string `json:"fromEnv,omitempty"`
}

type WorkflowSpec struct {
	Name    string `json:"name"`
	Kind    string `json:"kind,omitempty"`
	Content string `json:"content,omitempty"`
	Header  string `json:"header,omitempty"`

	On         OnSpec   `json:"on,omitzero"`
	Job        string   `json:"job,omitempty"`
	StepName   string   `json:"stepName,omitempty"`
	Secret     string   `json:"secret,omitempty"`
	PayloadEnv []string `json:"payloadEnv,omitempty"`
	Command    string   `json:"command,omitempty"`
	Push       bool     `json:"push,omitempty"`

	Token bool `json:"token,omitempty"`

	Packages bool `json:"packages,omitempty"`

	ReportFailure bool `json:"reportFailure,omitempty"`

	TagPattern string   `json:"tagPattern,omitempty"`
	EventType  string   `json:"eventType,omitempty"`
	Consumers  []string `json:"consumers,omitempty"`
	APIBaseURL string   `json:"apiBaseURL,omitempty"`
}

type RunnerSpec struct {
	Name                string `json:"name"`
	Secret              string `json:"secret,omitempty"`
	SetupScript         string `json:"setupScript,omitempty"`
	PollIntervalSeconds int    `json:"pollIntervalSeconds,omitempty"`
	TimeoutMinutes      int    `json:"timeoutMinutes,omitempty"`
}

const (
	KindCommand = "command"
	KindFanOut  = "fan-out"
	KindRelease = "release"
)

func ParseSpec(raw map[string]any) (Spec, error) {
	payload, err := json.Marshal(raw)
	if err != nil {
		return Spec{}, fmt.Errorf("encoding the spec: %w", err)
	}

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
		s.Ref = citypes.DefaultBranch
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

func (s Spec) caches() []CacheSpec {
	if s.Caches == nil {
		return DefaultCaches()
	}

	return *s.Caches
}

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

var folds = map[string]string{
	"containerFile":       "container: {file: ...}",
	"phases":              "jobs: stage or jobs: substage (jobs: whole is the default)",
	"toolchainCachePaths": "caches: [{name, paths, key: repos}]",
	"cron":                "on: {cron: ...}",
	"events":              "on: {events: [...]}",
	"pushBranches":        "on: {push: {branches: [...]}}",
	"pushPathsIgnore":     "on: {push: {ignorePaths: [...]}}",
}

func foldHint(err error) error {
	msg := err.Error()

	if strings.Contains(msg, `unknown field "true"`) {
		return errors.New(`a bare on: key is read as the boolean true, so quote it as "on"`)
	}

	for old, now := range folds {
		if strings.Contains(msg, `unknown field "`+old+`"`) {
			return fmt.Errorf("%s is no longer a key; write %s", old, now)
		}
	}

	if strings.Contains(msg, "cannot unmarshal string into Go struct field") &&
		strings.Contains(msg, ".container") {
		return errors.New("container is an object now; write container: {ref: ...} or container: {file: ...}")
	}

	return err
}

type Controller struct {
	api   func(spec Spec) githubadapter.API
	fs    fsadapter.FS
	now   func() time.Time
	sleep func(time.Duration)
}

func New(
	api func(spec Spec) githubadapter.API, fs fsadapter.FS, now func() time.Time, sleep func(time.Duration),
) (*Controller, error) {
	if now == nil {
		return nil, errors.New(
			"building the workflow controller: it was handed no clock, " +
				"and every dispatch measures its wait through one")
	}

	if sleep == nil {
		return nil, errors.New(
			"building the workflow controller: it was handed no sleep, " +
				"and every dispatch waits between polls through one")
	}

	return &Controller{api: api, fs: fs, now: now, sleep: sleep}, nil
}

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

	if strings.HasSuffix(in.Revision, citypes.DirtySuffix) {
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

func scriptFor(in citypes.RunInput) (string, error) {
	var b strings.Builder

	b.WriteString("set -eu\n")

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
			if len(wave) == 1 {
				fmt.Fprintf(&b, "(cd %s && %s)\n", quote(dirOf(wave[0])), strings.Join(argv, " "))

				continue
			}

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

func quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

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

func dirOf(name string) string {
	if name == "" {
		return "."
	}

	return name
}
