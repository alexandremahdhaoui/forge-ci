package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactcontroller"
	"github.com/stretchr/testify/require"
)

// The release runs gh in <root>/<releaseIn>, one level below the root the
// asset paths are joined from. While the root is absolute both agree. The
// moment it is relative they mean different places, and the release fails on
// "no matches found" for a file that is sitting on disk - at the last step,
// after the build and publish stages passed and the tags are already cut.
//
// `forge-ci apply --root .` is the form the operator runbook prints, so this
// was reachable by following the documentation.
func TestAssetsResolveAbsolutelyWhateverTheRootLooksLike(t *testing.T) {
	root := t.TempDir()

	member := filepath.Join(root, "forge-ci", "build", "bin")
	require.NoError(t, os.MkdirAll(member, 0o750))

	binary := filepath.Join(member, "ci-artifact-release_linux_amd64")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o600))

	// The release directory a sibling of the member, which is the shape
	// that makes a relative join resolve into the wrong tree.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "forge-self-factory"), 0o750))

	upload := filepath.Join("forge-ci", "build", "bin", "ci-artifact-release_linux_amd64")
	plan := artifactcontroller.Plan{Version: "v0.1.1", TagName: "v0.1.1"}

	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))

	t.Cleanup(func() { _ = os.Chdir(cwd) })

	for name, given := range map[string]string{
		"an absolute root": root,
		"a dot root":       ".",
		"a trailing slash": root + "/",
	} {
		t.Run(name, func(t *testing.T) {
			assets, err := stageAssets(given, plan, []string{upload}, "abc")
			require.NoError(t, err)
			require.NotEmpty(t, assets)

			for _, a := range assets {
				require.True(t, filepath.IsAbs(a),
					"gh runs one directory down, so %q resolves against the wrong tree", a)
				require.FileExists(t, a)
			}
		})
	}
}

func TestAnAbsoluteUploadIgnoresTheRoot(t *testing.T) {
	// An artifact record may already carry an absolute path. The root says
	// nothing about it and must not be prepended.
	dir := t.TempDir()
	binary := filepath.Join(dir, "already-absolute")
	require.NoError(t, os.WriteFile(binary, []byte("x"), 0o600))

	require.Equal(t, binary, resolveUpload("/somewhere/else", binary))
}
