package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactcontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/gitadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/githubadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func releaseRoot(t *testing.T, withAsset bool) (string, citypes.ArtifactInput) {
	t.Helper()

	root := t.TempDir()
	rel := filepath.Join("m", "build", "dist", "tool_linux_amd64")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "m", "build", "dist"), 0o750))

	if withAsset {
		require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte("bytes"), 0o600))
	}

	return root, citypes.ArtifactInput{
		Revision:  "abc123",
		Version:   "v0.2.0",
		Repos:     map[string]string{"m": "sha-m"},
		Artifacts: []forge.Artifact{{Name: "tool", Type: "binary", OS: "linux", Arch: "amd64", Location: rel}},
		Spec:      map[string]any{"repo": "o/r", "root": root},
	}
}

// The order a crash cannot leave a consumer-visible half: every read and
// every digest first, then the draft with its assets, then the tags, then
// the publish. golden run 19 tagged first and then met a missing file.
func TestTheWritesHappenInAnOrderACrashCannotHalve(t *testing.T) {
	root, in := releaseRoot(t, true)
	dir := root + "/m"

	var order []string

	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HasRemote(mock.Anything, dir).Return(true, nil).Once()
	git.EXPECT().RemoteTagAt(mock.Anything, dir, "v0.2.0").Return("", false, nil).Once()
	git.EXPECT().Tag(mock.Anything, dir, "v0.2.0", "sha-m").
		Run(func(context.Context, string, string, string) { order = append(order, "tag") }).Return(nil).Once()

	api := githubadaptermock.NewMockAPI(t)
	api.EXPECT().ReleaseByTag(mock.Anything, "o/r", "v0.2.0").Return(githubadapter.Release{}, false, nil).Once()
	api.EXPECT().CreateDraftRelease(mock.Anything, "o/r", "v0.2.0").
		Run(func(context.Context, string, string) { order = append(order, "draft") }).
		Return(githubadapter.Release{ID: 9, Draft: true, UploadURL: "http://up/{?name}"}, nil).Once()
	api.EXPECT().UploadAsset(mock.Anything, "http://up/{?name}", mock.Anything, mock.Anything).
		Run(func(context.Context, string, string, string) { order = append(order, "upload") }).Return(nil).Times(2)
	api.EXPECT().PublishRelease(mock.Anything, "o/r", int64(9)).
		Run(func(context.Context, string, int64) { order = append(order, "publish") }).
		Return(githubadapter.Release{ID: 9, HTMLURL: "http://rel/v0.2.0"}, nil).Once()

	out, err := publish(context.Background(), artifactcontroller.New(), git, api, in)
	require.NoError(t, err)
	require.True(t, out.Published)
	require.Equal(t, "http://rel/v0.2.0", out.URL)
	require.Equal(t, []string{"m"}, out.Tagged)
	require.Contains(t, out.Index, `"tool"`, "the staged index comes back so the core can record it")
	require.Equal(t, []string{"draft", "upload", "upload", "tag", "publish"}, order)
}

// A missing asset fails before the first write: no draft, no tag, and the
// error names the file.
func TestAMissingAssetTagsNothing(t *testing.T) {
	root, in := releaseRoot(t, false)
	dir := root + "/m"

	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HasRemote(mock.Anything, dir).Return(true, nil).Once()
	git.EXPECT().RemoteTagAt(mock.Anything, dir, "v0.2.0").Return("", false, nil).Once()

	api := githubadaptermock.NewMockAPI(t)
	api.EXPECT().ReleaseByTag(mock.Anything, "o/r", "v0.2.0").Return(githubadapter.Release{}, false, nil).Once()

	_, err := publish(context.Background(), artifactcontroller.New(), git, api, in)
	require.Error(t, err)
	require.Contains(t, err.Error(), "tool_linux_amd64")
}

// Tags on their remotes and a release that exists is a revision already
// published: converged, nothing read from disk, nothing written.
func TestAFullyPublishedRevisionConvergesWithoutReadingAssets(t *testing.T) {
	root, in := releaseRoot(t, false)
	dir := root + "/m"

	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HasRemote(mock.Anything, dir).Return(true, nil).Once()
	git.EXPECT().RemoteTagAt(mock.Anything, dir, "v0.2.0").Return("sha-m", true, nil).Once()

	api := githubadaptermock.NewMockAPI(t)
	api.EXPECT().ReleaseByTag(mock.Anything, "o/r", "v0.2.0").
		Return(githubadapter.Release{HTMLURL: "http://rel/v0.2.0"}, true, nil).Once()

	out, err := publish(context.Background(), artifactcontroller.New(), git, api, in)
	require.NoError(t, err)
	require.False(t, out.Published)
	require.Equal(t, "http://rel/v0.2.0", out.URL)
	require.Contains(t, out.Reason, "converged")
}
