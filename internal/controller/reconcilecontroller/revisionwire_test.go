package reconcilecontroller

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/gitadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestAMintedRevisionTravelsAsTheSharedContract(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)

	wire := toWire(citypes.Revision{
		ID:        "abc123-dirty",
		CreatedAt: at,
		Repos:     map[string]string{"golden-go": "9e54bb29"},
		Dirty:     []string{"golden-go"},
	})

	raw, err := json.Marshal(wire)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))

	assert.Equal(t, "abc123-dirty", got["id"])
	assert.Equal(t, "2026-08-19T10:00:00Z", got["createdAt"])
	assert.Equal(t, map[string]any{"golden-go": "9e54bb29"}, got["repos"])
	assert.Equal(t, []any{"golden-go"}, got["dirty"])
}

func TestAnEmptyRevisionOmitsWhatItDoesNotHave(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(toWire(citypes.Revision{ID: "abc", CreatedAt: time.Unix(0, 0).UTC()}))
	require.NoError(t, err)

	assert.NotContains(t, string(raw), "null",
		"a nil map or slice travels as null and a reader must not have to handle it")
}

// The semantic rules live in forge-revision-spec's validator and are asserted
// there against fixtures. Nothing checked that what forge-ci actually mints
// obeys them, which is the half that matters.
func semanticErrors(t *testing.T, revision citypes.Revision) []string {
	t.Helper()

	var out []string

	for _, name := range revision.Dirty {
		if _, ok := revision.Repos[name]; !ok {
			out = append(out, "dirty names a repo the revision does not hold")

			break
		}
	}

	for i := 1; i < len(revision.Dirty); i++ {
		if revision.Dirty[i-1] > revision.Dirty[i] {
			out = append(out, "dirty is not sorted")

			break
		}
	}

	seen := map[string]bool{}
	for _, name := range revision.Dirty {
		if seen[name] {
			out = append(out, "dirty repeats a repo")

			break
		}

		seen[name] = true
	}

	suffixed := len(revision.ID) > 6 && revision.ID[len(revision.ID)-6:] == "-dirty"

	if len(revision.Dirty) > 0 && !suffixed {
		out = append(out, "a revision with dirty repos must carry the -dirty suffix")
	}

	if len(revision.Dirty) == 0 && suffixed {
		out = append(out, "a revision with no dirty repos must not carry the -dirty suffix")
	}

	return out
}

func TestSemanticErrorsCatchesEachRule(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		revision citypes.Revision
		want     string
	}{
		"unknown repo": {
			citypes.Revision{ID: "a-dirty", Repos: map[string]string{"x": "1"}, Dirty: []string{"y"}},
			"dirty names a repo the revision does not hold",
		},
		"unsorted": {
			citypes.Revision{
				ID:    "a-dirty",
				Repos: map[string]string{"a": "1", "b": "2"},
				Dirty: []string{"b", "a"},
			},
			"dirty is not sorted",
		},
		"duplicate": {
			citypes.Revision{ID: "a-dirty", Repos: map[string]string{"a": "1"}, Dirty: []string{"a", "a"}},
			"dirty repeats a repo",
		},
		"missing suffix": {
			citypes.Revision{ID: "a", Repos: map[string]string{"a": "1"}, Dirty: []string{"a"}},
			"a revision with dirty repos must carry the -dirty suffix",
		},
		"spurious suffix": {
			citypes.Revision{ID: "a-dirty", Repos: map[string]string{"a": "1"}},
			"a revision with no dirty repos must not carry the -dirty suffix",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Contains(t, semanticErrors(t, tc.revision), tc.want)
		})
	}
}

// TestWhatIsActuallyMintedSatisfiesTheContract closes the loop. The rules were
// asserted against fixtures in forge-revision-spec and against the helper
// above, and neither of those runs the code that mints a revision for real.
func TestWhatIsActuallyMintedSatisfiesTheContract(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		dirty map[string]bool
	}{
		"every repo clean": {map[string]bool{}},
		"one repo dirty":   {map[string]bool{"alpha": true}},
		"every repo dirty": {map[string]bool{"zeta": true, "alpha": true}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			git := gitadaptermock.NewMockGit(t)
			git.EXPECT().IsRepo(mock.Anything, mock.Anything).Return(true, nil).Maybe()
			git.EXPECT().HeadSHA(mock.Anything, mock.Anything).
				RunAndReturn(func(_ context.Context, dir string) (string, error) {
					return "sha-" + filepath.Base(dir), nil
				}).Maybe()
			git.EXPECT().WorktreeHash(mock.Anything, mock.Anything).
				RunAndReturn(func(_ context.Context, dir string) (string, error) {
					if tc.dirty[filepath.Base(dir)] {
						return "worktree-hash", nil
					}

					return "", nil
				}).Maybe()

			c := New(nil, git, func() time.Time { return time.Unix(0, 0).UTC() })

			// Declared out of order on purpose. Minting appends dirty in
			// declaration order, so a sorted list here would prove nothing.
			p := config.Pipeline{Name: "demo", Repos: []config.Repo{
				{Name: "zeta", URL: "u"},
				{Name: "alpha", URL: "u"},
			}}

			revision, err := c.resolveRevision(t.Context(), p, "/w")
			require.NoError(t, err)

			assert.Empty(t, semanticErrors(t, revision),
				"a revision the pipeline mints must satisfy the shared contract")
		})
	}
}
