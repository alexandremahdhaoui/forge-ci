package workflowcontroller

import (
	"strings"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The compatibility guarantee for the remote half: with no needs declared,
// every dir is a plain subshell on its own line, which is what this rendered
// before dependencies existed. Nothing about an existing pipeline's script
// changes.
func TestWithNoNeedsTheScriptIsPlainSubshells(t *testing.T) {
	t.Parallel()

	script, err := scriptFor(citypes.RunInput{
		Revision: "abc",
		Repos:    []citypes.RepoCheckout{{Name: "one"}, {Name: "two"}},
		Targets: []citypes.Target{{
			Alias: "build", Binary: "forge", Args: []string{"test-all"}, In: []string{"one", "two"},
		}},
	})
	require.NoError(t, err)

	assert.Contains(t, script, "(cd 'one' && 'forge' 'test-all')\n(cd 'two' && 'forge' 'test-all')")
	assert.NotContains(t, script, "wave_rc", "nothing declared, so nothing to coordinate")
	assert.NotContains(t, script, "jobs -p")
	assert.NotContains(t, script, "forge-factory",
		"nothing converges unless the substage asked; an existing script must not change")
}

// A syncing substage converges the runner's workspace before its first
// target: manifests, then the dependency closure. The bootstrap synced at
// checkout, but the sha pins above may have moved members under it, and a
// sync never resolves the closure at all.
func TestASyncingSubstageConvergesTheRunnerFirst(t *testing.T) {
	t.Parallel()

	script, err := scriptFor(citypes.RunInput{
		Revision: "abc",
		Sync:     true,
		Repos:    []citypes.RepoCheckout{{Name: "one", SHA: "aaa"}},
		Targets: []citypes.Target{{
			Alias: "build", Binary: "forge", Args: []string{"test-all"}, In: []string{"one"},
		}},
	})
	require.NoError(t, err)

	sync := strings.Index(script, "forge-factory sync\nforge-factory lock\n")
	pin := strings.Index(script, "git -C 'one' checkout --detach 'aaa'")
	target := strings.Index(script, "(cd 'one' && 'forge' 'test-all')")

	require.GreaterOrEqual(t, sync, 0, script)
	assert.Greater(t, sync, pin, "the pins move members, so the convergence follows them")
	assert.Greater(t, target, sync, "no target may run on an unconverged workspace")
}

// A wave backgrounds its members and waits. set -eu does not fail a script on
// a background job, so each exit status is collected and the first non-zero
// one is raised after every member has finished - the wave reports every repo
// that broke, not the one that broke first.
func TestAWaveIsBackgroundedAndWaitedFor(t *testing.T) {
	t.Parallel()

	script, err := scriptFor(citypes.RunInput{
		Revision: "abc",
		Repos: []citypes.RepoCheckout{
			{Name: "one"},
			{Name: "two"},
			{Name: "last", Needs: []string{"one", "two"}},
		},
		Targets: []citypes.Target{{
			Alias: "build", Binary: "forge", Args: []string{"test-all"}, In: []string{"one", "two", "last"},
		}},
	})
	require.NoError(t, err)

	assert.Contains(t, script, "wave_rc=0\n(cd 'one' && 'forge' 'test-all') & \n(cd 'two' && 'forge' 'test-all') & \n")
	assert.Contains(t, script, `for job in $(jobs -p); do wait "$job" || wave_rc=$?; done`)
	assert.Contains(t, script, `[ "$wave_rc" -eq 0 ] || exit "$wave_rc"`)

	// The one that waits is a plain subshell again: a wave of one needs no
	// coordination, and `set -eu` fails the script on it directly.
	assert.Contains(t, script, "(cd 'last' && 'forge' 'test-all')\n")
}

// A repo name reaches the shell quoted, wave or not. The dirs come from
// config and a name with a space would otherwise split into two arguments.
func TestAWaveQuotesEveryDir(t *testing.T) {
	t.Parallel()

	script, err := scriptFor(citypes.RunInput{
		Revision: "abc",
		Repos: []citypes.RepoCheckout{
			{Name: "a b"},
			{Name: "c"},
			{Name: "last", Needs: []string{"a b", "c"}},
		},
		Targets: []citypes.Target{{
			Alias: "build", Binary: "forge", Args: []string{"test-all"}, In: []string{"a b", "c", "last"},
		}},
	})
	require.NoError(t, err)
	assert.Contains(t, script, "(cd 'a b' && 'forge' 'test-all') & ")
}

func TestACycleStopsTheScript(t *testing.T) {
	t.Parallel()

	_, err := scriptFor(citypes.RunInput{
		Revision: "abc",
		Repos: []citypes.RepoCheckout{
			{Name: "one", Needs: []string{"two"}},
			{Name: "two", Needs: []string{"one"}},
		},
		Targets: []citypes.Target{{
			Alias: "build", Binary: "forge", Args: []string{"test-all"}, In: []string{"one", "two"},
		}},
	})
	require.ErrorIs(t, err, citypes.ErrCycle)
}
