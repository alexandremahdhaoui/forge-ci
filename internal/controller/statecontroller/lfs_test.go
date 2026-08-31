package statecontroller_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/statecontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/stretchr/testify/require"
)

// An LFS-tracked kind stores a pointer, not the content. Real git and real
// git-lfs, because the clean filter is the whole mechanism and a mock cannot
// tell a pointer from a payload.
func TestAnLFSKindCommitsAPointerNotThePayload(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs is not installed on this machine; the LFS path cannot be proven here " +
			"(the toolchain image does not carry it yet either - see FOLLOWUP)")
	}

	root := t.TempDir()
	spec := map[string]any{
		"path":  root,
		"kinds": []any{"dependency-lock"},
		"lfs":   []any{"dependency-lock"},
	}

	c := statecontroller.New(fsadapter.New(), gitadapter.New(execadapter.New()))

	payload := `{"revision":"abc123","path":"golden-go/go.sum","sha256":"deadbeef","lockfile":"` +
		strings.Repeat("x", 512) + `"}`

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: "dependency-lock", Key: "abc123/golden-go", Payload: payload, Spec: spec,
	})
	require.NoError(t, err)

	// The attributes rule and the record travel in the same commit.
	attrs, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	require.NoError(t, err)
	require.Contains(t, string(attrs), "dependency-lock/** filter=lfs diff=lfs merge=lfs -text")

	// The committed blob is a pointer: git's own object for the path starts
	// with the LFS spec line, and the payload bytes are NOT in the object.
	blob, err := exec.Command("git", "-C", root, "cat-file", "-p",
		"HEAD:dependency-lock/abc123/golden-go.json").Output()
	require.NoError(t, err)
	require.Contains(t, string(blob), "version https://git-lfs.github.com/spec/v1",
		"the committed object must be an LFS pointer")
	require.NotContains(t, string(blob), "deadbeef",
		"the payload itself must live in the LFS object store, not the git object")

	// The round trip still answers the payload: Get reads the worktree,
	// where the smudge filter has the real content.
	got, err := c.Get(context.Background(), citypes.StateGetInput{
		Kind: "dependency-lock", Key: "abc123/golden-go", Spec: spec,
	})
	require.NoError(t, err)
	require.True(t, got.Found)
	require.JSONEq(t, payload, got.Payload)
}

// A kind outside spec.lfs keeps committing plain blobs, and the store never
// gains an attributes file it did not need.
func TestAPlainKindIsUntouchedByTheLFSDeclaration(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs is not installed on this machine")
	}

	root := t.TempDir()
	spec := map[string]any{
		"path":  root,
		"kinds": []any{"dependency-lock"},
		"lfs":   []any{"dependency-lock"},
	}

	c := statecontroller.New(fsadapter.New(), gitadapter.New(execadapter.New()))

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: statecontroller.KindRevision, Key: "abc123", Payload: `{"id":"abc123"}`, Spec: spec,
	})
	require.NoError(t, err)

	blob, err := exec.Command("git", "-C", root, "cat-file", "-p",
		"HEAD:revisions/abc123.json").Output()
	require.NoError(t, err)
	require.JSONEq(t, `{"id":"abc123"}`, string(blob),
		"a plain kind's committed object is the payload itself")

	_, statErr := os.Stat(filepath.Join(root, ".gitattributes"))
	require.True(t, os.IsNotExist(statErr),
		"no LFS-tracked kind was written, so no attributes file exists yet")
}

// spec.lfs naming an undeclared kind is refused loud: a typo would otherwise
// be a rule that silently tracks nothing.
func TestLFSNamingAnUndeclaredKindIsRefused(t *testing.T) {
	root := t.TempDir()
	spec := map[string]any{
		"path": root,
		"lfs":  []any{"dependency-lok"},
	}

	c := statecontroller.New(fsadapter.New(), nil)

	_, err := c.Put(context.Background(), citypes.StatePutInput{
		Kind: statecontroller.KindRevision, Key: "abc", Payload: "{}", Spec: spec,
	})
	require.ErrorIs(t, err, statecontroller.ErrLFS)
	require.Contains(t, err.Error(), "dependency-lok")
}
