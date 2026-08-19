package managercontroller_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/stretchr/testify/require"
)

func local(t *testing.T) *managercontroller.Controller {
	t.Helper()

	fs := fsadapter.New()

	return managercontroller.New(managercontroller.NewLocalRealizer(fs), fs)
}

func TestLocalCreatesADirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")

	out, err := local(t).Reconcile(citypes.ReconcileInput{
		Manager:   "local",
		Resources: []citypes.Resource{{Kind: "directory", Name: dir}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"created directory " + dir}, out.Actions)
	require.Equal(t, []citypes.Ownership{{Resource: "directory/" + dir, Manager: "local"}}, out.Owned)
	require.DirExists(t, dir)
}

func TestLocalIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	in := citypes.ReconcileInput{
		Manager:   "local",
		Resources: []citypes.Resource{{Kind: "directory", Name: dir}},
	}

	first, err := local(t).Reconcile(in)
	require.NoError(t, err)

	in.Owned = first.Owned

	second, err := local(t).Reconcile(in)
	require.NoError(t, err)
	require.Equal(t, first.Owned, second.Owned)
}

func TestLocalKeepsAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keep.txt")
	require.NoError(t, os.WriteFile(path, []byte("mine"), 0o600))

	out, err := local(t).Reconcile(citypes.ReconcileInput{
		Manager:   "local",
		Resources: []citypes.Resource{{Kind: "file", Name: path, Spec: map[string]any{"content": "other"}}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"kept file " + path}, out.Actions)

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "mine", string(body))
}

func TestLocalCreatesAFileWithContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "new.txt")

	_, err := local(t).Reconcile(citypes.ReconcileInput{
		Manager:   "local",
		Resources: []citypes.Resource{{Kind: "file", Name: path, Spec: map[string]any{"content": "hello"}}},
	})
	require.NoError(t, err)

	body, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "hello", string(body))
}

func TestSwappingTheManagerIsRefused(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")

	_, err := local(t).Reconcile(citypes.ReconcileInput{
		Manager:   "dryrun",
		Resources: []citypes.Resource{{Kind: "directory", Name: dir}},
		Owned:     []citypes.Ownership{{Resource: "directory/" + dir, Manager: "local"}},
	})
	require.ErrorIs(t, err, managercontroller.ErrOwnedElsewhere)
	require.Contains(t, err.Error(), "recorded owner is \"local\"")
	require.Contains(t, err.Error(), "import it or destroy it first")
}

func TestUnknownKindIsRefused(t *testing.T) {
	_, err := local(t).Reconcile(citypes.ReconcileInput{
		Manager:   "local",
		Resources: []citypes.Resource{{Kind: "table", Name: "runs"}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot realize kind \"table\"")
}

func TestResourceNeedsKindAndName(t *testing.T) {
	_, err := local(t).Reconcile(citypes.ReconcileInput{
		Manager:   "local",
		Resources: []citypes.Resource{{Kind: "directory"}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "needs a kind and a name")
}

func TestManagerAliasIsRequired(t *testing.T) {
	_, err := local(t).Reconcile(citypes.ReconcileInput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "manager alias is required")
}

func TestDryRunChangesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")

	c := managercontroller.New(managercontroller.NewDryRunRealizer(), fsadapter.New())

	out, err := c.Reconcile(citypes.ReconcileInput{
		Manager:   "dryrun",
		Resources: []citypes.Resource{{Kind: "directory", Name: dir}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"would realize directory/" + dir}, out.Actions)
	require.NoDirExists(t, dir)
}

func TestStatePathRecordsWhatWasCreated(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "state")
	statePath := filepath.Join(tmp, "manager-local.json")

	_, err := local(t).Reconcile(citypes.ReconcileInput{
		Manager:   "local",
		Resources: []citypes.Resource{{Kind: "directory", Name: dir}},
		Spec:      map[string]any{"statePath": statePath},
	})
	require.NoError(t, err)

	raw, err := os.ReadFile(statePath)
	require.NoError(t, err)

	var recorded struct {
		Manager string              `json:"manager"`
		Kind    string              `json:"kind"`
		Owned   []citypes.Ownership `json:"owned"`
	}
	require.NoError(t, json.Unmarshal(raw, &recorded))
	require.Equal(t, "local", recorded.Manager)
	require.Equal(t, "local", recorded.Kind)
	require.Len(t, recorded.Owned, 1)
}

func TestOwnedIsSortedSoTheRecordIsStable(t *testing.T) {
	tmp := t.TempDir()

	out, err := local(t).Reconcile(citypes.ReconcileInput{
		Manager: "local",
		Resources: []citypes.Resource{
			{Kind: "directory", Name: filepath.Join(tmp, "z")},
			{Kind: "directory", Name: filepath.Join(tmp, "a")},
		},
	})
	require.NoError(t, err)
	require.Len(t, out.Owned, 2)
	require.Less(t, out.Owned[0].Resource, out.Owned[1].Resource)
}

func TestDryRunKindIsReported(t *testing.T) {
	require.Equal(t, "dryrun", managercontroller.NewDryRunRealizer().Kind())
	require.Equal(t, "local", managercontroller.NewLocalRealizer(fsadapter.New()).Kind())
}

func TestRealizeErrorIsWrappedWithTheResourceID(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o600))

	_, err := local(t).Reconcile(citypes.ReconcileInput{
		Manager:   "local",
		Resources: []citypes.Resource{{Kind: "directory", Name: filepath.Join(blocker, "child")}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "realizing directory/")
}

func TestFileExistsFailureIsReported(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o600))

	_, err := local(t).Reconcile(citypes.ReconcileInput{
		Manager:   "local",
		Resources: []citypes.Resource{{Kind: "file", Name: filepath.Join(blocker, "child", "f.txt")}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "realizing file/")
}

func TestRecordingFailureIsReported(t *testing.T) {
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("not a directory"), 0o600))

	_, err := local(t).Reconcile(citypes.ReconcileInput{
		Manager:   "local",
		Resources: []citypes.Resource{{Kind: "directory", Name: filepath.Join(tmp, "state")}},
		Spec:      map[string]any{"statePath": filepath.Join(blocker, "sub", "state.json")},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "recording manager state")
}

func TestNoStatePathSkipsRecording(t *testing.T) {
	tmp := t.TempDir()

	_, err := local(t).Reconcile(citypes.ReconcileInput{
		Manager:   "local",
		Resources: []citypes.Resource{{Kind: "directory", Name: filepath.Join(tmp, "state")}},
		Spec:      map[string]any{"statePath": ""},
	})
	require.NoError(t, err)
}
