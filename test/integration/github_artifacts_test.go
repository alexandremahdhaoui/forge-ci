//go:build integration

package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

func TestTheGitHubComputePutsThenGetsAnArtifactUnderTheRootOverMCP(t *testing.T) {
	root := t.TempDir()
	rel := filepath.Join("member", "build", "bin", "tool_linux_amd64")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "member", "build", "bin"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, rel), []byte("built bytes"), 0o600))

	var put citypes.ArtifactPutOutput

	require.NoError(t, caller().Call(context.Background(), githubComputeURI, "put", citypes.ArtifactPutInput{
		Revision:  "rev1",
		Root:      root,
		Artifacts: []forge.Artifact{{Name: "tool", Type: "binary", Location: rel}},
		Spec:      map[string]any{},
	}, &put))
	require.Len(t, put.Artifacts, 1)
	require.Equal(t, "forge-ci-artifact://rev1/member/build/bin/tool_linux_amd64", put.Artifacts[0].Location)

	require.NoError(t, os.Remove(filepath.Join(root, rel)))

	var got citypes.ArtifactGetOutput

	require.NoError(t, caller().Call(context.Background(), githubComputeURI, "get", citypes.ArtifactGetInput{
		Revision:  "rev1",
		Root:      root,
		Artifacts: put.Artifacts,
		Spec:      map[string]any{},
	}, &got))
	require.Len(t, got.Artifacts, 1)
	require.Equal(t, filepath.ToSlash(rel), got.Artifacts[0].Location)

	content, err := os.ReadFile(filepath.Join(root, rel))
	require.NoError(t, err)
	require.Equal(t, "built bytes", string(content))
}
