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

// Only what travels is published, and a thing travels only under the
// name_os_arch convention: the name says which machine it runs on. A host
// build carries no platform and stays home - publishing it would hand a
// consumer a binary with no way to know whether it runs.
func TestPlanUploadsOnlyWhatTravels(t *testing.T) {
	t.Parallel()

	plan, err := artifactcontroller.New().Plan(citypes.ArtifactInput{
		Revision: "abc123",
		Version:  "v0.2.0",
		Artifacts: []forge.Artifact{
			{Name: "cli", Type: "binary", Location: "file:///out/cli"},
			{Name: "rel", Type: "binary", Location: "member/build/dist/rel_linux_amd64"},
			{Name: "img", Type: "binary", Location: "ghcr.io/x/img:v1"},
			{Name: "empty", Type: "binary", Location: ""},
			{Name: "gen", Type: "generated", Location: "member/zz_generated.go"},
			{Name: "dup", Type: "binary", Location: "member/build/dist/rel_linux_amd64"},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"member/build/dist/rel_linux_amd64"}, plan.Uploads,
		"a platform-named binary uploads once; a host build, an image and a generated file never travel")
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
			{Name: "docgen_linux_amd64", Type: "binary", Location: "forge-ci/build/dist/docgen_linux_amd64"},
			{Name: "docgen_linux_amd64", Type: "binary", Location: "forge-factory/build/dist/docgen_linux_amd64"},
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
			{Name: "forge_linux_amd64", Type: "binary", Location: "forge/build/dist/forge_linux_amd64"},
			{Name: "forge_linux_amd64", Type: "binary", Location: "forge/build/dist/forge_linux_amd64"},
		},
	})
	require.NoError(t, err)
	require.Len(t, plan.Uploads, 1)
}
