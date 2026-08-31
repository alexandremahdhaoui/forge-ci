package gitadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/gitident"
)

type Git interface {
	Init(ctx context.Context, dir string) error
	Clone(ctx context.Context, url, dir string) error
	IsRepo(ctx context.Context, dir string) (bool, error)
	Add(ctx context.Context, dir, path string) error
	// Commit records what paths hold. Callers pass the paths they staged:
	// a pathspec-less commit takes the whole index, including a human's.
	Commit(ctx context.Context, dir, message string, paths ...string) error
	HeadSHA(ctx context.Context, dir string) (string, error)
	RemoteSHA(ctx context.Context, url, ref string) (string, error)
	// Staged reports whether these paths have anything waiting in the index.
	// Scoped for the same reason Commit is.
	Staged(ctx context.Context, dir string, paths ...string) (bool, error)
	LatestTag(ctx context.Context, dir, prefix string) (string, error)
	SubjectsSince(ctx context.Context, dir, tag string) ([]string, error)
	WorktreeHash(ctx context.Context, dir string) (string, error)
	// Tag points a tag at one commit and pushes it.
	Tag(ctx context.Context, dir, tag, sha string) error
	// TagAt answers the commit a tag points at, and whether dir carries the
	// tag at all.
	TagAt(ctx context.Context, dir, tag string) (string, bool, error)
	// RemoteTagAt answers where origin's copy of the tag points, and whether
	// origin carries it. The local list is not the authority: a CI checkout
	// is a fresh clone with no tags, so only the remote knows what was
	// already published. An unreachable remote is an error, never "absent".
	RemoteTagAt(ctx context.Context, dir, tag string) (string, bool, error)
	// HasRemote reports whether dir has an origin to push to. A checkout
	// with none is a legitimate state, not a failure.
	HasRemote(ctx context.Context, dir string) (bool, error)
	// Branch answers the branch HEAD is on. A detached HEAD answers an
	// empty string, which nothing can push.
	Branch(ctx context.Context, dir string) (string, error)
	// Push sends the branch to origin. A rejected push is ErrRejected: the
	// remote moved, and only a human can decide what wins.
	Push(ctx context.Context, dir, branch string) error
	// PullRebase fetches origin and replays local commits on top. It is how
	// a store whose records are one file each recovers from a concurrent
	// run without anybody choosing a winner.
	PullRebase(ctx context.Context, dir, branch string) error
}

// ErrNoRemote marks a checkout with no origin. It is not a failure: a
// workspace on a laptop has one, a scratch fixture does not, and a manager
// that settles by pushing simply has nowhere to send it.
var ErrNoRemote = errors.New("the checkout has no origin to push to")

// ErrRejected marks a push the remote refused, which is a remote that moved
// under this run. Merging is somebody's decision and never the pipeline's,
// so this is fatal and stays fatal.
var ErrRejected = errors.New("the remote rejected the push")

type CLI struct {
	runner execadapter.Runner
}

var _ Git = (*CLI)(nil)

// semverTag is the tag shape the version rule reads: strict vMAJOR.MINOR.PATCH
// with an optional prerelease. It is the same expression artifactcontroller
// parses with, because a tag this does not match is a tag that cannot become
// the next version.
var semverTag = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$`)

func New(runner execadapter.Runner) *CLI {
	return &CLI{runner: runner}
}

func (g *CLI) run(ctx context.Context, dir, what string, args ...string) (execadapter.Result, error) {
	res, err := g.runner.Run(ctx, dir, "git", args...)
	if err != nil {
		return res, fmt.Errorf("%s: %w", what, err)
	}

	if res.ExitCode != 0 {
		return res, fmt.Errorf("%s: git exited %d: %s", what, res.ExitCode, strings.TrimSpace(res.Stderr))
	}

	return res, nil
}

func (g *CLI) Init(ctx context.Context, dir string) error {
	_, err := g.run(ctx, dir, "initialising "+dir, "init", "-b", "main")

	return err
}

func (g *CLI) Clone(ctx context.Context, url, dir string) error {
	_, err := g.run(ctx, "", "cloning "+url, "clone", url, dir)

	return err
}

func (g *CLI) IsRepo(ctx context.Context, dir string) (bool, error) {
	res, err := g.runner.Run(ctx, dir, "git", "rev-parse", "--git-dir")
	if err != nil {
		return false, fmt.Errorf("inspecting %s: %w", dir, err)
	}

	return res.ExitCode == 0, nil
}

func (g *CLI) Add(ctx context.Context, dir, path string) error {
	res, err := g.run(ctx, dir, "staging "+path, "add", path)

	// Record keys embed caller-chosen names (a stage called "build", say),
	// so an unanchored output pattern in the state repo's .gitignore can
	// swallow a record path. Name the fix, or the refusal reads like a
	// broken engine.
	if err != nil && strings.Contains(res.Stderr, ".gitignore") {
		return fmt.Errorf(
			"%w - the repo's .gitignore matches this record path; anchor output patterns to the repo root (write /build/, not build/)",
			err)
	}

	return err
}

// Commit records what these paths hold. A commit writes a git object, so it
// needs an identity the host may not have: see gitident.
//
// The pathspec is not decoration. A bare `git commit -m` commits the whole
// index, so a developer with anything staged had their work published under
// the pipeline's name and pushed. Both callers stage an explicit list and
// both were undone by this; passing that same list here is what makes the
// promise true.
func (g *CLI) Commit(ctx context.Context, dir, message string, paths ...string) error {
	args := append(gitident.Args(ctx, g.runner, dir), "commit", "-m", message)
	args = append(args, pathspec(paths)...)

	_, err := g.run(ctx, dir, "committing in "+dir, args...)

	return err
}

// Staged reports whether these paths have anything waiting in the index.
//
// It is scoped for the same reason Commit is. Asked about the whole index it
// answered yes because of somebody else's staged file, and the commit that
// followed swept that file up.
func (g *CLI) Staged(ctx context.Context, dir string, paths ...string) (bool, error) {
	args := append([]string{"diff", "--cached", "--name-only"}, pathspec(paths)...)

	res, err := g.run(ctx, dir, "reading the index of "+dir, args...)
	if err != nil {
		return false, err
	}

	return strings.TrimSpace(res.Stdout) != "", nil
}

// pathspec renders paths as a git pathspec. No paths means the whole tree,
// which both callers must avoid, so they always pass what they staged.
func pathspec(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}

	return append([]string{"--"}, paths...)
}

func (g *CLI) HeadSHA(ctx context.Context, dir string) (string, error) {
	res, err := g.run(ctx, dir, "reading HEAD of "+dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(res.Stdout), nil
}

func (g *CLI) RemoteSHA(ctx context.Context, url, ref string) (string, error) {
	if ref == "" {
		ref = "main"
	}

	res, err := g.run(ctx, "", "reading "+ref+" of "+url, "ls-remote", url, "refs/heads/"+ref)
	if err != nil {
		return "", err
	}

	fields := strings.Fields(res.Stdout)
	if len(fields) == 0 {
		return "", fmt.Errorf("reading %s of %s: no such ref", ref, url)
	}

	return fields[0], nil
}

func (g *CLI) WorktreeHash(ctx context.Context, dir string) (string, error) {
	status, err := g.run(ctx, dir, "reading status of "+dir, "status", "--porcelain")
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(status.Stdout) == "" {
		return "", nil
	}

	diff, err := g.run(ctx, dir, "reading uncommitted changes in "+dir, "diff", "HEAD")
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256([]byte(status.Stdout + "\n" + diff.Stdout))

	return hex.EncodeToString(sum[:])[:12], nil
}

// LatestTag is the highest version this prefix's line carries, without the
// prefix. A repo with no tag on the line answers empty rather than failing,
// because a workspace that has never released has nothing to report.
//
// The prefix is a namespace. Reading a tag from another prefix would make two
// factories that share a repo take turns overwriting each other's line, so a
// tag that does not carry this prefix is not this line's history and is
// skipped.
func (g *CLI) LatestTag(ctx context.Context, dir, prefix string) (string, error) {
	pattern := "v[0-9]*"
	if prefix != "" {
		pattern = prefix + "-v[0-9]*"
	}

	res, err := g.run(ctx, dir, "listing tags", "tag", "--sort=-v:refname", "--list", pattern)
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(res.Stdout, "\n") {
		tag := strings.TrimSpace(line)

		if prefix != "" {
			rest, ok := strings.CutPrefix(tag, prefix+"-")
			if !ok {
				continue
			}

			tag = rest
		}

		// Only a tag the version rule can read. git sorts every tag it
		// has, so one legacy tag sorting above the release line - "v1.2",
		// "wip" - was returned as the previous version, and the whole
		// release then failed on "not a semver tag", for every member.
		// A repo that carries release history from before this pipeline
		// existed is exactly the case this has to survive.
		if semverTag.MatchString(tag) {
			return tag, nil
		}
	}

	return "", nil
}

// SubjectsSince is every commit subject a checkout gained after tag. A tag the
// repo does not carry answers the whole history, which is the right answer for
// a member joining a line that already exists: all of it is new to that line.
func (g *CLI) SubjectsSince(ctx context.Context, dir, tag string) ([]string, error) {
	args := []string{"log", "--no-merges", "--format=%s"}

	if strings.TrimSpace(tag) != "" {
		known, err := g.run(ctx, dir, "reading tag", "tag", "--list", tag)
		if err != nil {
			return nil, err
		}

		if strings.TrimSpace(known.Stdout) != "" {
			args = append(args, tag+"..HEAD")
		}
	}

	res, err := g.run(ctx, dir, "reading commit subjects", args...)
	if err != nil {
		return nil, err
	}

	out := []string{}

	for _, line := range strings.Split(res.Stdout, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}

	return out, nil
}

// Tag converges origin's tag onto one commit. It decides on the union of
// local and remote state, because the local list alone lied: a CI checkout
// is a fresh clone with no tags, so a re-run of an already-published
// revision read "absent", re-created the tag, and the push was rejected by
// the only party that actually knew.
//
//   - remote carries the tag at sha: convergence, a no-op.
//   - remote carries it elsewhere: refused. A moved tag changes what a
//     consumer already pinned.
//   - remote absent: create the tag if the checkout lacks it, then push.
//     A local tag already at sha is an interrupted run's leftover, and the
//     push is what finishes that job.
//
// A checkout with no origin skips the remote read and the push: a scratch
// repo is a legitimate state, and a test fixture is always in it.
func (g *CLI) Tag(ctx context.Context, dir, tag, sha string) error {
	hasRemote, err := g.HasRemote(ctx, dir)
	if err != nil {
		return err
	}

	if hasRemote {
		remoteAt, onRemote, err := g.RemoteTagAt(ctx, dir, tag)
		if err != nil {
			// Never fall through to "absent": re-creating the tag on a
			// remote this run could not read is the exact mistake reading
			// the remote exists to remove.
			return err
		}

		if onRemote {
			if remoteAt == sha {
				return nil
			}

			return fmt.Errorf(
				"tagging %s: %s already points at %s on origin, not %s, and a tag is never moved",
				dir, tag, remoteAt, sha)
		}
	}

	localAt, exists, err := g.TagAt(ctx, dir, tag)
	if err != nil {
		return err
	}

	if exists && localAt != sha {
		return fmt.Errorf(
			"tagging %s: %s already points at %s, not %s, and a tag is never moved",
			dir, tag, localAt, sha)
	}

	if !exists {
		// Annotated, with the tag as its message: a lightweight tag dies with
		// "no tag message" on a machine whose global config signs tags, while an
		// annotated tag works signed and unsigned alike.
		//
		// An annotated tag is an object git writes, so it needs a committer
		// identity exactly as a commit does, and a runner has none.
		args := append(gitident.Args(ctx, g.runner, dir), "tag", "-m", tag, tag, sha)

		if _, err := g.run(ctx, dir, "tagging "+tag, args...); err != nil {
			return err
		}
	}

	if !hasRemote {
		return nil
	}

	_, err = g.run(ctx, dir, "pushing "+tag, "push", "origin", tag)

	return err
}

// RemoteTagAt reads origin's copy of one tag with ls-remote, asking for the
// peeled ^{} ref alongside the tag so an annotated tag answers the commit it
// wraps rather than its own object. Absent is the ordinary first-release
// case; a remote that cannot be read is an error, because treating it as
// absent would re-create a tag that may already exist.
func (g *CLI) RemoteTagAt(ctx context.Context, dir, tag string) (string, bool, error) {
	res, err := g.run(ctx, dir, "reading tag "+tag+" of origin",
		"ls-remote", "origin", "refs/tags/"+tag, "refs/tags/"+tag+"^{}")
	if err != nil {
		return "", false, err
	}

	var plain, peeled string

	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		if strings.HasSuffix(fields[1], "^{}") {
			peeled = fields[0]
		} else {
			plain = fields[0]
		}
	}

	if peeled != "" {
		return peeled, true, nil
	}

	if plain != "" {
		return plain, true, nil
	}

	return "", false, nil
}

func (g *CLI) HasRemote(ctx context.Context, dir string) (bool, error) {
	res, err := g.runner.Run(ctx, dir, "git", "remote", "get-url", "origin")
	if err != nil {
		return false, fmt.Errorf("reading the origin of %s: %w", dir, err)
	}

	return res.ExitCode == 0 && strings.TrimSpace(res.Stdout) != "", nil
}

func (g *CLI) Branch(ctx context.Context, dir string) (string, error) {
	res, err := g.run(ctx, dir, "reading the branch of "+dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}

	name := strings.TrimSpace(res.Stdout)
	if name == "HEAD" {
		// A detached HEAD is what a CI checkout of a tag looks like. There
		// is no branch to push, and inventing one would put the commit
		// somewhere nobody asked for.
		return "", nil
	}

	return name, nil
}

func (g *CLI) Push(ctx context.Context, dir, branch string) error {
	res, err := g.runner.Run(ctx, dir, "git", "push", "origin", "HEAD:"+branch)
	if err != nil {
		return fmt.Errorf("pushing %s of %s: %w", branch, dir, err)
	}

	if res.ExitCode != 0 {
		out := strings.TrimSpace(res.Stderr)
		if strings.Contains(out, "rejected") || strings.Contains(out, "non-fast-forward") {
			return fmt.Errorf("pushing %s of %s: %w: %s", branch, dir, ErrRejected, out)
		}

		return fmt.Errorf("pushing %s of %s: git exited %d: %s", branch, dir, res.ExitCode, out)
	}

	return nil
}

// PullRebase replays this checkout's commits on top of origin's.
//
// It carries an identity for the same reason Commit does: a rebase writes
// commit objects, and a runner has no ambient one.
func (g *CLI) PullRebase(ctx context.Context, dir, branch string) error {
	if _, err := g.run(ctx, dir, "fetching origin in "+dir, "fetch", "origin", branch); err != nil {
		return err
	}

	args := append(gitident.Args(ctx, g.runner, dir), "rebase", "origin/"+branch)

	_, err := g.run(ctx, dir, "rebasing "+dir+" onto origin/"+branch, args...)

	return err
}

// TagAt answers where a tag points, and whether the repo carries it. A repo
// that does not have the tag is not an error: that is the ordinary case, and
// it is what a first release of a member looks like.
func (g *CLI) TagAt(ctx context.Context, dir, tag string) (string, bool, error) {
	listed, err := g.run(ctx, dir, "reading tag "+tag, "tag", "--list", tag)
	if err != nil {
		return "", false, err
	}

	if strings.TrimSpace(listed.Stdout) == "" {
		return "", false, nil
	}

	// An annotated tag's own object is not the commit, so rev-list asks for
	// what it points at. It is a no-op on a lightweight tag.
	res, err := g.run(ctx, dir, "resolving tag "+tag, "rev-list", "-n", "1", tag)
	if err != nil {
		return "", false, err
	}

	return strings.TrimSpace(res.Stdout), true, nil
}
