//go:build e2e

package e2e_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// lockPipelineYAML is the demo pipeline with the state engine declaring the
// dependency-lock kind - the opt-in that makes a mint record the closure.
func lockPipelineYAML(root, statePath string) string {
	base := pipelineYAML(root, statePath)

	return strings.Replace(base,
		"      path: "+statePath,
		"      path: "+statePath+"\n      kinds: [dependency-lock]", 1)
}

// A minted revision carries the exact closure it was proven with: the lock
// manifest forge-factory wrote is read at mint, each lockfile verified
// against its recorded hash, and one dependency-lock record per file lands
// in the state repo under the revision's own id.
func TestAMintRecordsTheDependencyLocksBesideTheRevision(t *testing.T) {
	root, statePath := bareWorkspace(t, "true")

	require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci.yaml"),
		[]byte(lockPipelineYAML(root, statePath)), 0o600))

	// The manifest forge-factory's lock would have written, plus the
	// lockfile it hashes. The core knows neither name - only the manifest
	// path is contract.
	lockfile := "example.com/some-module v1.2.3 h1:abcdef\n"
	sum := sha256.Sum256([]byte(lockfile))

	require.NoError(t, os.MkdirAll(filepath.Join(root, "demo-repo"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, "demo-repo", "go.sum"),
		[]byte(lockfile), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".forge"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".forge", "dependency-locks.json"),
		[]byte(fmt.Sprintf(`{"version":1,"locks":[{"path":"demo-repo/go.sum","sha256":"%s"}]}`,
			hex.EncodeToString(sum[:]))), 0o600))

	// The lockfile sits in the member checkout uncommitted; it must not
	// dirty the revision, exactly as a real lock output would be ignored.
	require.NoError(t, os.WriteFile(filepath.Join(root, "demo-repo", ".gitignore"),
		[]byte("/.forge/\n.envrc\n/tmp/\n/go.sum\n"), 0o600))
	mustRun(t, filepath.Join(root, "demo-repo"), "git", "add", ".gitignore")
	mustRun(t, filepath.Join(root, "demo-repo"), "git", "commit", "-m", "ignore the lock output")

	mustRun(t, root, "forge-ci", "bootstrap", "--config", filepath.Join(root, "forge-ci.yaml"))
	mustRun(t, root, "forge-ci", "apply", "--config", filepath.Join(root, "forge-ci.yaml"))

	revision := revisionID(t, statePath)

	raw, err := os.ReadFile(filepath.Join(statePath,
		"dependency-lock", revision, "demo-repo", "go.sum.json"))
	require.NoError(t, err, "the record must land under the revision's own id")

	var record struct {
		Revision string `json:"revision"`
		Path     string `json:"path"`
		SHA256   string `json:"sha256"`
		Lockfile string `json:"lockfile"`
	}
	require.NoError(t, json.Unmarshal(raw, &record))
	require.Equal(t, revision, record.Revision)
	require.Equal(t, "demo-repo/go.sum", record.Path)
	require.Equal(t, hex.EncodeToString(sum[:]), record.SHA256)
	require.Equal(t, lockfile, record.Lockfile,
		"the record carries the exact bytes the revision was proven with")
}

// A manifest whose hash disagrees with the file refuses the mint's record:
// the tree moved between the lock and the mint, and pinning the revision to
// either version would be a claim about bytes it was not proven with.
func TestAChangedLockfileRefusesTheRecord(t *testing.T) {
	root, statePath := bareWorkspace(t, "true")

	require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci.yaml"),
		[]byte(lockPipelineYAML(root, statePath)), 0o600))

	require.NoError(t, os.WriteFile(filepath.Join(root, "demo-repo", "go.sum"),
		[]byte("the bytes that are actually here\n"), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".forge"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".forge", "dependency-locks.json"),
		[]byte(`{"version":1,"locks":[{"path":"demo-repo/go.sum","sha256":"`+
			strings.Repeat("0", 64)+`"}]}`), 0o600))

	require.NoError(t, os.WriteFile(filepath.Join(root, "demo-repo", ".gitignore"),
		[]byte("/.forge/\n.envrc\n/tmp/\n/go.sum\n"), 0o600))
	mustRun(t, filepath.Join(root, "demo-repo"), "git", "add", ".gitignore")
	mustRun(t, filepath.Join(root, "demo-repo"), "git", "commit", "-m", "ignore the lock output")

	mustRun(t, root, "forge-ci", "bootstrap", "--config", filepath.Join(root, "forge-ci.yaml"))

	out, err := run(t, root, "forge-ci", "apply", "--config", filepath.Join(root, "forge-ci.yaml"))
	require.Error(t, err, out)
	require.Contains(t, out, "changed since the lock resolved it")
}

// A pipeline whose state engine never declared the kind records nothing,
// manifest or not: the opt-in is the declaration.
func TestAStoreWithoutTheKindRecordsNoLocks(t *testing.T) {
	root, statePath := bareWorkspace(t, "true")

	require.NoError(t, os.MkdirAll(filepath.Join(root, ".forge"), 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".forge", "dependency-locks.json"),
		[]byte(`{"version":1,"locks":[]}`), 0o600))

	mustRun(t, root, "forge-ci", "bootstrap", "--config", filepath.Join(root, "forge-ci.yaml"))
	mustRun(t, root, "forge-ci", "apply", "--config", filepath.Join(root, "forge-ci.yaml"))

	_, err := os.Stat(filepath.Join(statePath, "dependency-lock"))
	require.True(t, os.IsNotExist(err),
		"no kind was declared, so no record family exists in the store")
}
