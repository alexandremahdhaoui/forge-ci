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

func TestPlanUploadsOnlyLocalFiles(t *testing.T) {
	t.Parallel()

	plan, err := artifactcontroller.New().Plan(citypes.ArtifactInput{
		Revision: "abc123",
		Version:  "v0.2.0",
		Artifacts: []forge.Artifact{
			{Name: "cli", Location: "file:///out/cli"},
			{Name: "img", Location: "ghcr.io/x/img:v1"},
			{Name: "empty", Location: ""},
			{Name: "other", Location: "file:///out/a.tar"},
		},
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"/out/a.tar", "/out/cli"}, plan.Uploads,
		"a container image is published by whatever built it")
}

func TestDeclareOwnsNothing(t *testing.T) {
	t.Parallel()

	out, err := artifactcontroller.New().Declare(nil)
	require.NoError(t, err)
	assert.Empty(t, out.Resources, "a release writes to someone else's system")
}
