package managercontroller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
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
	root string
}

var _ Realizer = GitHubRealizer{}

// NewGitHubRealizer builds the realizer. The context bounds every API
// call of one reconcile; root is the pipeline root a relative file name
// resolves against, so realization lands in the same place wherever
// forge-ci was started from. Empty means the working directory.
func NewGitHubRealizer(ctx context.Context, fs fsadapter.FS, api githubadapter.API, root string) GitHubRealizer {
	return GitHubRealizer{ctx: ctx, fs: fs, api: api, root: root}
}

// Kind names the realizer.
func (GitHubRealizer) Kind() string {
	return "github"
}

// Realize converges one resource.
func (r GitHubRealizer) Realize(res citypes.Resource) (string, error) {
	switch res.Kind {
	case KindFileContent:
		return r.realizeFile(res)
	case KindActionsSecret:
		return r.realizeSecret(res)
	case KindWorkflowEnabled:
		return r.realizeEnable(res)
	default:
		return "", fmt.Errorf("the github manager cannot realize kind %q, it knows %s, %s and %s",
			res.Kind, KindFileContent, KindActionsSecret, KindWorkflowEnabled)
	}
}

// realizeFile writes the declared content when the file is missing or
// differs, and keeps it when it already matches. The pipeline's own push
// delivers the file to the remote; this manager converges the checkout.
func (r GitHubRealizer) realizeFile(res citypes.Resource) (string, error) {
	want, _ := res.Spec["content"].(string)
	if want == "" {
		return "", errors.New("spec.content is required")
	}

	path := res.Name
	if r.root != "" && !filepath.IsAbs(path) {
		path = filepath.Join(r.root, path)
	}

	have, err := r.fs.ReadFile(path)
	if err == nil && string(have) == want {
		return "kept file " + res.Name, nil
	}

	if dir := filepath.Dir(path); dir != "." {
		if err := r.fs.MkdirAll(dir); err != nil {
			return "", err
		}
	}

	if err := r.fs.WriteFile(path, []byte(want)); err != nil {
		return "", err
	}

	return "converged file " + res.Name, nil
}

// realizeSecret seals the value from the named environment variable
// against the repo key and puts it. An empty source variable is an error:
// a bootstrap that silently writes an empty secret looks green and fails
// at the first workflow run.
func (r GitHubRealizer) realizeSecret(res citypes.Resource) (string, error) {
	repo, _ := res.Spec["repo"].(string)
	secret, _ := res.Spec["secret"].(string)

	if repo == "" || secret == "" {
		return "", errors.New("spec.repo and spec.secret are required")
	}

	fromEnv, _ := res.Spec["fromEnv"].(string)
	if fromEnv == "" {
		fromEnv = "GITHUB_TOKEN"
	}

	value := os.Getenv(fromEnv)
	if value == "" {
		return "", fmt.Errorf("environment variable %s is empty; export it (.envrc) before bootstrapping", fromEnv)
	}

	keyID, keyB64, err := r.api.PublicKey(r.ctx, repo)
	if err != nil {
		// The one failure worth naming. GitHub excludes a workflow run's own
		// injected token from the secrets API entirely - no permissions:
		// block grants it, on purpose, so a workflow cannot use its own
		// token to rewrite or read back the secrets it runs under. The
		// token variable defaults to GITHUB_TOKEN, which means an operator's
		// PAT on a laptop and that excluded token in a runner, so the same
		// spec works in one place and 403s in the other. Three CI runs went
		// into reading that message as a permissions problem.
		if strings.Contains(err.Error(), "403") {
			return "", fmt.Errorf(
				"%w: a run's own GITHUB_TOKEN can never manage repository secrets, "+
					"whatever permissions: says. Point this manager at a PAT with "+
					"spec.tokenEnv on the manager; it defaults to GITHUB_TOKEN, "+
					"which is an operator's PAT on a laptop and the excluded token "+
					"in a runner",
				err)
		}

		return "", err
	}

	sealed, err := githubadapter.Seal(keyB64, value)
	if err != nil {
		return "", err
	}

	if err := r.api.PutSecret(r.ctx, repo, secret, keyID, sealed); err != nil {
		return "", err
	}

	return fmt.Sprintf("sealed secret %s on %s from $%s", secret, repo, fromEnv), nil
}

// realizeEnable enables the workflow by file name. A 404 means the file
// has never been pushed - the very first bootstrap runs before the
// pipeline's push delivers it - so it reports pending instead of failing,
// and a later reconcile completes it.
func (r GitHubRealizer) realizeEnable(res citypes.Resource) (string, error) {
	repo, _ := res.Spec["repo"].(string)
	workflow, _ := res.Spec["workflow"].(string)

	if repo == "" || workflow == "" {
		return "", errors.New("spec.repo and spec.workflow are required")
	}

	err := r.api.EnableWorkflow(r.ctx, repo, workflow)
	if errors.Is(err, githubadapter.ErrNotFound) {
		return fmt.Sprintf("workflow %s not on the remote yet; enabled on a later reconcile", workflow), nil
	}

	if err != nil {
		return "", err
	}

	return fmt.Sprintf("enabled workflow %s on %s", workflow, repo), nil
}
