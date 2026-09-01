package managercontroller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

// Resource kinds the github manager realizes. The vocabulary is open by
// design; these are the ones this realizer knows.
const (
	// KindFileContent is a file converged to declared content: missing or
	// differing is written, equal is kept. This is deliberately not the
	// local manager's "file", which never overwrites - a workflow file
	// must follow its engine, drift included.
	KindFileContent = "file-content"
	// KindActionsSecret is one repository Actions secret, sealed and
	// written every reconcile: the API is write-only, so a put is the
	// only convergence there is.
	KindActionsSecret = "actions-secret"
	// KindWorkflowEnabled is one workflow file enabled on the remote.
	KindWorkflowEnabled = "workflow-enabled"
)

// GitHubRealizer realizes the GitHub-side kinds: converged files on the
// local checkout, sealed Actions secrets and workflow enablement through
// the API.
type GitHubRealizer struct {
	ctx  context.Context
	fs   fsadapter.FS
	api  githubadapter.API
	git  gitadapter.Git
	root string
}

var _ Realizer = GitHubRealizer{}

// NewGitHubRealizer builds the realizer. The context bounds every API
// call of one reconcile; root is the pipeline root a relative file name
// resolves against, so realization lands in the same place wherever
// forge-ci was started from. Empty means the working directory.
//
// git is how this manager makes a converged file durable, which is what
// Settle does. A nil one settles nothing and still reports what changed:
// the run stops either way, and a manager that cannot push is a manager
// whose operator commits by hand, not a broken one.
func NewGitHubRealizer(
	ctx context.Context, fs fsadapter.FS, api githubadapter.API, git gitadapter.Git, root string,
) GitHubRealizer {
	return GitHubRealizer{ctx: ctx, fs: fs, api: api, git: git, root: root}
}

// Kind names the realizer.
func (GitHubRealizer) Kind() string {
	return "github"
}

// Realize converges one resource.
func (r GitHubRealizer) Realize(res citypes.Resource, opts Options) (Action, error) {
	switch res.Kind {
	case KindFileContent:
		return r.realizeFile(res, opts)
	case KindActionsSecret:
		return r.realizeSecret(res, opts)
	case KindWorkflowEnabled:
		return r.realizeEnable(res, opts)
	default:
		return Action{}, fmt.Errorf("the github manager cannot realize kind %q, it knows %s, %s and %s",
			res.Kind, KindFileContent, KindActionsSecret, KindWorkflowEnabled)
	}
}

// realizeFile writes the declared content when the file is missing or
// differs, and keeps it when it already matches. The pipeline's own push
// delivers the file to the remote; this manager converges the checkout.
func (r GitHubRealizer) realizeFile(res citypes.Resource, opts Options) (Action, error) {
	want, _ := res.Spec["content"].(string)
	if want == "" {
		return Action{}, errors.New("spec.content is required")
	}

	path := res.Name
	if r.root != "" && !filepath.IsAbs(path) {
		path = filepath.Join(r.root, path)
	}

	have, err := r.fs.ReadFile(path)
	if err == nil && string(have) == want {
		return Kept("kept file " + res.Name), nil
	}

	if opts.DryRun {
		return Did(opts.would("converge file " + res.Name)), nil
	}

	if dir := filepath.Dir(path); dir != "." {
		if err := r.fs.MkdirAll(dir); err != nil {
			return Action{}, err
		}
	}

	if err := r.fs.WriteFile(path, []byte(want)); err != nil {
		return Action{}, err
	}

	return Did("converged file " + res.Name), nil
}

// realizeSecret seals the value from the named environment variable
// against the repo key and puts it. An empty source variable is an error:
// a bootstrap that silently writes an empty secret looks green and fails
// at the first workflow run.
//
// The secrets API never returns a value, so existence is the only actual
// state there is to read back. A secret that already exists is therefore
// left alone: a put would silently replace whatever was there, and two
// operators bootstrapping with different credentials would leave one winner
// with nothing to detect it. Git arbitrates every other collision by
// refusing a stale push; this is the one it cannot see.
//
// Force rewrites it anyway, which is how a rotation is asked for.
func (r GitHubRealizer) realizeSecret(res citypes.Resource, opts Options) (Action, error) {
	repo, _ := res.Spec["repo"].(string)
	secret, _ := res.Spec["secret"].(string)

	if repo == "" || secret == "" {
		return Action{}, errors.New("spec.repo and spec.secret are required")
	}

	fromEnv, _ := res.Spec["fromEnv"].(string)
	if fromEnv == "" {
		fromEnv = "GITHUB_TOKEN"
	}

	// Actual state first. A secret that already exists is kept without a
	// put, and keeping needs no value - demanding the env before looking
	// made every environment that holds no credentials fail on secrets
	// that were already sealed, which is exactly the case a re-run meets.
	existed, err := r.api.SecretExists(r.ctx, repo, secret)
	if err != nil {
		return Action{}, explainSecretsDenial(err)
	}

	if existed && !opts.Force {
		return Kept(fmt.Sprintf(
			"kept secret %s on %s: it already exists and cannot be read back, so it is not rewritten. "+
				"--force rotates it", secret, repo)), nil
	}

	value := os.Getenv(fromEnv)
	if value == "" {
		return Action{}, fmt.Errorf(
			"environment variable %s is empty; export it (.envrc) before bootstrapping", fromEnv)
	}

	text := fmt.Sprintf("seal secret %s on %s from $%s", secret, repo, fromEnv)
	if opts.DryRun {
		return Did(opts.would(text)), nil
	}

	keyID, keyB64, err := r.api.PublicKey(r.ctx, repo)
	if err != nil {
		return Action{}, explainSecretsDenial(err)
	}

	sealed, err := githubadapter.Seal(keyB64, value)
	if err != nil {
		return Action{}, err
	}

	if err := r.api.PutSecret(r.ctx, repo, secret, keyID, sealed); err != nil {
		return Action{}, err
	}

	if existed {
		return Did(fmt.Sprintf("rotated secret %s on %s from $%s", secret, repo, fromEnv)), nil
	}

	return Did(fmt.Sprintf("sealed secret %s on %s from $%s", secret, repo, fromEnv)), nil
}

// explainSecretsDenial names the one failure of the secrets API worth
// naming, and it wraps every call to that API rather than one of them.
//
// GitHub excludes a workflow run's own injected token from the secrets API
// entirely - no permissions: block grants it, on purpose, so a workflow
// cannot use its own token to rewrite or read back the secrets it runs
// under. The token variable defaults to GITHUB_TOKEN, which means an
// operator's PAT on a laptop and that excluded token in a runner, so the
// same spec works in one place and 403s in the other. Three CI runs went
// into reading that message as a permissions problem.
func explainSecretsDenial(err error) error {
	if !strings.Contains(err.Error(), "403") {
		return err
	}

	return fmt.Errorf(
		"%w: a run's own GITHUB_TOKEN can never manage repository secrets, "+
			"whatever permissions: says. Point this manager at a PAT with "+
			"spec.tokenEnv on the manager; it defaults to GITHUB_TOKEN, "+
			"which is an operator's PAT on a laptop and the excluded token "+
			"in a runner",
		err)
}

// realizeEnable enables the workflow by file name. A file the pipeline has
// written but not yet pushed cannot be enabled, and the very first bootstrap
// always meets that state, so it reports pending instead of failing and a
// later reconcile completes it.
//
// GitHub says so two different ways. A file it has never seen is 404. A file
// it knows but that is not on the default branch is 403 "not active", which
// is the answer a first bootstrap actually gets - tolerating only the 404
// killed the run on its first repo. A real permission denial is 403 as well
// and stays fatal; the adapter separates them.
func (r GitHubRealizer) realizeEnable(res citypes.Resource, opts Options) (Action, error) {
	repo, _ := res.Spec["repo"].(string)
	workflow, _ := res.Spec["workflow"].(string)

	if repo == "" || workflow == "" {
		return Action{}, errors.New("spec.repo and spec.workflow are required")
	}

	// The state is read first, and it is not an optimisation. Enabling a
	// workflow that is already enabled succeeds, so a realizer that always
	// enabled would report a change on every single run, and a run that
	// reports a change stops. The pipeline would never reach a stage again.
	state, err := r.api.WorkflowState(r.ctx, repo, workflow)
	if err == nil && state == githubadapter.StateActive {
		return Kept(fmt.Sprintf("workflow %s already enabled on %s", workflow, repo)), nil
	}

	if err != nil && !errors.Is(err, githubadapter.ErrNotFound) {
		return Action{}, err
	}

	if opts.DryRun {
		// A plan cannot know whether the enable would land: the file it needs
		// is one this same reconcile would have written and not yet pushed.
		// Saying it would try is the honest answer.
		return Did(opts.would(fmt.Sprintf("enable workflow %s on %s", workflow, repo))), nil
	}

	err = r.api.EnableWorkflow(r.ctx, repo, workflow)
	if errors.Is(err, githubadapter.ErrNotFound) || errors.Is(err, githubadapter.ErrInactive) {
		// Nothing was enabled, so nothing changed. The file that will make
		// this succeed is the one the file-content resource just wrote, and
		// that resource is what stops the run.
		return Kept(fmt.Sprintf(
			"workflow %s not on the remote yet; enabled on a later reconcile", workflow)), nil
	}

	if err != nil {
		return Action{}, err
	}

	return Did(fmt.Sprintf("enabled workflow %s on %s", workflow, repo)), nil
}
