package computecontroller_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/computecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
	"github.com/stretchr/testify/require"
)

// A put hands a run's files to the engine's own place and answers
// locations the engine serves again; a get brings them back to the same
// paths with the same bytes. Anything that is not a file under the root -
// an image reference, a generated tree - passes through untouched, because
// it is somebody else's to serve or stays where it was built.
func TestPutThenGetRoundTripsAFileByRevision(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	rel := filepath.Join("member", "build", "dist", "tool_linux_amd64")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "member", "build", "dist"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte("bytes"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "member", "generated"), 0o750))

	ctrl := computecontroller.New(nil, nil, nil)

	put, err := ctrl.Put(fsadapter.New(), citypes.ArtifactPutInput{
		Revision: "rev1", Root: root,
		Artifacts: []forge.Artifact{
			{Name: "tool", Type: "binary", Location: rel},
			{Name: "img", Type: "container", Location: "ghcr.io/x/img:v1"},
			{Name: "gen", Type: "generated", Location: "member/generated"},
		},
	})
	require.NoError(t, err)
	require.Len(t, put.Artifacts, 3)
	require.Equal(t, computecontroller.ArtifactScheme+"rev1/member/build/dist/tool_linux_amd64", put.Artifacts[0].Location)
	require.Equal(t, "ghcr.io/x/img:v1", put.Artifacts[1].Location, "a URL is somebody else's to serve")
	require.Equal(t, "member/generated", put.Artifacts[2].Location, "a directory stays where it was built")
	require.FileExists(t, filepath.Join(root, computecontroller.ArtifactDir, "rev1", rel))

	// The runner that built the file is gone: the path is empty and only
	// the engine's place has the bytes.
	require.NoError(t, os.Remove(filepath.Join(root, rel)))

	got, err := ctrl.Get(fsadapter.New(), citypes.ArtifactGetInput{
		Revision: "rev1", Root: root, Artifacts: put.Artifacts,
	})
	require.NoError(t, err)
	require.Equal(t, "member/build/dist/tool_linux_amd64", got.Artifacts[0].Location)
	require.Equal(t, "ghcr.io/x/img:v1", got.Artifacts[1].Location)

	data, err := os.ReadFile(filepath.Join(root, rel))
	require.NoError(t, err)
	require.Equal(t, "bytes", string(data))
}

// A location this engine never answered is refused rather than guessed
// at, and a path that escapes the root is never copied.
func TestGetRefusesALocationNoPutAnswered(t *testing.T) {
	t.Parallel()

	ctrl := computecontroller.New(nil, nil, nil)

	_, err := ctrl.Get(fsadapter.New(), citypes.ArtifactGetInput{
		Revision: "rev1", Root: t.TempDir(),
		Artifacts: []forge.Artifact{{Name: "x", Location: computecontroller.ArtifactScheme + "rev1"}},
	})
	require.ErrorIs(t, err, computecontroller.ErrArtifactURL)

	put, err := ctrl.Put(fsadapter.New(), citypes.ArtifactPutInput{
		Revision: "rev1", Root: t.TempDir(),
		Artifacts: []forge.Artifact{{Name: "escape", Type: "binary", Location: "../outside_linux_amd64"}},
	})
	require.NoError(t, err)
	require.Equal(t, "../outside_linux_amd64", put.Artifacts[0].Location, "a path outside the root passes through, never copied")
}

func TestPutNeedsARevision(t *testing.T) {
	t.Parallel()

	_, err := computecontroller.New(nil, nil, nil).Put(fsadapter.New(), citypes.ArtifactPutInput{Root: t.TempDir()})
	require.Error(t, err)
}
