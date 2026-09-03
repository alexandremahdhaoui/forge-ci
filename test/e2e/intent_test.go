//go:build e2e

package e2e_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// releasingRepo stands up one member with a bare origin and a pipeline that
// releases it, bootstrapped and applied once, so a test starts from a
// released v0.1.0 and asks what the next apply does.
func releasingRepo(t *testing.T, fake *releaseFake, versioning string) (root, repo, origin string) {
	t.Helper()

	root = t.TempDir()
	repo = filepath.Join(root, "demo-repo")
	statePath := filepath.Join(root, "state")

	require.NoError(t, os.MkdirAll(repo, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "forge.yaml"), []byte(releaseForgeYAML), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".envrc"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".gitignore"),
		[]byte("/.forge/\n.envrc\n/build/\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("one"), 0o600))

	mustRun(t, repo, "git", "init", "-b", "main")
	mustRun(t, repo, "git", "config", "user.email", "e2e@example.com")
	mustRun(t, repo, "git", "config", "user.name", "e2e")
	mustRun(t, repo, "git", "add", ".")
	mustRun(t, repo, "git", "commit", "-m", "feat: first")

	origin = filepath.Join(root, "remotes", "owner", "demo-repo.git")
	require.NoError(t, os.MkdirAll(origin, 0o750))
	mustRun(t, origin, "git", "init", "--bare")
	mustRun(t, origin, "git", "symbolic-ref", "HEAD", "refs/heads/main")
	mustRun(t, repo, "git", "remote", "add", "origin", origin)
	mustRun(t, repo, "git", "push", "origin", "main")

	yaml := strings.Replace(releasePipelineYAML(root, statePath, fake.server.URL), "name: demo\n", "name: demo\n"+versioning, 1)
	require.NoError(t, os.WriteFile(filepath.Join(root, "forge-ci.yaml"), []byte(yaml), 0o600))

	mustRun(t, root, "forge-ci", "bootstrap", "--config", "forge-ci.yaml", "--root", ".")
	mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".")
	require.Equal(t, "v0.1.0", fake.releases["owner/demo-repo"])

	return root, repo, origin
}

// The rerun that killed golden run 19: a checkout WITH tags, so the tag
// line answers v0.1.0 and a derivation would say v0.1.1. The release record
// answers first, the run is skipped, and the remote still carries exactly
// one tag.
func TestARerunOfAReleasedRevisionReusesItsVersion(t *testing.T) {
	fake := newReleaseFake(t)
	root, repo, origin := releasingRepo(t, fake, "")

	require.NoError(t, os.RemoveAll(repo))
	mustRun(t, root, "git", "clone", origin, "demo-repo")
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".envrc"), nil, 0o600))
	require.Contains(t, mustRun(t, repo, "git", "tag"), "v0.1.0", "this clone carries the tag, or the test proves nothing")

	out := mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".")
	require.Contains(t, out, "skipped: This revision was released as v0.1.0. Nothing to do.")
	require.Contains(t, out, "evaluate: skip")

	require.Equal(t, []string{"v0.1.0"}, strings.Fields(mustRun(t, origin, "git", "tag")))
	require.Equal(t, "v0.1.0", fake.releases["owner/demo-repo"])
	require.Empty(t, fake.drafts, "nothing was even drafted")
}

// A commit the vocabulary ignores is a new revision with nothing to
// release. The run ends at the evaluation, before any build, and the next fix
// releases the patch.
func TestADocsOnlyCommitSkipsBeforeAnyBuildAndTheNextFixReleases(t *testing.T) {
	fake := newReleaseFake(t)
	root, repo, origin := releasingRepo(t, fake, `versioning:
  strategy: semantic
  semantic:
    patch: ["fix:"]
    ignore: ["docs:"]
`)

	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("two"), 0o600))
	mustRun(t, repo, "git", "commit", "-am", "docs: explain the door")

	out := mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".")
	require.Contains(t, out, `skipped: Nothing to release. 1 commit since v0.1.0, and it is a kind that never releases: "docs: explain the door" in demo-repo.`)
	require.NotContains(t, out, "stage build", "a skipped run builds nothing")
	require.Equal(t, []string{"v0.1.0"}, strings.Fields(mustRun(t, origin, "git", "tag")))

	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("three"), 0o600))
	mustRun(t, repo, "git", "commit", "-am", "fix: the door")

	out = mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".")
	require.Contains(t, out, "stage build")
	require.Equal(t, "v0.1.1", fake.releases["owner/demo-repo"])
	require.ElementsMatch(t, []string{"v0.1.0", "v0.1.1"}, strings.Fields(mustRun(t, origin, "git", "tag")))
}

// unmatched: error fails the run before anything builds, naming the
// subject, and nothing is tagged.
func TestAnUnclaimedSubjectFailsFast(t *testing.T) {
	fake := newReleaseFake(t)
	root, repo, origin := releasingRepo(t, fake, `versioning:
  strategy: semantic
  semantic:
    minor: ["feat:"]
    patch: ["fix:"]
    ignore: ["docs:"]
    unmatched: error
`)

	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("two"), 0o600))
	mustRun(t, repo, "git", "commit", "-am", "pushed from the train")

	out, err := run(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".")
	require.Error(t, err)
	require.Contains(t, out, "pushed from the train")
	require.NotContains(t, out, "stage build")
	require.Equal(t, []string{"v0.1.0"}, strings.Fields(mustRun(t, origin, "git", "tag")))

	// The fix is a good commit on top. The old one stays, scores patch, and
	// the run releases.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("three"), 0o600))
	mustRun(t, repo, "git", "commit", "-am", "docs: say it properly")

	out = mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".")
	require.Contains(t, out, "stage build")
	require.Equal(t, "v0.1.1", fake.releases["owner/demo-repo"])
}

// The phases, one process each, reach the same release the whole apply
// reaches, and the evaluate phase prints the word a rendered workflow reads.
func TestThePhasesReachTheSameReleaseAsOneApply(t *testing.T) {
	fake := newReleaseFake(t)
	root, repo, origin := releasingRepo(t, fake, "")

	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("two"), 0o600))
	mustRun(t, repo, "git", "commit", "-am", "fix: the door")

	mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".", "--phase", "self-reconcile")

	out := mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".", "--phase", "evaluate")
	require.True(t, strings.HasSuffix(strings.TrimSpace(out), "evaluate: proceed"), out)
	require.Contains(t, out, "Release v0.1.1 (patch).")

	out = mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".",
		"--phase", "stages", "--stage", "build")
	require.Contains(t, out, "stage build")
	require.Equal(t, "v0.1.0", fake.releases["owner/demo-repo"], "the build stage releases nothing")

	// The stage that publishes reads the recorded file back through the
	// engine that kept it, so the build output can go the way a runner's
	// disk goes. Only the recorded artifact: a spec.assets glob names files
	// no record carries, and those still live where the release runs.
	require.NoError(t, os.Remove(filepath.Join(repo, "build", "dist", "demo-tool_linux_amd64")))

	mustRun(t, root, "forge-ci", "apply", "--config", "forge-ci.yaml", "--root", ".",
		"--phase", "stages", "--stage", "release")
	require.Equal(t, "v0.1.1", fake.releases["owner/demo-repo"])
	require.ElementsMatch(t, []string{"v0.1.0", "v0.1.1"}, strings.Fields(mustRun(t, origin, "git", "tag")))
	require.Contains(t, string(fake.assets["demo-tool_linux_amd64"]), "demo-tool works")
}

// The stages phase cut as a compute engine renders it with one job per
// substage: the substage runs alone, the promotion reads its record, and the
// stage that publishes reads the file the substage job kept. Run out of
// order, a job refuses by name.
func TestTheStageJobsReachTheSameReleaseAsOneApply(t *testing.T) {
	fake := newReleaseFake(t)
	root, repo, origin := releasingRepo(t, fake, "")

	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("two"), 0o600))
	mustRun(t, repo, "git", "commit", "-am", "fix: the door")

	apply := func(args ...string) (string, error) {
		return run(t, root, "forge-ci", append([]string{"apply", "--config", "forge-ci.yaml", "--root", "."}, args...)...)
	}

	_, err := apply("--phase", "self-reconcile")
	require.NoError(t, err)

	_, err = apply("--phase", "evaluate")
	require.NoError(t, err)

	out, err := apply("--phase", "stages", "--stage", "build", "--promote")
	require.Error(t, err, "the promotion over a stage nothing ran is refused")
	require.Contains(t, out, `substage "default"`)

	out, err = apply("--phase", "stages", "--stage", "build", "--substage", "default")
	require.NoError(t, err, out)
	require.Contains(t, out, "stage build")

	out, err = apply("--phase", "stages", "--stage", "build", "--promote")
	require.NoError(t, err, out)
	require.Equal(t, "v0.1.0", fake.releases["owner/demo-repo"], "the promotion releases nothing")

	require.NoError(t, os.Remove(filepath.Join(repo, "build", "dist", "demo-tool_linux_amd64")))

	_, err = apply("--phase", "stages", "--stage", "release", "--substage", "publish")
	require.NoError(t, err)
	require.Equal(t, "v0.1.1", fake.releases["owner/demo-repo"])
	require.ElementsMatch(t, []string{"v0.1.0", "v0.1.1"}, strings.Fields(mustRun(t, origin, "git", "tag")))
	require.Contains(t, string(fake.assets["demo-tool_linux_amd64"]), "demo-tool works")
}

// Run 27's shape, with real git repos and real processes.
//
// A stage publishes into one of the pipeline's own repos, so the commit it
// writes moves that repo's HEAD and every later phase, cloning fresh, would
// derive a revision this run never wrote a record under. Handed the revision
// the evaluate phase reported, the later phases answer for the run instead of
// for the checkout, and the release lands.
func TestAPhaseCarriesOnAfterAStageMovedAPipelineRepo(t *testing.T) {
	fake := newReleaseFake(t)
	root, repo, origin := releasingRepo(t, fake, "")

	require.NoError(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("two"), 0o600))
	mustRun(t, repo, "git", "commit", "-am", "fix: the door")

	apply := func(args ...string) (string, error) {
		return run(t, root, "forge-ci", append([]string{"apply", "--config", "forge-ci.yaml", "--root", "."}, args...)...)
	}

	_, err := apply("--phase", "self-reconcile")
	require.NoError(t, err)

	out, err := apply("--phase", "evaluate")
	require.NoError(t, err, out)

	revision := revisionFrom(t, out)

	out, err = apply("--phase", "stages", "--stage", "build")
	require.NoError(t, err, out)

	// The stage did its job: a commit lands in a repo the revision hashes.
	// From here a phase measuring its own checkout answers for another run.
	require.NoError(t, os.WriteFile(filepath.Join(repo, "published.txt"), []byte("catalog"), 0o600))
	mustRun(t, repo, "git", "add", "published.txt")
	mustRun(t, repo, "git", "commit", "-m", "forge-ci: publish")

	out, err = apply("--phase", "stages", "--stage", "release")
	require.Error(t, err, "without the revision, the stage looks for a record nothing wrote")
	require.Contains(t, out, "no evaluation recorded")

	out, err = apply("--phase", "stages", "--stage", "release", "--revision", revision)
	require.NoError(t, err, out)
	require.Equal(t, "v0.1.1", fake.releases["owner/demo-repo"])
	require.ElementsMatch(t, []string{"v0.1.0", "v0.1.1"}, strings.Fields(mustRun(t, origin, "git", "tag")))
}

// revisionFrom reads the revision off the first line of a report, the same
// line a rendered workflow captures as the evaluate job's output.
func revisionFrom(t *testing.T, report string) string {
	t.Helper()

	for _, line := range strings.Split(report, "\n") {
		if rest, ok := strings.CutPrefix(line, "revision "); ok {
			return strings.TrimSpace(rest)
		}
	}

	t.Fatalf("no revision line in the report:\n%s", report)

	return ""
}
