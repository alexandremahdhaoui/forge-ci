package managercontroller_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/nacl/box"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/githubadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

func githubRealizer(t *testing.T) (managercontroller.GitHubRealizer, *githubadaptermock.MockAPI) {
	t.Helper()

	api := githubadaptermock.NewMockAPI(t)

	return managercontroller.NewGitHubRealizer(t.Context(), fsadapter.New(), api, ""), api
}

func TestGitHubRealizerConvergesAFile(t *testing.T) {
	t.Chdir(t.TempDir())

	r, _ := githubRealizer(t)
	res := citypes.Resource{
		Kind: managercontroller.KindFileContent,
		Name: ".github/workflows/ci.yaml",
		Spec: map[string]any{"content": "on: push\n"},
	}

	// Missing: written.
	action, err := r.Realize(res)
	require.NoError(t, err)
	assert.Equal(t, "converged file .github/workflows/ci.yaml", action)

	// Equal: kept.
	action, err = r.Realize(res)
	require.NoError(t, err)
	assert.Equal(t, "kept file .github/workflows/ci.yaml", action)

	// Drifted by hand: converged back. This is the point of the kind.
	fs := fsadapter.New()
	require.NoError(t, fs.WriteFile(".github/workflows/ci.yaml", []byte("edited by hand")))

	action, err = r.Realize(res)
	require.NoError(t, err)
	assert.Equal(t, "converged file .github/workflows/ci.yaml", action)

	got, err := fs.ReadFile(".github/workflows/ci.yaml")
	require.NoError(t, err)
	assert.Equal(t, "on: push\n", string(got))
}

func TestGitHubRealizerRefusesAFileWithoutContent(t *testing.T) {
	t.Parallel()

	api := githubadaptermock.NewMockAPI(t)
	r := managercontroller.NewGitHubRealizer(t.Context(), fsadapter.New(), api, "")

	_, err := r.Realize(citypes.Resource{Kind: managercontroller.KindFileContent, Name: "x"})
	require.ErrorContains(t, err, "spec.content is required")
}

func TestGitHubRealizerSealsASecret(t *testing.T) {
	pub, priv, err := box.GenerateKey(rand.Reader)
	require.NoError(t, err)

	t.Setenv("TEST_SECRET_SOURCE", "hunter2")

	r, api := githubRealizer(t)
	api.EXPECT().PublicKey(mock.Anything, "o/r").
		Return("k1", base64.StdEncoding.EncodeToString(pub[:]), nil)
	api.EXPECT().PutSecret(mock.Anything, "o/r", "FORGE_CI_GITHUB_TOKEN", "k1", mock.Anything).
		RunAndReturn(func(_ context.Context, _, _, _ string, sealedB64 string) error {
			raw, err := base64.StdEncoding.DecodeString(sealedB64)
			require.NoError(t, err)

			opened, ok := box.OpenAnonymous(nil, raw, pub, priv)
			require.True(t, ok)
			assert.Equal(t, "hunter2", string(opened))

			return nil
		})

	action, err := r.Realize(citypes.Resource{
		Kind: managercontroller.KindActionsSecret,
		Name: "o/r/FORGE_CI_GITHUB_TOKEN",
		Spec: map[string]any{"repo": "o/r", "secret": "FORGE_CI_GITHUB_TOKEN", "fromEnv": "TEST_SECRET_SOURCE"},
	})
	require.NoError(t, err)
	assert.Equal(t, "sealed secret FORGE_CI_GITHUB_TOKEN on o/r from $TEST_SECRET_SOURCE", action)
}

func TestGitHubRealizerRefusesAnEmptySecretSource(t *testing.T) {
	t.Setenv("EMPTY_SOURCE", "")

	r, _ := githubRealizer(t)

	_, err := r.Realize(citypes.Resource{
		Kind: managercontroller.KindActionsSecret,
		Name: "o/r/S",
		Spec: map[string]any{"repo": "o/r", "secret": "S", "fromEnv": "EMPTY_SOURCE"},
	})
	require.ErrorContains(t, err, "EMPTY_SOURCE is empty")
}

func TestGitHubRealizerSecretDefaultsToGithubToken(t *testing.T) {
	pub, _, err := box.GenerateKey(rand.Reader)
	require.NoError(t, err)

	t.Setenv("GITHUB_TOKEN", "pat")

	r, api := githubRealizer(t)
	api.EXPECT().PublicKey(mock.Anything, "o/r").
		Return("k1", base64.StdEncoding.EncodeToString(pub[:]), nil)
	api.EXPECT().PutSecret(mock.Anything, "o/r", "S", "k1", mock.Anything).Return(nil)

	action, err := r.Realize(citypes.Resource{
		Kind: managercontroller.KindActionsSecret,
		Name: "o/r/S",
		Spec: map[string]any{"repo": "o/r", "secret": "S"},
	})
	require.NoError(t, err)
	assert.Contains(t, action, "from $GITHUB_TOKEN")
}

func TestGitHubRealizerEnablesAWorkflow(t *testing.T) {
	t.Parallel()

	r, api := githubRealizer(t)
	api.EXPECT().EnableWorkflow(mock.Anything, "o/r", "intake.yaml").Return(nil)

	action, err := r.Realize(citypes.Resource{
		Kind: managercontroller.KindWorkflowEnabled,
		Name: "o/r/intake.yaml",
		Spec: map[string]any{"repo": "o/r", "workflow": "intake.yaml"},
	})
	require.NoError(t, err)
	assert.Equal(t, "enabled workflow intake.yaml on o/r", action)
}

func TestGitHubRealizerEnableIsPendingBeforeTheFirstPush(t *testing.T) {
	t.Parallel()

	// The first bootstrap runs before the pipeline's push has ever
	// delivered the workflow file, so the enable 404s. That is pending,
	// not broken: the next reconcile after the push completes it.
	r, api := githubRealizer(t)
	api.EXPECT().EnableWorkflow(mock.Anything, "o/r", "new.yaml").
		Return(githubadapter.ErrNotFound)

	action, err := r.Realize(citypes.Resource{
		Kind: managercontroller.KindWorkflowEnabled,
		Name: "o/r/new.yaml",
		Spec: map[string]any{"repo": "o/r", "workflow": "new.yaml"},
	})
	require.NoError(t, err)
	assert.Contains(t, action, "not on the remote yet")
}

func TestGitHubRealizerRefusesAnUnknownKind(t *testing.T) {
	t.Parallel()

	r, _ := githubRealizer(t)

	_, err := r.Realize(citypes.Resource{Kind: "table", Name: "x"})
	require.ErrorContains(t, err, `cannot realize kind "table"`)
}

func TestGitHubRealizerRefusesMissingSpecKeys(t *testing.T) {
	t.Parallel()

	r, _ := githubRealizer(t)

	_, err := r.Realize(citypes.Resource{Kind: managercontroller.KindActionsSecret, Name: "x"})
	require.ErrorContains(t, err, "spec.repo and spec.secret are required")

	_, err = r.Realize(citypes.Resource{Kind: managercontroller.KindWorkflowEnabled, Name: "x"})
	require.ErrorContains(t, err, "spec.repo and spec.workflow are required")
}

func TestGitHubRealizerResolvesRelativeFilesAgainstTheRoot(t *testing.T) {
	t.Chdir(t.TempDir())

	root := t.TempDir()
	api := githubadaptermock.NewMockAPI(t)
	r := managercontroller.NewGitHubRealizer(t.Context(), fsadapter.New(), api, root)

	// The resource name stays root-relative (that is the ownership id);
	// realization must land under the root, not under the process cwd.
	_, err := r.Realize(citypes.Resource{
		Kind: managercontroller.KindFileContent,
		Name: "repo/.github/workflows/ci.yaml",
		Spec: map[string]any{"content": "on: push\n"},
	})
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(root, "repo/.github/workflows/ci.yaml"))
	require.NoFileExists(t, "repo/.github/workflows/ci.yaml")
}

// TestA403OnTheSecretsAPINamesTheRealCause pins the message that would have
// saved three CI runs.
//
// GitHub excludes a workflow run's own injected token from the secrets API
// entirely, and no permissions: block grants it. The raw error says only
// "Resource not accessible by integration", which reads exactly like a
// permissions problem you can fix in the workflow, and is not.
func TestA403OnTheSecretsAPINamesTheRealCause(t *testing.T) {
	r, api := githubRealizer(t)

	api.EXPECT().PublicKey(mock.Anything, "owner/repo").
		Return("", "", errors.New(`status 403: {"message":"Resource not accessible by integration"}`)).Once()

	t.Setenv("A_PAT", "value-to-seal")

	_, err := r.Realize(citypes.Resource{
		Kind: managercontroller.KindActionsSecret,
		Name: "owner/repo/FORGE_CI_GITHUB_TOKEN",
		Spec: map[string]any{"repo": "owner/repo", "secret": "FORGE_CI_GITHUB_TOKEN", "fromEnv": "A_PAT"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "can never manage repository secrets")
	require.Contains(t, err.Error(), "spec.tokenEnv",
		"the message must name the knob that fixes it")
}

// The 403 half of the same condition. This is the one the live bootstrap
// hit: the realizer tolerated only ErrNotFound, so a first bootstrap died on
// its first repo with seven still to provision.
func TestGitHubRealizerEnableIsPendingWhenTheWorkflowIsNotActiveYet(t *testing.T) {
	t.Parallel()

	r, api := githubRealizer(t)
	api.EXPECT().EnableWorkflow(mock.Anything, "o/r", "fresh.yaml").
		Return(fmt.Errorf("%w: not active", githubadapter.ErrInactive))

	action, err := r.Realize(citypes.Resource{
		Kind: managercontroller.KindWorkflowEnabled,
		Name: "o/r/fresh.yaml",
		Spec: map[string]any{"repo": "o/r", "workflow": "fresh.yaml"},
	})
	require.NoError(t, err)
	assert.Contains(t, action, "not on the remote yet")
}

// A denied token must still stop the run. Tolerating every 403 would turn a
// credential problem into a pipeline that provisions nothing and says it
// worked.
func TestGitHubRealizerEnableStillFailsOnARealDenial(t *testing.T) {
	t.Parallel()

	r, api := githubRealizer(t)
	api.EXPECT().EnableWorkflow(mock.Anything, "o/r", "denied.yaml").
		Return(errors.New("status 403: Resource not accessible by personal access token"))

	_, err := r.Realize(citypes.Resource{
		Kind: managercontroller.KindWorkflowEnabled,
		Name: "o/r/denied.yaml",
		Spec: map[string]any{"repo": "o/r", "workflow": "denied.yaml"},
	})
	require.ErrorContains(t, err, "status 403")
}
