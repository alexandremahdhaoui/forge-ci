package artifactdircontroller_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactdircontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/fsadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

func theDiskFailingAt(t *testing.T, failing string) *fsadaptermock.MockFS {
	t.Helper()

	disk := fsadapter.New()
	fs := fsadaptermock.NewMockFS(t)

	broken := func(method string) error {
		if method == failing {
			return errBoom
		}

		return nil
	}

	fs.EXPECT().IsDir(mock.Anything).RunAndReturn(func(path string) (bool, error) {
		if err := broken("IsDir"); err != nil {
			return false, err
		}

		return disk.IsDir(path)
	}).Maybe()
	fs.EXPECT().Exists(mock.Anything).RunAndReturn(func(path string) (bool, error) {
		if err := broken("Exists"); err != nil {
			return false, err
		}

		return disk.Exists(path)
	}).Maybe()
	fs.EXPECT().Walk(mock.Anything).RunAndReturn(func(dir string) ([]string, error) {
		if err := broken("Walk"); err != nil {
			return nil, err
		}

		return disk.Walk(dir)
	}).Maybe()
	fs.EXPECT().ReadFile(mock.Anything).RunAndReturn(func(path string) ([]byte, error) {
		if err := broken("ReadFile"); err != nil {
			return nil, err
		}

		return disk.ReadFile(path)
	}).Maybe()
	fs.EXPECT().MkdirAll(mock.Anything).RunAndReturn(func(path string) error {
		if err := broken("MkdirAll"); err != nil {
			return err
		}

		return disk.MkdirAll(path)
	}).Maybe()
	fs.EXPECT().WriteFile(mock.Anything, mock.Anything).RunAndReturn(func(path string, data []byte) error {
		if err := broken("WriteFile"); err != nil {
			return err
		}

		return disk.WriteFile(path, data)
	}).Maybe()
	fs.EXPECT().Mode(mock.Anything).RunAndReturn(func(path string) (os.FileMode, error) {
		if err := broken("Mode"); err != nil {
			return 0, err
		}

		return disk.Mode(path)
	}).Maybe()
	fs.EXPECT().Chmod(mock.Anything, mock.Anything).RunAndReturn(func(path string, mode os.FileMode) error {
		if err := broken("Chmod"); err != nil {
			return err
		}

		return disk.Chmod(path, mode)
	}).Maybe()

	return fs
}

func aRootHoldingAFileAndAnImageLayout(t *testing.T) (string, forge.Artifact, forge.Artifact) {
	t.Helper()

	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "member", "bin"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "member", "bin", "tool"), []byte("bytes"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "member", "img.oci"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "member", "img.oci", "index.json"), []byte("{}"), 0o600))

	return root,
		forge.Artifact{Name: "tool", Type: "binary", Location: "member/bin/tool"},
		forge.Artifact{Name: "img", Type: forge.TypeContainer, Location: "member/img.oci"}
}

func TestAFailureMovingAnArtifactNamesItsPathAndKeepsTheCause(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		get     bool
		tree    bool
		failing string
		want    string
	}{
		{name: "putting a file that cannot be inspected", failing: "IsDir", want: "putting member/bin/tool: boom"},
		{name: "putting a file that cannot be read", failing: "ReadFile", want: "putting member/bin/tool: boom"},
		{name: "putting a file whose directory cannot be made", failing: "MkdirAll", want: "putting member/bin/tool: boom"},
		{name: "putting a file that cannot be written", failing: "WriteFile", want: "putting member/bin/tool: boom"},
		{name: "putting a file whose mode cannot be read", failing: "Mode", want: "putting member/bin/tool: boom"},
		{name: "putting a file whose mode cannot be set", failing: "Chmod", want: "putting member/bin/tool: boom"},
		{name: "putting a tree that cannot be walked", tree: true, failing: "Walk", want: "putting member/img.oci: boom"},
		{name: "putting a tree whose file cannot be read", tree: true, failing: "ReadFile", want: "putting member/img.oci: boom"},
		{name: "getting a file whose copy cannot be found", get: true, failing: "Exists", want: "getting member/bin/tool: boom"},
		{name: "getting a file whose copy cannot be inspected", get: true, failing: "IsDir", want: "getting member/bin/tool: boom"},
		{name: "getting a file whose copy cannot be read", get: true, failing: "ReadFile", want: "getting member/bin/tool: boom"},
		{name: "getting a tree that cannot be walked", get: true, tree: true, failing: "Walk", want: "getting member/img.oci: boom"},
		{
			name: "getting a tree whose file cannot be read", get: true, tree: true, failing: "ReadFile",
			want: "getting member/img.oci/index.json: boom",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			root, file, tree := aRootHoldingAFileAndAnImageLayout(t)

			artifact := file
			if tc.tree {
				artifact = tree
			}

			put := citypes.ArtifactPutInput{Revision: "rev1", Root: root, Artifacts: []forge.Artifact{artifact}}

			if !tc.get {
				_, err := artifactdircontroller.New(theDiskFailingAt(t, tc.failing)).Put(put)
				require.ErrorIs(t, err, errBoom)
				require.EqualError(t, err, tc.want)

				return
			}

			kept, err := artifactdircontroller.New(fsadapter.New()).Put(put)
			require.NoError(t, err)

			_, err = artifactdircontroller.New(theDiskFailingAt(t, tc.failing)).Get(citypes.ArtifactGetInput{
				Revision: "rev1", Root: root, Artifacts: kept.Artifacts,
			})
			require.ErrorIs(t, err, errBoom)
			require.EqualError(t, err, tc.want)
		})
	}
}

func TestAGetWhoseCopyLandsWithDifferentBytesIsRefused(t *testing.T) {
	t.Parallel()

	root, file, _ := aRootHoldingAFileAndAnImageLayout(t)

	kept, err := artifactdircontroller.New(fsadapter.New()).Put(citypes.ArtifactPutInput{
		Revision: "rev1", Root: root, Artifacts: []forge.Artifact{file},
	})
	require.NoError(t, err)

	disk := fsadapter.New()
	corrupting := theDiskFailingAt(t, "")
	corrupting.EXPECT().WriteFile(mock.Anything, mock.Anything).Unset()
	corrupting.EXPECT().WriteFile(mock.Anything, mock.Anything).RunAndReturn(func(path string, data []byte) error {
		return disk.WriteFile(path, append(data, '!'))
	}).Once()

	_, err = artifactdircontroller.New(corrupting).Get(citypes.ArtifactGetInput{
		Revision: "rev1", Root: root, Artifacts: kept.Artifacts,
	})
	require.ErrorIs(t, err, artifactdircontroller.ErrArtifactSum)
	require.EqualError(t, err, "getting member/bin/tool: an artifact came back with different bytes")
}

func TestAnAbsolutePathOutsideTheRootPassesThroughUncopied(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "tool")
	require.NoError(t, os.WriteFile(outside, []byte("bytes"), 0o600))

	put, err := artifactdircontroller.New(fsadapter.New()).Put(citypes.ArtifactPutInput{
		Revision: "rev1", Root: root,
		Artifacts: []forge.Artifact{{Name: "tool", Type: "binary", Location: outside}},
	})
	require.NoError(t, err)
	require.Equal(t, outside, put.Artifacts[0].Location, "a path outside the root is somebody else's to serve")
	require.NoDirExists(t, filepath.Join(root, artifactdircontroller.ArtifactDir))
}

func TestPutThenGetRoundTripsAFileByRevision(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	rel := filepath.Join("member", "build", "dist", "tool_linux_amd64")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "member", "build", "dist"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte("bytes"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "member", "generated"), 0o750))

	ctrl := artifactdircontroller.New(fsadapter.New())

	put, err := ctrl.Put(citypes.ArtifactPutInput{
		Revision: "rev1", Root: root,
		Artifacts: []forge.Artifact{
			{Name: "tool", Type: "binary", Location: rel},
			{Name: "img", Type: "container", Location: "ghcr.io/x/img:v1"},
			{Name: "gen", Type: "generated", Location: "member/generated"},
		},
	})
	require.NoError(t, err)
	require.Len(t, put.Artifacts, 3)
	require.Equal(t, artifactdircontroller.ArtifactScheme+"rev1/member/build/dist/tool_linux_amd64", put.Artifacts[0].Location)
	require.Equal(t, "ghcr.io/x/img:v1", put.Artifacts[1].Location, "a URL is somebody else's to serve")
	require.Equal(t, "member/generated", put.Artifacts[2].Location, "a directory stays where it was built")
	require.FileExists(t, filepath.Join(root, artifactdircontroller.ArtifactDir, "rev1", rel))

	require.NoError(t, os.Remove(filepath.Join(root, rel)))

	got, err := ctrl.Get(citypes.ArtifactGetInput{
		Revision: "rev1", Root: root, Artifacts: put.Artifacts,
	})
	require.NoError(t, err)
	require.Equal(t, "member/build/dist/tool_linux_amd64", got.Artifacts[0].Location)
	require.Equal(t, "ghcr.io/x/img:v1", got.Artifacts[1].Location)

	data, err := os.ReadFile(filepath.Join(root, rel))
	require.NoError(t, err)
	require.Equal(t, "bytes", string(data))
}

func TestAnImageLayoutTravelsAsATree(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	layout := filepath.Join("member", "build", "images", "tool.oci")
	require.NoError(t, os.MkdirAll(filepath.Join(root, layout, "blobs", "sha256"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, layout, "index.json"), []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, layout, "blobs", "sha256", "abc"), []byte("layer"), 0o600))

	ctrl := artifactdircontroller.New(fsadapter.New())

	put, err := ctrl.Put(citypes.ArtifactPutInput{
		Revision: "rev1", Root: root,
		Artifacts: []forge.Artifact{{Name: "tool", Type: "container", Location: "file://" + filepath.Join(root, layout)}},
	})
	require.NoError(t, err)
	require.Equal(t, artifactdircontroller.ArtifactScheme+"rev1/member/build/images/tool.oci", put.Artifacts[0].Location)
	require.FileExists(t, filepath.Join(root, artifactdircontroller.ArtifactDir, "rev1", layout, "blobs", "sha256", "abc"))

	require.NoError(t, os.RemoveAll(filepath.Join(root, layout)))

	got, err := ctrl.Get(citypes.ArtifactGetInput{Revision: "rev1", Root: root, Artifacts: put.Artifacts})
	require.NoError(t, err)
	require.Equal(t, "file://"+filepath.Join(root, layout), got.Artifacts[0].Location)

	data, err := os.ReadFile(filepath.Join(root, layout, "blobs", "sha256", "abc"))
	require.NoError(t, err)
	require.Equal(t, "layer", string(data))
}

func TestGetRefusesALocationNoPutAnswered(t *testing.T) {
	t.Parallel()

	ctrl := artifactdircontroller.New(fsadapter.New())

	_, err := ctrl.Get(citypes.ArtifactGetInput{
		Revision: "rev1", Root: t.TempDir(),
		Artifacts: []forge.Artifact{{Name: "x", Location: artifactdircontroller.ArtifactScheme + "rev1"}},
	})
	require.ErrorIs(t, err, artifactdircontroller.ErrArtifactURL)

	put, err := ctrl.Put(citypes.ArtifactPutInput{
		Revision: "rev1", Root: t.TempDir(),
		Artifacts: []forge.Artifact{{Name: "escape", Type: "binary", Location: "../outside_linux_amd64"}},
	})
	require.NoError(t, err)
	require.Equal(t, "../outside_linux_amd64", put.Artifacts[0].Location, "a path outside the root passes through, never copied")
}

func TestPutNeedsARevision(t *testing.T) {
	t.Parallel()

	_, err := artifactdircontroller.New(fsadapter.New()).Put(citypes.ArtifactPutInput{Root: t.TempDir()})
	require.Error(t, err)
}

func TestGetSkipsWhatThisRunNeverPut(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctrl := artifactdircontroller.New(fsadapter.New())

	require.NoError(t, os.MkdirAll(filepath.Join(root, "member", "build", "bin"), 0o750))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "member", "build", "bin", "here_linux_amd64"), []byte("real"), 0o600))

	put, err := ctrl.Put(citypes.ArtifactPutInput{
		Revision: "rev1", Root: root,
		Artifacts: []forge.Artifact{
			{Name: "here", Type: "binary", Location: "member/build/bin/here_linux_amd64"},
		},
	})
	require.NoError(t, err)
	require.Len(t, put.Artifacts, 1)

	absent := forge.Artifact{
		Name:     "server",
		Type:     "binary",
		Location: artifactdircontroller.ArtifactScheme + "rev1/member/build/bin/server_linux_amd64",
	}

	got, err := ctrl.Get(citypes.ArtifactGetInput{
		Revision: "rev1", Root: root,
		Artifacts: append(put.Artifacts, absent),
	})
	require.NoError(t, err, "a carry-forward never fails on bytes this run did not put")
	require.Len(t, got.Artifacts, 1, "the absent one is skipped, the present one arrives")
	require.Equal(t, "member/build/bin/here_linux_amd64", got.Artifacts[0].Location)
}

func TestPutThenGetKeepsTheExecutableBit(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctrl := artifactdircontroller.New(fsadapter.New())
	rel := filepath.Join("member", "build", "bin", "server")

	require.NoError(t, os.MkdirAll(filepath.Join(root, "member", "build", "bin"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte("#!/bin/sh\ntrue\n"), 0o755))

	put, err := ctrl.Put(citypes.ArtifactPutInput{
		Revision:  "rev1",
		Root:      root,
		Artifacts: []forge.Artifact{{Name: "server", Type: "binary", Location: rel}},
	})
	require.NoError(t, err)

	kept, err := os.Stat(filepath.Join(root, artifactdircontroller.ArtifactDir, "rev1", rel))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), kept.Mode().Perm(), "the copy put keeps is still executable")

	require.NoError(t, os.Remove(filepath.Join(root, rel)))

	_, err = ctrl.Get(citypes.ArtifactGetInput{Revision: "rev1", Root: root, Artifacts: put.Artifacts})
	require.NoError(t, err)

	back, err := os.Stat(filepath.Join(root, rel))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o755), back.Mode().Perm(), "and so is the one get brings back")
}
