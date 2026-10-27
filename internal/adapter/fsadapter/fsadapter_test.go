package fsadapter_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/stretchr/testify/require"
)

func TestWriteCreatesTheParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "nested", "f.txt")

	require.NoError(t, fsadapter.New().WriteFile(path, []byte("hi")))

	body, err := fsadapter.New().ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "hi", string(body))
}

func TestModeReturnsThePermissionBitsAndNoFileTypeBits(t *testing.T) {
	dir := t.TempDir()

	file := filepath.Join(dir, "talosconfig")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))
	require.NoError(t, os.Chmod(file, 0o600))

	mode, err := fsadapter.New().Mode(file)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), mode)

	sub := filepath.Join(dir, "recovery")
	require.NoError(t, os.Mkdir(sub, 0o700))
	require.NoError(t, os.Chmod(sub, 0o700))

	mode, err = fsadapter.New().Mode(sub)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), mode, "a directory answers 0700 without its directory bit")
}

func TestModeOfAMissingFileNamesIt(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")

	_, err := fsadapter.New().Mode(missing)
	require.Error(t, err)
	require.Contains(t, err.Error(), missing)
}

func TestReadingAMissingFileNamesIt(t *testing.T) {
	_, err := fsadapter.New().ReadFile("/nope/f.txt")
	require.Error(t, err)
	require.Contains(t, err.Error(), "reading /nope/f.txt")
}

func TestExistsIsTrueFalseAndErrorInTheRightCases(t *testing.T) {
	fs := fsadapter.New()
	dir := t.TempDir()

	ok, err := fs.Exists(dir)
	require.NoError(t, err)
	require.True(t, ok)

	ok, err = fs.Exists(filepath.Join(dir, "missing"))
	require.NoError(t, err)
	require.False(t, ok)

	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))

	_, err = fs.Exists(filepath.Join(blocker, "child", "grandchild"))
	require.Error(t, err)
}

func TestListIsSortedAndEmptyForAMissingDirectory(t *testing.T) {
	fs := fsadapter.New()
	dir := t.TempDir()

	for _, name := range []string{"c", "a", "b"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o600))
	}

	names, err := fs.List(dir)
	require.NoError(t, err)
	require.Equal(t, []string{"a", "b", "c"}, names)

	names, err = fs.List(filepath.Join(dir, "missing"))
	require.NoError(t, err)
	require.Empty(t, names)
}

func TestListReportsARealFailure(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "f")
	require.NoError(t, os.WriteFile(file, nil, 0o600))

	_, err := fsadapter.New().List(file)
	require.Error(t, err)
	require.Contains(t, err.Error(), "listing "+file)
}

func TestMkdirAllAndRemove(t *testing.T) {
	fs := fsadapter.New()
	dir := filepath.Join(t.TempDir(), "a", "b")

	require.NoError(t, fs.MkdirAll(dir))
	require.DirExists(t, dir)

	require.NoError(t, fs.Remove(dir))
	require.NoDirExists(t, dir)
}

func TestMkdirAllNamesWhatItCouldNotCreate(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))

	err := fsadapter.New().MkdirAll(filepath.Join(blocker, "child"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "creating ")
}

func TestWriteNamesWhatItCouldNotWrite(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))

	err := fsadapter.New().WriteFile(filepath.Join(blocker, "child", "f.txt"), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "creating directory for ")
}

func TestWalkReturnsRelativePathsRecursively(t *testing.T) {
	dir := t.TempDir()
	fs := fsadapter.New()
	require.NoError(t, fs.WriteFile(dir+"/a/b/one.json", []byte("{}")))
	require.NoError(t, fs.WriteFile(dir+"/a/two.json", []byte("{}")))

	names, err := fs.Walk(dir)
	require.NoError(t, err)
	require.Equal(t, []string{"a/b/one.json", "a/two.json"}, names)
}

func TestWalkOfAMissingDirectoryIsEmpty(t *testing.T) {
	names, err := fsadapter.New().Walk(t.TempDir() + "/absent")
	require.NoError(t, err)
	require.Empty(t, names)
}
