package citypes_test

import (
	"strings"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The compatibility guarantee, and the reason it is first: a pipeline that
// declares nothing must behave exactly as it did before dependencies
// existed. One dir per wave, in the order given, is what a plain sequential
// loop did.
func TestWithNoNeedsEveryDirIsItsOwnWaveInOrder(t *testing.T) {
	t.Parallel()

	waves, err := citypes.Waves([]string{"c", "a", "b"}, nil)
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"c"}, {"a"}, {"b"}}, waves)
}

// A need on a repo this target does not name is not an edge here. The
// declaration says what must be built first when both are being built, not
// that the other must be built at all.
func TestANeedOutsideTheTargetIsNotAnEdge(t *testing.T) {
	t.Parallel()

	waves, err := citypes.Waves([]string{"a", "b"}, map[string][]string{"a": {"elsewhere"}})
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"a"}, {"b"}}, waves,
		"nothing constrains these two, so they stay one at a time")
}

// One edge is enough to group everything else. That is the whole point:
// declare that e waits on the four, and the four run together without
// anybody writing them in a wave.
func TestOneEdgeGroupsTheRest(t *testing.T) {
	t.Parallel()

	waves, err := citypes.Waves(
		[]string{"go", "rust", "python", "typescript", "e2e"},
		map[string][]string{"e2e": {"go", "rust", "python", "typescript"}})
	require.NoError(t, err)
	assert.Equal(t, [][]string{
		{"go", "rust", "python", "typescript"},
		{"e2e"},
	}, waves)
}

func TestItLayersATransitiveChain(t *testing.T) {
	t.Parallel()

	waves, err := citypes.Waves(
		[]string{"e2e", "go", "spec", "rust"},
		map[string][]string{
			"go":   {"spec"},
			"rust": {"spec"},
			"e2e":  {"go", "rust"},
		})
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"spec"}, {"go", "rust"}, {"e2e"}}, waves,
		"spec first, the two that need it together, then the one that needs those")
}

// Declared order survives inside a wave. A wave is a set so the order decides
// nothing, and keeping it makes the rendered script and the log read in the
// order the operator wrote rather than in map order, which changes every run.
func TestAWaveKeepsDeclaredOrder(t *testing.T) {
	t.Parallel()

	in := []string{"zebra", "alpha", "middle", "last"}
	needs := map[string][]string{"last": {"zebra"}}

	for range 20 {
		waves, err := citypes.Waves(in, needs)
		require.NoError(t, err)
		require.Equal(t, [][]string{{"zebra", "alpha", "middle"}, {"last"}}, waves)
	}
}

func TestACycleIsRefusedAndNamesEveryoneInIt(t *testing.T) {
	t.Parallel()

	_, err := citypes.Waves([]string{"stuck-one", "stuck-two", "free"},
		map[string][]string{"stuck-one": {"stuck-two"}, "stuck-two": {"stuck-one"}})
	require.ErrorIs(t, err, citypes.ErrCycle)

	// The names, not the whole message: only what could not be ordered is
	// listed, so the report points at the two repos to look at rather than
	// at every repo in the run.
	_, named, ok := strings.Cut(err.Error(), ": ")
	require.True(t, ok)
	assert.Equal(t, "stuck-one, stuck-two", named,
		"free waits on nothing, so it was ordered and is not in the cycle")
}

// A target naming no repo runs once at the root, which is one wave of one.
func TestATargetWithNoReposIsOneWaveAtTheRoot(t *testing.T) {
	t.Parallel()

	waves, err := citypes.WavesFor(citypes.Target{Alias: "a"}, citypes.RunInput{})
	require.NoError(t, err)
	assert.Equal(t, [][]string{{""}}, waves)
}

func TestWavesForReadsTheGraphOffTheCheckouts(t *testing.T) {
	t.Parallel()

	in := citypes.RunInput{Repos: []citypes.RepoCheckout{
		{Name: "one"},
		{Name: "two", Needs: []string{"one"}},
	}}

	waves, err := citypes.WavesFor(citypes.Target{Alias: "a", In: []string{"two", "one"}}, in)
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"one"}, {"two"}}, waves,
		"the declaration decides, not the order they were listed in")
}

func TestWavesForNamesTheTargetWhenTheGraphIsBroken(t *testing.T) {
	t.Parallel()

	in := citypes.RunInput{Repos: []citypes.RepoCheckout{
		{Name: "one", Needs: []string{"two"}},
		{Name: "two", Needs: []string{"one"}},
	}}

	_, err := citypes.WavesFor(citypes.Target{Alias: "build-all", In: []string{"one", "two"}}, in)
	require.ErrorIs(t, err, citypes.ErrCycle)
	assert.Contains(t, err.Error(), "build-all")
}
