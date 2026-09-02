package gitadapter_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The three pattern shapes a factory writes, and the one thing each must
// not do: a root-anchored glob must not reach a nested file, a directory
// pattern must not match a sibling with the same prefix, and a bare glob
// reads against the base name so "**.md" is every markdown file.
func TestIgnoredMatchesTheThreePatternShapes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		path     string
		patterns []string
		want     bool
	}{
		{"README.md", []string{"/*.md"}, true},
		{"docs/guide.md", []string{"/*.md"}, false},
		{"docs/guide.md", []string{"docs/**"}, true},
		{"docs", []string{"docs/**"}, true},
		{"docs-site/index.html", []string{"docs/**"}, false},
		{"internal/notes.md", []string{"**.md"}, true},
		{"internal/notes.go", []string{"**.md"}, false},
		{"FOLLOWUP.md", []string{"FOLLOWUP.md"}, true},
		{"a/FOLLOWUP.md", []string{"FOLLOWUP.md"}, false},
		{"main.go", []string{"", "  "}, false},
	} {
		assert.Equal(t, tc.want, gitadapter.Ignored(tc.path, tc.patterns), "%s against %v", tc.path, tc.patterns)
	}
}

// The tree hash is the identity of what HEAD holds: a commit that changes
// only an ignored path hashes the same, a commit that changes anything else
// does not, and a reworded commit with the same files hashes the same.
func TestTreeHashIgnoresWhatItIsToldAndNothingElse(t *testing.T) {
	g, dir, _ := tagFixture(t)
	ctx := context.Background()
	exec := execadapter.New()

	commit := func(name, content, message string) {
		t.Helper()
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))

		for _, args := range [][]string{
			{"add", name},
			{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", message},
		} {
			res, err := exec.Run(ctx, dir, "git", args...)
			require.NoError(t, err)
			require.Zero(t, res.ExitCode, res.Stderr)
		}
	}

	before, err := g.TreeHash(ctx, dir, "/*.md")
	require.NoError(t, err)

	commit("README.md", "docs only", "docs: explain")

	afterDocs, err := g.TreeHash(ctx, dir, "/*.md")
	require.NoError(t, err)
	require.Equal(t, before, afterDocs, "a change under an ignored path is not a change")

	unfiltered, err := g.TreeHash(ctx, dir)
	require.NoError(t, err)
	require.NotEqual(t, before, unfiltered, "with nothing ignored the docs change counts")

	commit("main.go", "package main", "fix: code")

	afterCode, err := g.TreeHash(ctx, dir, "/*.md")
	require.NoError(t, err)
	require.NotEqual(t, afterDocs, afterCode, "a code change is a change whatever is ignored")
}
