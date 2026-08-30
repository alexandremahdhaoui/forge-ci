package managercontroller_test

import (
	"encoding/json"
	"errors"
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

// TestABootstrapOnlyResourceIsOwnedButNotRealizedByAnApply pins the rule that
// keeps a pipeline run from holding the rights to rewrite the secrets it runs
// under.
//
// A credential cannot be converged: it is written blind, because nothing can
// be read back to compare against, so realizing one on every run is only
// writing it again. The platform draws the same line - a workflow's own token
// is excluded from the secrets API whatever its permissions declare.
//
// Ownership is recorded either way. That record is what stops another manager
// claiming the resource, and that is true whichever ceremony this is.
func TestABootstrapOnlyResourceIsOwnedButNotRealizedByAnApply(t *testing.T) {
	dir := t.TempDir()

	credential := citypes.Resource{
		Kind:          "directory",
		Name:          filepath.Join(dir, "pretend-credential"),
		BootstrapOnly: true,
	}
	ordinary := citypes.Resource{Kind: "directory", Name: filepath.Join(dir, "ordinary")}

	t.Run("an apply owns it and leaves it alone", func(t *testing.T) {
		out, err := local(t).Reconcile(citypes.ReconcileInput{
			Manager:   "local",
			Resources: []citypes.Resource{ordinary, credential},
		})
		require.NoError(t, err)

		require.NoDirExists(t, credential.Name, "an apply must not write a credential")
		require.DirExists(t, ordinary.Name, "everything else still converges")

		ids := []string{}
		for _, o := range out.Owned {
			ids = append(ids, o.Resource)
		}

		require.Contains(t, ids, credential.ID(),
			"ownership survives, or another manager could claim it")
		require.Contains(t, out.Actions, "left "+credential.ID()+" alone: only a bootstrap writes it",
			"and it says so rather than looking like it did the work")
	})

	t.Run("a bootstrap writes it", func(t *testing.T) {
		_, err := local(t).Reconcile(citypes.ReconcileInput{
			Manager:   "local",
			Resources: []citypes.Resource{credential},
			Bootstrap: true,
		})
		require.NoError(t, err)
		require.DirExists(t, credential.Name)
	})
}

// One failing resource must not decide the fate of the ones after it.
//
// Realization stopped at the first error, so a reconcile's result depended on
// declaration order: a 403 on a credential left every later resource
// untouched, including files that cannot fail for a network reason. The
// operator fixed one thing, re-ran, met the next, and the checkout stayed
// stale throughout - which is indistinguishable from a generator that
// produced nothing.
func TestOneFailedResourceStillConvergesTheRest(t *testing.T) {
	t.Parallel()

	realizer := &countingRealizer{
		fail: map[string]error{"actions-secret/o/r/A_TOKEN": errBoom},
	}

	_, err := managercontroller.New(realizer, fsadapter.New()).Reconcile(citypes.ReconcileInput{
		Manager:   "m",
		Bootstrap: true,
		Resources: []citypes.Resource{
			{Kind: "file-content", Name: "first"},
			{Kind: "actions-secret", Name: "o/r/A_TOKEN"},
			{Kind: "file-content", Name: "after-the-failure"},
		},
	})

	require.ErrorIs(t, err, errBoom)
	require.Equal(t, []string{"first", "o/r/A_TOKEN", "after-the-failure"}, realizer.seen,
		"every resource must be attempted, whatever happened to the ones before it")
}

// Two failures are two lines. Fixing one thing and re-running to discover the
// next is how a provisioning session turns into an afternoon.
func TestEveryFailureIsReportedTogether(t *testing.T) {
	t.Parallel()

	realizer := &countingRealizer{
		fail: map[string]error{
			"actions-secret/o/r/A_TOKEN":  errBoom,
			"workflow-enabled/o/r/w.yaml": errOther,
		},
	}

	_, err := managercontroller.New(realizer, fsadapter.New()).Reconcile(citypes.ReconcileInput{
		Manager:   "m",
		Bootstrap: true,
		Resources: []citypes.Resource{
			{Kind: "actions-secret", Name: "o/r/A_TOKEN"},
			{Kind: "workflow-enabled", Name: "o/r/w.yaml"},
		},
	})

	require.ErrorIs(t, err, errBoom)
	require.ErrorIs(t, err, errOther)
}

// A resource with no kind or no name is the caller handing over something
// that is not a resource. That is not a realization that failed, and it stays
// fatal on the spot.
func TestAMalformedResourceIsStillFatalImmediately(t *testing.T) {
	t.Parallel()

	realizer := &countingRealizer{}

	_, err := managercontroller.New(realizer, fsadapter.New()).Reconcile(citypes.ReconcileInput{
		Manager:   "m",
		Resources: []citypes.Resource{{Kind: "", Name: "nameless"}, {Kind: "file-content", Name: "later"}},
	})

	require.Error(t, err)
	require.Empty(t, realizer.seen, "nothing is realized once the input is not a resource list")
}

var (
	errBoom  = errors.New("boom")
	errOther = errors.New("other")
)

// countingRealizer records what it was asked to realize, and fails the
// resource ids it was told to. It exists so a test can decide which resource
// breaks and then assert on what happened to the ones after it.
type countingRealizer struct {
	seen    []string
	fail    map[string]error
	changes bool
}

func (countingRealizer) Kind() string { return "counting" }

func (c *countingRealizer) Realize(res citypes.Resource) (managercontroller.Action, error) {
	c.seen = append(c.seen, res.Name)

	if err, ok := c.fail[res.ID()]; ok {
		return managercontroller.Action{}, err
	}

	if c.changes {
		return managercontroller.Did("realized " + res.ID()), nil
	}

	return managercontroller.Kept("realized " + res.ID()), nil
}
