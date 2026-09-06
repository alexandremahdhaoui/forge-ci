package artifactcontroller_test

import (
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactcontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlanTagsEveryRepoTheRevisionNames(t *testing.T) {
	t.Parallel()

	plan, err := artifactcontroller.New().Plan(citypes.ArtifactInput{
		Revision: "3dd48e96ed7e",
		Version:  "v0.2.0",
		Repos:    map[string]string{"golden-rust": "a5b26c6a", "golden-go": "9e54bb29"},
	})
	require.NoError(t, err)

	assert.Equal(t, "v0.2.0", plan.Version)
	assert.Equal(t, []artifactcontroller.Tag{
		{Repo: "golden-go", SHA: "9e54bb29"},
		{Repo: "golden-rust", SHA: "a5b26c6a"},
	}, plan.Tags, "sorted, so a release does the same thing twice")
}

func TestPlanRefusesADirtyRevision(t *testing.T) {
	t.Parallel()

	_, err := artifactcontroller.New().Plan(citypes.ArtifactInput{
		Revision: "3dd48e96ed7e-dirty",
		Version:  "v0.2.0",
	})
	require.ErrorIs(t, err, artifactcontroller.ErrDirty)
}

func TestPlanNeedsARevision(t *testing.T) {
	t.Parallel()

	_, err := artifactcontroller.New().Plan(citypes.ArtifactInput{Version: "v0.2.0"})
	require.ErrorIs(t, err, artifactcontroller.ErrRevision)
}

func TestPlanRefusesAVersionNobodyCanPin(t *testing.T) {
	t.Parallel()

	for _, version := range []string{"", "0.2.0", "v0.2", "latest", "v01.2.0", "v0.2.0.1"} {
		_, err := artifactcontroller.New().Plan(citypes.ArtifactInput{
			Revision: "abc123",
			Version:  version,
		})
		require.ErrorIs(t, err, artifactcontroller.ErrVersion, version)
	}
}

func TestPlanAcceptsAPrerelease(t *testing.T) {
	t.Parallel()

	plan, err := artifactcontroller.New().Plan(citypes.ArtifactInput{
		Revision: "abc123",
		Version:  "v1.0.0-rc.1",
	})
	require.NoError(t, err)
	assert.Equal(t, "v1.0.0-rc.1", plan.Version)
}

// Only what travels is published, and what travels is read from the
// record's fields: a binary built for a platform. The asset name is composed
// from those fields, never parsed from the file name, so a binary whose file
// carries no suffix still travels under one. A record with no platform is
// one an older forge wrote and says nothing about where it runs, so it stays
// home; so does an image, a generated file and a URL.
func TestPlanUploadsOnlyWhatTravels(t *testing.T) {
	t.Parallel()

	plan, err := artifactcontroller.New().Plan(citypes.ArtifactInput{
		Revision: "abc123",
		Version:  "v0.2.0",
		Artifacts: []forge.Artifact{
			{Name: "cli", Type: "binary", OS: "linux", Arch: "arm64", Location: "file:///out/cli"},
			{Name: "rel", Type: "binary", OS: "linux", Arch: "amd64", Location: "member/build/dist/rel_linux_amd64"},
			{Name: "old", Type: "binary", Location: "member/build/bin/old"},
			{Name: "img", Type: "container", Location: "member/build/images/img"},
			{Name: "url", Type: "binary", OS: "linux", Arch: "amd64", Location: "https://x/url"},
			{Name: "empty", Type: "binary", OS: "linux", Arch: "amd64", Location: ""},
			{Name: "gen", Type: "generated", Location: "member/zz_generated.go"},
			{Name: "rel", Type: "binary", OS: "linux", Arch: "amd64", Location: "member/build/dist/rel_linux_amd64"},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, []artifactcontroller.Upload{
		{Path: "/out/cli", Asset: "cli_linux_arm64", Name: "cli", OS: "linux", Arch: "arm64"},
		{Path: "member/build/dist/rel_linux_amd64", Asset: "rel_linux_amd64", Name: "rel", OS: "linux", Arch: "amd64"},
	}, plan.Uploads,
		"a binary with a platform uploads once under a composed name; nothing else travels")
}

// The aggregated release carries the version, not the revision. Everything
// built together is released together, under the one number every member is
// tagged with, so a consumer who knows one member's version knows all of them.
func TestPlanReleasesUnderTheVersionAndNotTheRevision(t *testing.T) {
	t.Parallel()

	plan, err := artifactcontroller.New().Plan(citypes.ArtifactInput{
		Revision: "abc123def456",
		Version:  "v0.2.0",
	})
	require.NoError(t, err)
	assert.Equal(t, "v0.2.0", plan.TagName)
	assert.NotContains(t, plan.TagName, "dist-",
		"a dist prefix was a second name for one release, and nobody could pin it")
}

// A prefix is a namespace for the case where one repo is released by more
// than one factory. It is empty by default, so a factory that never meets
// that case sees plain semver.
func TestPlanPrefixesTheTagWhenTheFactoryNamesOne(t *testing.T) {
	t.Parallel()

	plan, err := artifactcontroller.New().Plan(citypes.ArtifactInput{
		Revision:  "abc123def456",
		Version:   "v0.2.0",
		TagPrefix: "forge",
	})
	require.NoError(t, err)
	assert.Equal(t, "forge-v0.2.0", plan.TagName)
	assert.Equal(t, "v0.2.0", plan.Version, "the version is the version; the prefix is only in the tag")
}

func TestDeclareOwnsNothing(t *testing.T) {
	t.Parallel()

	out, err := artifactcontroller.New().Declare(nil)
	require.NoError(t, err)
	assert.Empty(t, out.Resources, "a release writes to someone else's system")
}

// Two repos that each build a command of the same name would upload one
// asset over the other. The release refuses and names both sources, so the
// fix is obvious: rename one, or stop declaring it public. Live case:
// forge-ci and forge-factory each ship their own cmd/docgen.
func TestTwoArtifactsClaimingOneAssetNameAreRefused(t *testing.T) {
	t.Parallel()

	_, err := artifactcontroller.New().Plan(citypes.ArtifactInput{
		Revision: "abc123", Version: "v0.2.0",
		Artifacts: []forge.Artifact{
			{Name: "docgen", Type: "binary", OS: "linux", Arch: "amd64", Location: "forge-ci/build/dist/docgen_linux_amd64"},
			{Name: "docgen", Type: "binary", OS: "linux", Arch: "amd64", Location: "forge-factory/build/dist/docgen_linux_amd64"},
		},
	})
	require.ErrorIs(t, err, artifactcontroller.ErrCollision)
	require.ErrorContains(t, err, "docgen_linux_amd64")
	require.ErrorContains(t, err, "forge-ci/build/dist/docgen_linux_amd64")
	require.ErrorContains(t, err, "forge-factory/build/dist/docgen_linux_amd64")
}

// The same name from the same path twice is one artifact recorded twice,
// not a collision.
func TestOneArtifactRecordedTwiceIsNotACollision(t *testing.T) {
	t.Parallel()

	plan, err := artifactcontroller.New().Plan(citypes.ArtifactInput{
		Revision: "abc123", Version: "v0.2.0",
		Artifacts: []forge.Artifact{
			{Name: "forge", Type: "binary", OS: "linux", Arch: "amd64", Location: "forge/build/dist/forge_linux_amd64"},
			{Name: "forge", Type: "binary", OS: "linux", Arch: "amd64", Location: "forge/build/dist/forge_linux_amd64"},
		},
	})
	require.NoError(t, err)
	require.Len(t, plan.Uploads, 1)
}
