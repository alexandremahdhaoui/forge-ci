package artifactcontroller_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactcontroller"
)

// upload is one built binary as the plan describes it: the fields the
// record carried, and the asset name composed from them.
func upload(path, name, os, arch string) artifactcontroller.Upload {
	return artifactcontroller.Upload{
		Path: path, Asset: artifactcontroller.AssetName(name, os, arch), Name: name, OS: os, Arch: arch,
	}
}

func TestBuildIndexGroupsUploadsByTheirRecordedPlatform(t *testing.T) {
	t.Parallel()

	raw, err := artifactcontroller.BuildIndex(
		"abc123def456", "2026-08-26T00:00:00Z",
		artifactcontroller.Release{Tag: "dist-abc123def456"},
		[]artifactcontroller.UploadDigest{
			{Upload: upload("/w/m/build/dist/alpha_linux_amd64", "alpha", "linux", "amd64"), Digest: "aa", Size: 10},
			{Upload: upload("/w/m/build/dist/alpha_linux_arm64", "alpha", "linux", "arm64"), Digest: "bb", Size: 11},
			{Upload: upload("/w/m/build/dist/beta-tool_linux_amd64", "beta-tool", "linux", "amd64"), Digest: "cc", Size: 12},
			// A glob asset names no tool and no platform: a plain asset.
			{Upload: artifactcontroller.Upload{Path: "/w/m/build/bin/plain", Asset: "plain"}, Digest: "dd", Size: 13},
		})
	require.NoError(t, err)

	var index artifactcontroller.Index
	require.NoError(t, json.Unmarshal(raw, &index))

	assert.Equal(t, "abc123def456", index.Revision)
	assert.Equal(t, "dist-abc123def456", index.Release.Tag)
	require.Len(t, index.Tools, 2)

	alpha := index.Tools[0]
	assert.Equal(t, "alpha", alpha.Name)
	// No per-tool version. The pipeline tags each member on its own line
	// and a binary's name says nothing about which member built it, so the
	// only version available here was the release's own - stamped alike on
	// tools tagged v0.44.4 and v0.1.5. A field that can only be filled in
	// wrongly is better absent.
	assert.Equal(t, "sha256:aa", alpha.Platforms["linux/amd64"].Digest)
	assert.Equal(t, "alpha_linux_amd64", alpha.Platforms["linux/amd64"].Asset)
	assert.Equal(t, "sha256:bb", alpha.Platforms["linux/arm64"].Digest)

	assert.Equal(t, "beta-tool", index.Tools[1].Name)
}

func TestBuildIndexRefusesAmbiguityAndClaims(t *testing.T) {
	t.Parallel()

	_, err := artifactcontroller.BuildIndex("abc", "",
		artifactcontroller.Release{},
		[]artifactcontroller.UploadDigest{
			{Upload: upload("x_linux_amd64", "x", "linux", "amd64"), Digest: "aa"},
			{Upload: upload("sub/x_linux_amd64", "x", "linux", "amd64"), Digest: "bb"},
		})
	require.ErrorContains(t, err, "two binaries for linux/amd64")

	_, err = artifactcontroller.BuildIndex("abc", "",
		artifactcontroller.Release{},
		[]artifactcontroller.UploadDigest{{Upload: upload("x_linux_amd64", "x", "linux", "amd64")}})
	require.ErrorContains(t, err, "no digest")

	_, err = artifactcontroller.BuildIndex("", "", artifactcontroller.Release{}, nil)
	require.ErrorIs(t, err, artifactcontroller.ErrRevision)
}
