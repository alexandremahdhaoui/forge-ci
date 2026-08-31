package managercontroller_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/githubadaptermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

// workspace stands up a root holding one member checkout with one commit,
// and a bare remote it pushes to. Everything is real git: the settle is a
// commit and a push, and a mock cannot say whether either happened.
func workspace(t *testing.T) (root, member, remote string) {
	t.Helper()

	root = t.TempDir()
	member = filepath.Join(root, "member")
	remote = filepath.Join(t.TempDir(), "member.git")

	require.NoError(t, os.MkdirAll(member, 0o750))

	run(t, "", "init", "--bare", "-b", "main", remote)
	run(t, member, "init", "-q", "-b", "main")
	run(t, member, "remote", "add", "origin", remote)

	require.NoError(t, os.WriteFile(filepath.Join(member, "README"), []byte("x\n"), 0o600))
	run(t, member, "add", "README")
	run(t, member, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "first")
	run(t, member, "push", "-q", "origin", "main")

	return root, member, remote
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()

	res, err := execadapter.New().Run(t.Context(), dir, "git", args...)
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, "git %v: %s", args, res.Stderr)
}

func out(t *testing.T, dir string, args ...string) string {
	t.Helper()

	res, err := execadapter.New().Run(t.Context(), dir, "git", args...)
	require.NoError(t, err)
	require.Zero(t, res.ExitCode, "git %v: %s", args, res.Stderr)

	return res.Stdout
}

func settling(t *testing.T, root string) *managercontroller.Controller {
	t.Helper()

	fs := fsadapter.New()
	api := githubadaptermock.NewMockAPI(t)
	git := gitadapter.New(execadapter.New())

	return managercontroller.New(
		managercontroller.NewGitHubRealizer(t.Context(), fs, api, git, root), fs)
}

func fileContent(name, body string) citypes.Resource {
	return citypes.Resource{
		Kind: managercontroller.KindFileContent,
		Name: name,
		Spec: map[string]any{"content": body},
	}
}

// The whole mechanism in one test: converge, commit, push, and say changed.
// Nothing else in this package can prove the push, because a push is the
// only thing that makes the next run see the corrected state.
func TestASettleCommitsAndPushesWhatItConverged(t *testing.T) {
	root, member, remote := workspace(t)

	first, err := settling(t, root).Reconcile(citypes.ReconcileInput{
		Manager:   "github",
		Resources: []citypes.Resource{fileContent("member/.github/workflows/ci.yaml", "on: push\n")},
	})
	require.NoError(t, err)
	assert.True(t, first.Changed, "the file was missing, so this converged it")

	assert.Empty(t, out(t, member, "status", "--porcelain"),
		"the settle committed what it wrote, so the tree is clean and the revision is not dirty")

	assert.Contains(t, out(t, remote, "show", "main:.github/workflows/ci.yaml"), "on: push",
		"the remote carries it, which is what makes the next run find no drift")
}

// The termination proof. A second reconcile over the settled state finds
// everything as declared and reports no change, so the run it belongs to
// reaches its stages. Without this the pipeline stops on every run forever.
func TestASecondReconcileOverSettledStateChangesNothing(t *testing.T) {
	root, member, _ := workspace(t)

	res := []citypes.Resource{fileContent("member/.github/workflows/ci.yaml", "on: push\n")}

	first, err := settling(t, root).Reconcile(citypes.ReconcileInput{Manager: "github", Resources: res})
	require.NoError(t, err)
	require.True(t, first.Changed)

	head := out(t, member, "rev-parse", "HEAD")

	second, err := settling(t, root).Reconcile(citypes.ReconcileInput{
		Manager: "github", Resources: res, Owned: first.Owned,
	})
	require.NoError(t, err)
	assert.False(t, second.Changed, "nothing differed, so nothing stops the run")
	assert.Equal(t, head, out(t, member, "rev-parse", "HEAD"), "and nothing was committed")
}

// A human's uncommitted work is not the pipeline's to publish. Only the
// paths that changed are staged, which is why this stages an explicit list
// and never the worktree.
func TestASettleLeavesUnrelatedUncommittedWorkAlone(t *testing.T) {
	root, member, _ := workspace(t)

	mine := filepath.Join(member, "half-finished.go")
	require.NoError(t, os.WriteFile(mine, []byte("package broken\n"), 0o600))

	_, err := settling(t, root).Reconcile(citypes.ReconcileInput{
		Manager:   "github",
		Resources: []citypes.Resource{fileContent("member/.github/workflows/ci.yaml", "on: push\n")},
	})
	require.NoError(t, err)

	assert.Contains(t, out(t, member, "status", "--porcelain"), "half-finished.go",
		"it is still uncommitted, exactly where its author left it")
	assert.NotContains(t, out(t, member, "show", "--name-only", "HEAD"), "half-finished.go")
}

// A checkout with no origin is a legitimate state, not a failure. The commit
// still happens and the report says why nothing was pushed.
func TestASettleWithNoRemoteCommitsAndSaysSo(t *testing.T) {
	root := t.TempDir()
	member := filepath.Join(root, "member")
	require.NoError(t, os.MkdirAll(member, 0o750))

	run(t, member, "init", "-q", "-b", "main")
	require.NoError(t, os.WriteFile(filepath.Join(member, "README"), []byte("x\n"), 0o600))
	run(t, member, "add", "README")
	run(t, member, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "first")

	res, err := settling(t, root).Reconcile(citypes.ReconcileInput{
		Manager:   "github",
		Resources: []citypes.Resource{fileContent("member/.github/workflows/ci.yaml", "on: push\n")},
	})
	require.NoError(t, err)
	assert.True(t, res.Changed)
	assert.Contains(t, res.Actions[len(res.Actions)-1], "nothing was pushed")
	assert.Empty(t, out(t, member, "status", "--porcelain"))
}

// A bootstrap settles like any other reconcile. One rule: a change is made
// durable by whoever made it.
//
// The alternative was leaving eight repos' worth of generated files
// uncommitted for an operator to commit by hand, and resolving the
// bootstrap's own revision over that dirty tree.
func TestABootstrapCommitsAndPushesLikeAnyOtherReconcile(t *testing.T) {
	root, member, remote := workspace(t)

	res, err := settling(t, root).Reconcile(citypes.ReconcileInput{
		Manager:   "github",
		Bootstrap: true,
		Resources: []citypes.Resource{fileContent("member/.github/workflows/ci.yaml", "on: push\n")},
	})
	require.NoError(t, err)
	assert.True(t, res.Changed)

	assert.Empty(t, out(t, member, "status", "--porcelain"),
		"so the revision this bootstrap goes on to resolve hashes a committed tree")
	assert.Contains(t, out(t, remote, "show", "main:.github/workflows/ci.yaml"), "on: push")
}

// A dry run reads everything and writes nothing, anywhere. It still reports
// what would change, because a plan is only worth reading if it came from
// the same comparison the real run makes.
func TestADryRunWritesNothingAndStillSaysWhatWouldChange(t *testing.T) {
	root, member, _ := workspace(t)

	head := out(t, member, "rev-parse", "HEAD")

	res, err := settling(t, root).Reconcile(citypes.ReconcileInput{
		Manager:   "github",
		DryRun:    true,
		Resources: []citypes.Resource{fileContent("member/.github/workflows/ci.yaml", "on: push\n")},
	})
	require.NoError(t, err)

	assert.True(t, res.Changed, "the plan says this run would change something")
	assert.Equal(t, []string{"would converge file member/.github/workflows/ci.yaml"}, res.Actions)

	require.NoFileExists(t, filepath.Join(member, ".github/workflows/ci.yaml"))
	assert.Equal(t, head, out(t, member, "rev-parse", "HEAD"), "and nothing was committed")
	assert.Empty(t, out(t, member, "status", "--porcelain"))
}

// The promise a plan makes: run it twice and it says the same thing, because
// it changed nothing the second reading could see.
func TestADryRunIsRepeatable(t *testing.T) {
	root, _, _ := workspace(t)

	in := citypes.ReconcileInput{
		Manager:   "github",
		DryRun:    true,
		Resources: []citypes.Resource{fileContent("member/.github/workflows/ci.yaml", "on: push\n")},
	}

	first, err := settling(t, root).Reconcile(in)
	require.NoError(t, err)

	in.Owned = first.Owned

	second, err := settling(t, root).Reconcile(in)
	require.NoError(t, err)
	assert.Equal(t, first.Actions, second.Actions)
}

// A declared file the repo ignores can never be made durable, so every run
// would rewrite it, report a change and stop: an infinite stop loop. That is
// loud, and the message names the fix.
func TestASettleFailsLoudWhenADeclaredPathIsIgnored(t *testing.T) {
	root, member, _ := workspace(t)

	require.NoError(t, os.WriteFile(filepath.Join(member, ".gitignore"), []byte("/generated/\n"), 0o600))
	run(t, member, "add", ".gitignore")
	run(t, member, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "ignore")

	_, err := settling(t, root).Reconcile(citypes.ReconcileInput{
		Manager:   "github",
		Resources: []citypes.Resource{fileContent("member/generated/out.yaml", "x\n")},
	})
	require.ErrorContains(t, err, "settling what changed")
	require.ErrorContains(t, err, ".gitignore matches this record path")
}

// A file deleted by hand and rewritten to exactly what HEAD carries stages
// nothing. There is no commit to make, and reporting one would be a lie in
// the log an operator reads to find out what the pipeline did.
func TestASettleSaysNothingHappenedWhenTheRestoredFileMatchesHead(t *testing.T) {
	root, member, _ := workspace(t)

	res := []citypes.Resource{fileContent("member/.github/workflows/ci.yaml", "on: push\n")}

	first, err := settling(t, root).Reconcile(citypes.ReconcileInput{Manager: "github", Resources: res})
	require.NoError(t, err)
	require.True(t, first.Changed)

	require.NoError(t, os.Remove(filepath.Join(member, ".github/workflows/ci.yaml")))

	second, err := settling(t, root).Reconcile(citypes.ReconcileInput{
		Manager: "github", Resources: res, Owned: first.Owned,
	})
	require.NoError(t, err)
	assert.Contains(t, second.Actions[len(second.Actions)-1], "nothing to commit")
}

// A converged path outside any checkout has nothing to commit it to, and
// that is not an error: it is on disk, which for a path nobody tracks is as
// durable as it gets.
func TestASettleIgnoresAPathInNoCheckout(t *testing.T) {
	root := t.TempDir()

	res, err := settling(t, root).Reconcile(citypes.ReconcileInput{
		Manager:   "github",
		Resources: []citypes.Resource{fileContent("loose/file.yaml", "x\n")},
	})
	require.NoError(t, err)
	assert.True(t, res.Changed, "the file was written, so the run still stops")
	require.FileExists(t, filepath.Join(root, "loose/file.yaml"))
}

// The case the untracked test above cannot reach. An untracked file is not in
// the index, so a pathspec-less `git commit -m` never took it; a STAGED one
// it took every time, and the settle then pushed it. Somebody's half-finished
// work, published under the pipeline's name, to a remote.
func TestASettleLeavesAStagedFileOfSomebodyElsesAlone(t *testing.T) {
	root, member, _ := workspace(t)

	mine := filepath.Join(member, "half-finished.go")
	require.NoError(t, os.WriteFile(mine, []byte("package broken\n"), 0o600))
	out(t, member, "add", "half-finished.go")

	_, err := settling(t, root).Reconcile(citypes.ReconcileInput{
		Manager:   "github",
		Resources: []citypes.Resource{fileContent("member/.github/workflows/ci.yaml", "on: push\n")},
	})
	require.NoError(t, err)

	assert.NotContains(t, out(t, member, "show", "--name-only", "HEAD"), "half-finished.go",
		"a staged file belongs to whoever staged it, not to the pipeline's commit")
	assert.Contains(t, out(t, member, "diff", "--cached", "--name-only"), "half-finished.go",
		"and it is still staged, exactly where its author left it")
}

// The other half of the same defect. settleRepo asks whether what it staged
// is worth committing; asked about the WHOLE index it answered yes because of
// somebody else's staged file, and committed on the strength of it.
//
// The file the reconcile converged is deleted and restored to exactly what
// HEAD carries, so the settle's own `git add` stages nothing. Only the
// human's file is in the index.
func TestASettleStagingNothingOfItsOwnIgnoresSomebodyElsesIndex(t *testing.T) {
	root, member, _ := workspace(t)

	res := []citypes.Resource{fileContent("member/.github/workflows/ci.yaml", "on: push\n")}

	first, err := settling(t, root).Reconcile(citypes.ReconcileInput{Manager: "github", Resources: res})
	require.NoError(t, err)
	require.True(t, first.Changed)

	head := out(t, member, "rev-parse", "HEAD")

	require.NoError(t, os.Remove(filepath.Join(member, ".github/workflows/ci.yaml")))

	mine := filepath.Join(member, "half-finished.go")
	require.NoError(t, os.WriteFile(mine, []byte("package broken\n"), 0o600))
	out(t, member, "add", "half-finished.go")

	second, err := settling(t, root).Reconcile(citypes.ReconcileInput{
		Manager: "github", Resources: res, Owned: first.Owned,
	})
	require.NoError(t, err)

	assert.Contains(t, second.Actions[len(second.Actions)-1], "nothing to commit",
		"the settle staged nothing of its own, whatever else is in the index")
	assert.Equal(t, head, out(t, member, "rev-parse", "HEAD"))
	assert.Contains(t, out(t, member, "diff", "--cached", "--name-only"), "half-finished.go",
		"and the human's file is untouched, still staged")
}
