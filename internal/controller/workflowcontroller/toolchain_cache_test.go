package workflowcontroller_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/workflowcontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

// A factory whose toolchain is built from the checkout builds it in every
// job of a run, which is one compile per job for the same shas. A cache
// keyed by repos wraps the script: restored before, saved on a miss after,
// keyed on the heads of the repos the pipeline declares and on nothing
// looser.
func TestAReposCacheWrapsTheScriptAndIsKeyedOnTheDeclaredRepos(t *testing.T) {
	t.Parallel()

	spec := phasedSpec()
	spec.Container = workflowcontroller.ContainerSpec{}
	spec.Workspace.ToolchainScript = "(cd tool && go install ./cmd/...)\necho \"$HOME/go/bin\" >> \"$GITHUB_PATH\"\n"
	spec.Repos = []citypes.DeclaredRepo{{Name: "tool"}, {Name: "lib"}}

	plain := renderCI(t, spec)
	assert.NotContains(t, plain, "Restore the toolchain", "no cache named, no cache")
	assert.NotContains(t, plain, "toolchain-key")

	caches := append(workflowcontroller.DefaultCaches(), workflowcontroller.CacheSpec{
		Name: "toolchain", Paths: []string{"~/go/bin", "~/.cache/go-build"}, Key: workflowcontroller.CacheKeyRepos,
	})
	spec.Caches = &caches

	ci := renderCI(t, spec)

	restore := strings.Index(ci, "      - name: Restore the toolchain\n")
	install := strings.Index(ci, "      - name: Install the toolchain from the workspace\n")
	save := strings.Index(ci, "      - name: Save the toolchain\n")
	require.True(t, restore > 0 && install > restore && save > install,
		"restore, then the script, then the save")

	// The key hashes the declared repos' heads after the checkout and is
	// used exactly: no restore-keys prefix, so a different set of shas
	// rebuilds - and only the declared repos, so a stage that writes into a
	// repo the revision does not cover moves no key.
	assert.Contains(t, ci, `for d in tool lib; do git -C "$d" rev-parse HEAD; done | sha256sum`)
	assert.Contains(t, ci, "          key: toolchain-${{ runner.os }}-${{ steps.toolchain-key.outputs.key }}\n")
	assert.NotContains(t, ci[restore:save], "restore-keys")

	assert.Contains(t, ci, "          path: |\n            ~/go/bin\n            ~/.cache/go-build\n")
	assert.Contains(t, ci, "        if: steps.toolchain-cache.outputs.cache-hit != 'true'\n        uses: actions/cache/save@v6\n")

	// Every workspace-building job carries the three steps.
	assert.Equal(t, strings.Count(ci, "Install the toolchain from the workspace"),
		strings.Count(ci, "      - name: Restore the toolchain\n"))
	assert.Equal(t, strings.Count(ci, "Install the toolchain from the workspace"),
		strings.Count(ci, "      - name: Save the toolchain\n"))
}

// A cache keyed by repos hashes the pipeline's repos; a pipeline that
// declares none has nothing to hash and is refused rather than handed one
// key every run shares.
func TestAReposCacheNeedsDeclaredRepos(t *testing.T) {
	t.Parallel()

	spec := phasedSpec()
	caches := []workflowcontroller.CacheSpec{{Name: "toolchain", Paths: []string{"~/go/bin"}, Key: "repos"}}
	spec.Caches = &caches

	_, err := workflowcontroller.RenderAll(spec)
	require.ErrorContains(t, err, "a cache keyed by repos needs the pipeline to declare repos")
}

// The key that used to name the toolchain's paths is refused naming the
// caches entry that replaced it.
func TestToolchainCachePathsIsFolded(t *testing.T) {
	t.Parallel()

	_, err := workflowcontroller.ParseSpec(map[string]any{
		"repo":      "o/r",
		"container": map[string]any{"ref": "ghcr.io/o/toolchain:v1"},
		"workspace": map[string]any{
			"bootstrapCommand":    "forge clone git@github.com:o/r.git .",
			"toolchainCachePaths": []any{"~/go/bin"},
		},
	})
	require.ErrorContains(t, err, "toolchainCachePaths is no longer a key; write caches: [{name, paths, key: repos}]")
}
