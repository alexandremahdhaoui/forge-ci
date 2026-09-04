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

// An image layout is a tree the registry push reads, so it travels: put
// keeps every file under it and get brings the tree back, verified, at the
// absolute file URL the layout reader expects.
func TestAnImageLayoutTravelsAsATree(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	layout := filepath.Join("member", "build", "images", "tool.oci")
	require.NoError(t, os.MkdirAll(filepath.Join(root, layout, "blobs", "sha256"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, layout, "index.json"), []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, layout, "blobs", "sha256", "abc"), []byte("layer"), 0o600))

	ctrl := computecontroller.New(nil, nil, nil)

	put, err := ctrl.Put(fsadapter.New(), citypes.ArtifactPutInput{
		Revision: "rev1", Root: root,
		Artifacts: []forge.Artifact{{Name: "tool", Type: "container", Location: "file://" + filepath.Join(root, layout)}},
	})
	require.NoError(t, err)
	require.Equal(t, computecontroller.ArtifactScheme+"rev1/member/build/images/tool.oci", put.Artifacts[0].Location)
	require.FileExists(t, filepath.Join(root, computecontroller.ArtifactDir, "rev1", layout, "blobs", "sha256", "abc"))

	require.NoError(t, os.RemoveAll(filepath.Join(root, layout)))

	got, err := ctrl.Get(fsadapter.New(), citypes.ArtifactGetInput{Revision: "rev1", Root: root, Artifacts: put.Artifacts})
	require.NoError(t, err)
	require.Equal(t, "file://"+filepath.Join(root, layout), got.Artifacts[0].Location)

	data, err := os.ReadFile(filepath.Join(root, layout, "blobs", "sha256", "abc"))
	require.NoError(t, err)
	require.Equal(t, "layer", string(data))
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

// A record outlives the run that wrote it, and the transport does not. A
// substage that already passed for this revision is read from its record
// instead of run again, so a later run inherits the artifact NAMES without
// the bytes: nothing in that run built or uploaded them.
//
// A live pipeline died on exactly this. Seven member pushes made seven runs
// of one revision; the last reused the first's test record and looked for a
// binary among artifacts its own run had never put.
//
// Carrying forward is best effort, so the absent one is skipped and the
// present one still arrives. The gate is at the point of use: a release
// digests every asset it plans to upload and names the one it cannot find.
func TestGetSkipsWhatThisRunNeverPut(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fs := fsadapter.New()
	ctrl := computecontroller.New(nil, nil, nil)

	require.NoError(t, os.MkdirAll(filepath.Join(root, "member", "build", "bin"), 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "member", "build", "bin", "here_linux_amd64"), []byte("real"), 0o600))

	put, err := ctrl.Put(fs, citypes.ArtifactPutInput{
		Revision: "rev1", Root: root,
		Artifacts: []forge.Artifact{
			{Name: "here", Type: "binary", Location: "member/build/bin/here_linux_amd64"},
		},
	})
	require.NoError(t, err)
	require.Len(t, put.Artifacts, 1)

	// The run record also names what an earlier run built. Same revision,
	// same location shape, no bytes in this run.
	absent := forge.Artifact{
		Name:     "server",
		Type:     "binary",
		Location: computecontroller.ArtifactScheme + "rev1/member/build/bin/server_linux_amd64",
	}

	got, err := ctrl.Get(fs, citypes.ArtifactGetInput{
		Revision: "rev1", Root: root,
		Artifacts: append(put.Artifacts, absent),
	})
	require.NoError(t, err, "a carry-forward never fails on bytes this run did not put")
	require.Len(t, got.Artifacts, 1, "the absent one is skipped, the present one arrives")
	require.Equal(t, "member/build/bin/here_linux_amd64", got.Artifacts[0].Location)
}

// The mode travels with the bytes. A put used to write every file 0o600,
// so a binary a later stage had to RUN arrived unexecutable and that stage
// died on "permission denied" - a message about the file, never about the
// build that made it. The canary that starts four servers is what found it,
// because it is the first thing that executes something it was handed.
func TestPutThenGetKeepsTheExecutableBit(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fs := fsadapter.New()
	ctrl := computecontroller.New(nil, nil, nil)
	rel := filepath.Join("member", "build", "bin", "server")

	require.NoError(t, os.MkdirAll(filepath.Join(root, "member", "build", "bin"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte("#!/bin/sh\ntrue\n"), 0o755))

	put, err := ctrl.Put(fs, citypes.ArtifactPutInput{
		Revision:  "rev1",
		Root:      root,
		Artifacts: []forge.Artifact{{Name: "server", Type: "binary", Location: rel}},
	})
	require.NoError(t, err)

	kept, err := os.Stat(filepath.Join(root, computecontroller.ArtifactDir, "rev1", rel))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), kept.Mode().Perm(), "the copy put keeps is still executable")

	// The runner that built it is gone.
	require.NoError(t, os.Remove(filepath.Join(root, rel)))

	_, err = ctrl.Get(fs, citypes.ArtifactGetInput{Revision: "rev1", Root: root, Artifacts: put.Artifacts})
	require.NoError(t, err)

	back, err := os.Stat(filepath.Join(root, rel))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), back.Mode().Perm(), "and so is the one get brings back")
}
