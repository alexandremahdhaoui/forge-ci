package artifactcontroller_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactcontroller"
)

func TestBuildIndexGroupsPlatformSuffixedUploads(t *testing.T) {
	t.Parallel()

	raw, err := artifactcontroller.BuildIndex(
		"abc123def456", "v0.2.0", "2026-08-26T00:00:00Z",
		artifactcontroller.Release{Tag: "dist-abc123def456"},
		[]artifactcontroller.UploadDigest{
			{Path: "/w/m/build/dist/alpha_linux_amd64", Digest: "aa", Size: 10},
			{Path: "/w/m/build/dist/alpha_linux_arm64", Digest: "bb", Size: 11},
			{Path: "/w/m/build/dist/beta-tool_linux_amd64", Digest: "cc", Size: 12},
			{Path: "/w/m/build/bin/plain", Digest: "dd", Size: 13}, // no suffix: plain asset
		})
	require.NoError(t, err)

	var index artifactcontroller.Index
	require.NoError(t, json.Unmarshal(raw, &index))

	assert.Equal(t, "abc123def456", index.Revision)
	assert.Equal(t, "dist-abc123def456", index.Release.Tag)
	require.Len(t, index.Tools, 2)

	alpha := index.Tools[0]
	assert.Equal(t, "alpha", alpha.Name)
	assert.Equal(t, "v0.2.0", alpha.Version)
	assert.Equal(t, "sha256:aa", alpha.Platforms["linux/amd64"].Digest)
	assert.Equal(t, "alpha_linux_amd64", alpha.Platforms["linux/amd64"].Asset)
	assert.Equal(t, "sha256:bb", alpha.Platforms["linux/arm64"].Digest)

	assert.Equal(t, "beta-tool", index.Tools[1].Name)
}

func TestBuildIndexRefusesAmbiguityAndClaims(t *testing.T) {
	t.Parallel()

	_, err := artifactcontroller.BuildIndex("abc", "v0.1.0", "",
		artifactcontroller.Release{},
		[]artifactcontroller.UploadDigest{
			{Path: "x_linux_amd64", Digest: "aa"},
			{Path: "sub/x_linux_amd64", Digest: "bb"},
		})
	require.ErrorContains(t, err, "two binaries for linux/amd64")

	_, err = artifactcontroller.BuildIndex("abc", "v0.1.0", "",
		artifactcontroller.Release{},
		[]artifactcontroller.UploadDigest{{Path: "x_linux_amd64"}})
	require.ErrorContains(t, err, "no digest")

	_, err = artifactcontroller.BuildIndex("", "v0.1.0", "", artifactcontroller.Release{}, nil)
	require.ErrorIs(t, err, artifactcontroller.ErrRevision)
}
