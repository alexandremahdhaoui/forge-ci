package workflowcontroller_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/workflowcontroller"
)

// A factory whose toolchain is built from the checkout builds it in every
// job of a run, which is one compile per job for the same shas. Naming what
// the script installs caches it around the script: restored before, saved
// on a miss after, keyed on every member's HEAD and on nothing looser.
func TestAToolchainCacheWrapsTheScriptAndIsKeyedOnTheMembers(t *testing.T) {
	t.Parallel()

	spec := phasedSpec()
	spec.Container = ""
	spec.Workspace.ToolchainScript = "(cd tool && go install ./cmd/...)\necho \"$HOME/go/bin\" >> \"$GITHUB_PATH\"\n"

	plain := renderCI(t, spec)
	assert.NotContains(t, plain, "Restore the toolchain", "no paths named, no cache")
	assert.NotContains(t, plain, "toolchain-key")

	spec.Workspace.ToolchainCachePaths = []string{"~/go/bin", "~/.cache/go-build"}

	ci := renderCI(t, spec)

	restore := strings.Index(ci, "      - name: Restore the toolchain\n")
	install := strings.Index(ci, "      - name: Install the toolchain from the workspace\n")
	save := strings.Index(ci, "      - name: Save the toolchain\n")
	require.True(t, restore > 0 && install > restore && save > install,
		"restore, then the script, then the save")

	// The key hashes every member's HEAD after the checkout and is used
	// exactly: no restore-keys prefix, so a different set of shas rebuilds.
	assert.Contains(t, ci, `for d in */.git; do git -C "${d%/.git}" rev-parse HEAD; done | sha256sum`)
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

func TestToolchainCachePathsNeedAScript(t *testing.T) {
	t.Parallel()

	_, err := workflowcontroller.ParseSpec(map[string]any{
		"repo":      "o/r",
		"container": "ghcr.io/o/toolchain:v1",
		"workspace": map[string]any{
			"bootstrapCommand":    "forge clone git@github.com:o/r.git .",
			"toolchainCachePaths": []any{"~/go/bin"},
		},
		"workflows": []any{map[string]any{
			"name": "ci", "kind": "command", "command": "true", "secret": "S",
			"events": []any{"member-pushed"},
		}},
	})
	require.ErrorContains(t, err, "toolchainCachePaths names what toolchainScript installs")
}
