package containeradapter_test

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/containeradapter"
)

// written is a layout on disk holding one index over the named architectures,
// which is what the build engine writes.
func written(t *testing.T, arches ...string) string {
	t.Helper()

	idx := v1.ImageIndex(empty.Index)
	idx = mutate.IndexMediaType(idx, types.OCIImageIndex)

	for _, arch := range arches {
		cf, err := empty.Image.ConfigFile()
		require.NoError(t, err)

		cf = cf.DeepCopy()
		cf.OS, cf.Architecture = "linux", arch
		cf.Config.Labels = map[string]string{"built.by": "the build stage"}

		img, err := mutate.ConfigFile(empty.Image, cf)
		require.NoError(t, err)

		idx = mutate.AppendManifests(idx, mutate.IndexAddendum{
			Add:        img,
			Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: arch}},
		})
	}

	dir := filepath.Join(t.TempDir(), "toolchain.oci")
	require.NoError(t, os.MkdirAll(dir, 0o750))

	_, err := layout.Write(dir, idx)
	require.NoError(t, err)

	return dir
}

func localRegistry(t *testing.T) string {
	t.Helper()

	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)

	return strings.TrimPrefix(srv.URL, "http://")
}

// Every tag must point at the SAME index. Two pushes that happen to produce
// matching bytes is not the same thing as one index under two names, and the
// difference shows the day one of them drifts.
func TestEveryTagPointsAtTheSameIndex(t *testing.T) {
	t.Parallel()

	host := localRegistry(t)
	path := written(t, "amd64", "arm64")

	refs := []string{host + "/forge:v0.50.0", host + "/forge:latest"}
	require.NoError(t, (&containeradapter.Remote{}).Push(path, refs, nil))

	digests := map[string]bool{}

	for _, ref := range refs {
		parsed, err := name.ParseReference(ref)
		require.NoError(t, err)

		idx, err := remote.Index(parsed)
		require.NoError(t, err)

		manifest, err := idx.IndexManifest()
		require.NoError(t, err)
		require.Len(t, manifest.Manifests, 2, "%s carries both architectures", ref)

		d, err := idx.Digest()
		require.NoError(t, err)

		digests[d.String()] = true
	}

	require.Len(t, digests, 1, "the version tag and the moving tag are one index, not two builds")
}

// The build wrote what it knew; the release adds what only it knows. Both
// have to survive, in every architecture, or somebody holding the image for
// one arch cannot find the revision.
func TestTheReleaseLabelsReachEveryArchitectureAndTheBuildsSurvive(t *testing.T) {
	t.Parallel()

	host := localRegistry(t)
	path := written(t, "amd64", "arm64")

	ref := host + "/forge:v0.50.0"
	require.NoError(t, (&containeradapter.Remote{}).Push(path, []string{ref}, map[string]string{
		"org.opencontainers.image.revision": "abc123def456",
	}))

	parsed, err := name.ParseReference(ref)
	require.NoError(t, err)

	for _, arch := range []string{"amd64", "arm64"} {
		img, err := remote.Image(parsed,
			remote.WithPlatform(v1.Platform{OS: "linux", Architecture: arch}))
		require.NoError(t, err)

		cf, err := img.ConfigFile()
		require.NoError(t, err)

		assert.Equal(t, arch, cf.Architecture)
		assert.Equal(t, "abc123def456", cf.Config.Labels["org.opencontainers.image.revision"])
		assert.Equal(t, "the build stage", cf.Config.Labels["built.by"])
	}
}

// A layout with no manifest means the build that was supposed to write it did
// not, and pushing it would leave a tag naming nothing.
func TestAnEmptyLayoutIsRefused(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t,
		(&containeradapter.Remote{}).Push(written(t), []string{localRegistry(t) + "/forge:v0.50.0"}, nil),
		containeradapter.ErrEmptyLayout)
}

func TestALayoutThatIsNotThereIsNamedInTheError(t *testing.T) {
	t.Parallel()

	err := (&containeradapter.Remote{}).Push("/nowhere/toolchain.oci",
		[]string{localRegistry(t) + "/forge:v0.50.0"}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "/nowhere/toolchain.oci")
}

func TestAReferenceItCannotReadIsNamedInTheError(t *testing.T) {
	t.Parallel()

	err := (&containeradapter.Remote{}).Push(written(t, "amd64"), []string{"NOT A REF"}, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "NOT A REF")
}
