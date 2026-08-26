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

func TestPlanUploadsOnlyLocalBinaries(t *testing.T) {
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

	assert.Equal(t, []string{"/out/cli", "member/build/dist/rel_linux_amd64"}, plan.Uploads,
		"binaries upload once each; images and generated files belong to whatever made them")
}

// The aggregated release is one per revision: everything built together is
// released together, under a tag no semver fan-out matches.
func TestPlanNamesTheAggregatedDistTag(t *testing.T) {
	t.Parallel()

	plan, err := artifactcontroller.New().Plan(citypes.ArtifactInput{
		Revision: "abc123def456",
		Version:  "v0.2.0",
	})
	require.NoError(t, err)
	assert.Equal(t, "dist-abc123def456", plan.DistTag)
}

func TestDeclareOwnsNothing(t *testing.T) {
	t.Parallel()

	out, err := artifactcontroller.New().Declare(nil)
	require.NoError(t, err)
	assert.Empty(t, out.Resources, "a release writes to someone else's system")
}

func TestNextVersionStartsAtTheFirstRelease(t *testing.T) {
	t.Parallel()

	got, err := artifactcontroller.NextVersion("")
	require.NoError(t, err)
	assert.Equal(t, "v0.1.0", got, "a workspace that never released starts here")
}

func TestNextVersionBumpsThePatch(t *testing.T) {
	t.Parallel()

	for previous, want := range map[string]string{
		"v0.1.0":  "v0.1.1",
		"v0.1.9":  "v0.1.10",
		"v1.2.3":  "v1.2.4",
		"v0.44.2": "v0.44.3",
	} {
		got, err := artifactcontroller.NextVersion(previous)
		require.NoError(t, err, previous)
		assert.Equal(t, want, got, previous)
	}
}

func TestAPrereleaseIsReleasedAsWhatItWasACandidateFor(t *testing.T) {
	t.Parallel()

	got, err := artifactcontroller.NextVersion("v1.0.0-rc.1")
	require.NoError(t, err)
	assert.Equal(t, "v1.0.0", got)
}

func TestNextVersionRefusesSomethingThatIsNotAVersion(t *testing.T) {
	t.Parallel()

	for _, previous := range []string{"latest", "1.2.3", "v1.2", "v1.2.3.4"} {
		_, err := artifactcontroller.NextVersion(previous)
		require.ErrorIs(t, err, artifactcontroller.ErrPrevious, previous)
	}
}
