package reconcilecontroller

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The factory's manifest maps to per-repo exclusions: each entry loses its
// repo prefix, root-level and dot-directory entries are dropped, and an
// absent or broken manifest excludes nothing.
func TestGeneratedExclusionsGroupByRepo(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".forge"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".forge", "factory-generated.json"),
		[]byte(`{"version":1,"files":["member-a/manifest.file","member-a/closure.file","member-b/manifest.file","root.file",".forge/dependency-locks.json"]}`), 0o600))

	got := generatedExclusions(fsadapter.New(), root)

	assert.Equal(t, map[string][]string{
		"member-a": {"manifest.file", "closure.file"},
		"member-b": {"manifest.file"},
	}, got)
}

func TestAnAbsentGeneratedManifestExcludesNothing(t *testing.T) {
	t.Parallel()

	assert.Empty(t, generatedExclusions(fsadapter.New(), t.TempDir()))
}

func TestABrokenGeneratedManifestExcludesNothing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".forge"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".forge", "factory-generated.json"),
		[]byte("not json"), 0o600))

	assert.Empty(t, generatedExclusions(fsadapter.New(), root))
}
