package triggercontroller_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/triggercontroller"
	"github.com/stretchr/testify/require"
)

func declareSpec() map[string]any {
	return map[string]any{
		"watch": []any{"one", "two"},
		"notify": map[string]any{
			"owner":     "an-owner",
			"factory":   "a-factory",
			"eventType": "member-pushed",
			"secret":    "A_TOKEN",
		},
	}
}

func TestAWatchListWithNoNotifyBlockDeclaresNothing(t *testing.T) {
	t.Parallel()

	c := triggercontroller.New(gitadapter.New(nil))

	out, err := c.Declare(map[string]any{"watch": []any{"one"}})
	require.NoError(t, err)
	require.Empty(t, out.Resources)
}

func TestEveryWatchedRepoGetsAWorkflowASecretAndEnablement(t *testing.T) {
	t.Parallel()

	c := triggercontroller.New(gitadapter.New(nil))

	out, err := c.Declare(declareSpec())
	require.NoError(t, err)

	names := map[string]string{}
	for _, r := range out.Resources {
		names[r.Name] = r.Kind
	}

	require.Equal(t, map[string]string{
		"one/.github/workflows/notify.yaml": "file-content",
		"an-owner/one/A_TOKEN":              "actions-secret",
		"an-owner/one/notify.yaml":          "workflow-enabled",
		"two/.github/workflows/notify.yaml": "file-content",
		"an-owner/two/A_TOKEN":              "actions-secret",
		"an-owner/two/notify.yaml":          "workflow-enabled",
	}, names)
}

// The secret is written by a bootstrap and never by an apply. An apply runs
// on a runner, where the value it would seal does not exist, so writing it
// there replaces a working credential with an empty string.
func TestOnlyTheSecretIsBootstrapOnly(t *testing.T) {
	t.Parallel()

	c := triggercontroller.New(gitadapter.New(nil))

	out, err := c.Declare(declareSpec())
	require.NoError(t, err)

	for _, r := range out.Resources {
		require.Equal(t, r.Kind == "actions-secret", r.BootstrapOnly, r.Name)
	}
}

func TestTheWorkflowIsRenderedWholeAndMatchesTheGolden(t *testing.T) {
	t.Parallel()

	c := triggercontroller.New(gitadapter.New(nil))

	out, err := c.Declare(declareSpec())
	require.NoError(t, err)

	var content string

	for _, r := range out.Resources {
		if r.Name == "one/.github/workflows/notify.yaml" {
			content, _ = r.Spec["content"].(string)
		}
	}

	golden := filepath.Join("testdata", "notify.yaml")

	if os.Getenv("UPDATE_GOLDEN") != "" {
		require.NoError(t, os.WriteFile(golden, []byte(content), 0o600))
	}

	want, err := os.ReadFile(golden)
	require.NoError(t, err)
	require.Equal(t, string(want), content)
}

func TestAFactoryThatWatchesItselfIsRefused(t *testing.T) {
	t.Parallel()

	spec := declareSpec()
	spec["watch"] = []any{"one", "a-factory"}

	c := triggercontroller.New(gitadapter.New(nil))

	_, err := c.Declare(spec)
	require.ErrorContains(t, err, "never settles")
}

func TestANotifyBlockMissingItsTargetIsRefused(t *testing.T) {
	t.Parallel()

	for _, key := range []string{"owner", "factory", "eventType", "secret"} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			spec := declareSpec()
			notify, _ := spec["notify"].(map[string]any)
			delete(notify, key)

			c := triggercontroller.New(gitadapter.New(nil))

			_, err := c.Declare(spec)
			require.ErrorContains(t, err, key)
		})
	}
}

func TestANotifyBlockWithNoWatchListIsRefused(t *testing.T) {
	t.Parallel()

	spec := declareSpec()
	delete(spec, "watch")

	c := triggercontroller.New(gitadapter.New(nil))

	_, err := c.Declare(spec)
	require.ErrorIs(t, err, triggercontroller.ErrWatch)
}

func TestTheWorkflowFileNameAndBranchAreOverridable(t *testing.T) {
	t.Parallel()

	spec := declareSpec()
	notify, _ := spec["notify"].(map[string]any)
	notify["workflow"] = "tell-the-factory"
	notify["branch"] = "trunk"

	c := triggercontroller.New(gitadapter.New(nil))

	out, err := c.Declare(spec)
	require.NoError(t, err)

	var content string

	for _, r := range out.Resources {
		if r.Name == "one/.github/workflows/tell-the-factory.yaml" {
			content, _ = r.Spec["content"].(string)
		}
	}

	require.Contains(t, content, "name: tell-the-factory\n")
	require.Contains(t, content, "branches: [trunk]")
}

// fromEnv defaults to the secret's own name, so the common case names the
// credential once rather than twice.
func TestFromEnvDefaultsToTheSecretName(t *testing.T) {
	t.Parallel()

	c := triggercontroller.New(gitadapter.New(nil))

	out, err := c.Declare(declareSpec())
	require.NoError(t, err)

	for _, r := range out.Resources {
		if r.Kind == "actions-secret" {
			require.Equal(t, "A_TOKEN", r.Spec["fromEnv"])
		}
	}
}

func TestAWatchEntryWithAPathKeepsThePathAndUsesItsBaseAsTheRepo(t *testing.T) {
	t.Parallel()

	spec := declareSpec()
	spec["watch"] = []any{"nested/one"}

	c := triggercontroller.New(gitadapter.New(nil))

	out, err := c.Declare(spec)
	require.NoError(t, err)

	names := []string{}
	for _, r := range out.Resources {
		names = append(names, r.Name)
	}

	require.Contains(t, names, "nested/one/.github/workflows/notify.yaml")
	require.Contains(t, names, "an-owner/one/A_TOKEN")
}

func TestTheRenderedWorkflowNamesTheRepoItLivesIn(t *testing.T) {
	t.Parallel()

	c := triggercontroller.New(gitadapter.New(nil))

	out, err := c.Declare(declareSpec())
	require.NoError(t, err)

	for _, r := range out.Resources {
		if r.Kind != "file-content" {
			continue
		}

		repo := strings.Split(r.Name, "/")[0]
		content, _ := r.Spec["content"].(string)

		require.Contains(t, content, "that "+repo+" moved")
	}
}
