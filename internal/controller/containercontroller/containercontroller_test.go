package containercontroller_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/containercontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

func input(spec map[string]any, artifacts ...forge.Artifact) citypes.ArtifactInput {
	return citypes.ArtifactInput{
		Revision:  "abc123def456",
		Version:   "v0.50.0",
		Artifacts: artifacts,
		Spec:      spec,
	}
}

func image(name, location string) forge.Artifact {
	return forge.Artifact{Name: name, Type: "container", Location: location}
}

func TestThePublishedTagIsTheVersionAndNothingElse(t *testing.T) {
	t.Parallel()

	plan, err := containercontroller.New().Plan(input(
		map[string]any{"image": "ghcr.io/owner/forge"},
		image("toolchain", "file:///w/build/images/toolchain.oci"),
	))
	require.NoError(t, err)

	assert.Equal(t, []string{"ghcr.io/owner/forge:v0.50.0"}, plan.Tags)
	assert.Equal(t, []string{"/w/build/images/toolchain.oci"}, plan.Layouts)
}

// A prefix is a namespace for the case where one repo is released by more
// than one factory. The image tag follows the git tag, or the two names for
// one release disagree.
func TestThePrefixReachesTheImageTag(t *testing.T) {
	t.Parallel()

	in := input(map[string]any{"image": "ghcr.io/owner/forge"},
		image("toolchain", "file:///w/toolchain.oci"))
	in.TagPrefix = "forge"

	plan, err := containercontroller.New().Plan(in)
	require.NoError(t, err)
	assert.Equal(t, []string{"ghcr.io/owner/forge:forge-v0.50.0"}, plan.Tags)
}

// A moving tag is a convenience and never something to pin, so the version
// tag comes first and the moving ones follow it.
func TestMovingTagsFollowTheVersionAndPointAtTheSameIndex(t *testing.T) {
	t.Parallel()

	plan, err := containercontroller.New().Plan(input(
		map[string]any{
			"image":      "ghcr.io/owner/forge",
			"movingTags": []any{"latest", "edge", "latest", ""},
		},
		image("toolchain", "file:///w/toolchain.oci"),
	))
	require.NoError(t, err)

	assert.Equal(t, []string{
		"ghcr.io/owner/forge:v0.50.0",
		"ghcr.io/owner/forge:latest",
		"ghcr.io/owner/forge:edge",
	}, plan.Tags, "the version is first, duplicates and blanks are dropped")
}

// Filtering on the type is what keeps a binary out of the registry and a
// container off the upload path.
func TestOnlyContainerArtifactsArePublished(t *testing.T) {
	t.Parallel()

	plan, err := containercontroller.New().Plan(input(
		map[string]any{"image": "ghcr.io/owner/forge"},
		forge.Artifact{Name: "forge", Type: "binary", Location: "build/dist/forge_linux_amd64"},
		image("toolchain", "file:///w/toolchain.oci"),
		forge.Artifact{Name: "docs", Type: "file", Location: "docs/index.html"},
	))
	require.NoError(t, err)
	assert.Equal(t, []string{"/w/toolchain.oci"}, plan.Layouts)
}

// A tag silently left pointing at last week's image is worse than a red
// build: the operator reads a version number and gets something else.
func TestAReleaseWithNoContainerBuiltIsRefused(t *testing.T) {
	t.Parallel()

	_, err := containercontroller.New().Plan(input(
		map[string]any{"image": "ghcr.io/owner/forge"},
		forge.Artifact{Name: "forge", Type: "binary", Location: "build/dist/forge_linux_amd64"},
	))
	require.ErrorIs(t, err, containercontroller.ErrNoImages)
}

// The build writes a local layout. A remote reference here means the build
// and the release disagree about what was built, which is worth saying rather
// than guessing at.
func TestAContainerArtifactThatIsNotALocalLayoutIsRefused(t *testing.T) {
	t.Parallel()

	_, err := containercontroller.New().Plan(input(
		map[string]any{"image": "ghcr.io/owner/forge"},
		image("toolchain", "ghcr.io/somebody/else:v1"),
	))
	require.ErrorIs(t, err, containercontroller.ErrLocation)
	require.Contains(t, err.Error(), "toolchain")
}

func TestTheImageIsRequiredAndCarriesNoTag(t *testing.T) {
	t.Parallel()

	_, err := containercontroller.New().Plan(input(
		map[string]any{}, image("toolchain", "file:///w/toolchain.oci")))
	require.ErrorIs(t, err, containercontroller.ErrImage)

	// A tag in the image name would be a second authority over what the
	// release is called, and the two would disagree the moment they differ.
	_, err = containercontroller.New().Plan(input(
		map[string]any{"image": "ghcr.io/owner/forge:latest"},
		image("toolchain", "file:///w/toolchain.oci")))
	require.ErrorIs(t, err, containercontroller.ErrImage)
	require.Contains(t, err.Error(), "the tag is the version")

	// A digest in the name is the same mistake, and it also pins bytes the
	// release did not build.
	_, err = containercontroller.New().Plan(input(
		map[string]any{"image": "ghcr.io/owner/forge@sha256:abc"},
		image("toolchain", "file:///w/toolchain.oci")))
	require.ErrorIs(t, err, containercontroller.ErrImage)
}

// A colon is a registry port far more often than it is a tag. Reading one as
// a tag refused every registry that is not on 443, which is every local one.
func TestARegistryPortIsNotATag(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"127.0.0.1:39509/owner/forge",
		"localhost:5000/forge",
		"ghcr.io/owner/forge",
		"forge",
	} {
		plan, err := containercontroller.New().Plan(input(
			map[string]any{"image": name},
			image("toolchain", "file:///w/toolchain.oci")))
		require.NoError(t, err, "image %q", name)
		assert.Equal(t, name+":v0.50.0", plan.Tags[0])
	}
}

// A dirty revision was never proven, so publishing it ships something nobody
// can reproduce. Same rule as the binary release, because it is the same
// reason.
func TestADirtyOrMissingRevisionIsRefused(t *testing.T) {
	t.Parallel()

	in := input(map[string]any{"image": "ghcr.io/owner/forge"},
		image("toolchain", "file:///w/toolchain.oci"))

	in.Revision = "abc123-dirty"
	_, err := containercontroller.New().Plan(in)
	require.ErrorIs(t, err, containercontroller.ErrDirty)

	in.Revision = ""
	_, err = containercontroller.New().Plan(in)
	require.ErrorIs(t, err, containercontroller.ErrRevision)
}

func TestAVersionThatIsNotSemverIsRefused(t *testing.T) {
	t.Parallel()

	in := input(map[string]any{"image": "ghcr.io/owner/forge"},
		image("toolchain", "file:///w/toolchain.oci"))
	in.Version = "latest"

	_, err := containercontroller.New().Plan(in)
	require.ErrorIs(t, err, containercontroller.ErrVersion)
}

// Somebody holding only the image has to be able to find the revision it came
// from, because that is the tuple the whole pipeline is keyed on.
func TestTheVersionAndRevisionAreLabelledAndTheInstanceWins(t *testing.T) {
	t.Parallel()

	plan, err := containercontroller.New().Plan(input(
		map[string]any{
			"image": "ghcr.io/owner/forge",
			"labels": map[string]any{
				"org.opencontainers.image.source":  "https://github.com/owner/forge",
				"org.opencontainers.image.version": "whatever the instance says",
			},
		},
		image("toolchain", "file:///w/toolchain.oci"),
	))
	require.NoError(t, err)

	assert.Equal(t, "abc123def456", plan.Labels["org.opencontainers.image.revision"])
	assert.Equal(t, "https://github.com/owner/forge", plan.Labels["org.opencontainers.image.source"])
	assert.Equal(t, "whatever the instance says", plan.Labels["org.opencontainers.image.version"],
		"the instance knows things this does not")
}

func TestDeclareOwnsNothing(t *testing.T) {
	t.Parallel()

	out, err := containercontroller.New().Declare(nil)
	require.NoError(t, err)
	assert.Empty(t, out.Resources, "a release writes to somebody else's registry")
}
