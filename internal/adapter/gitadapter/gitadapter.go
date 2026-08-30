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
	Commit(ctx context.Context, dir, message string) error
	HeadSHA(ctx context.Context, dir string) (string, error)
	RemoteSHA(ctx context.Context, url, ref string) (string, error)
	Staged(ctx context.Context, dir string) (bool, error)
	LatestTag(ctx context.Context, dir, prefix string) (string, error)
	SubjectsSince(ctx context.Context, dir, tag string) ([]string, error)
	WorktreeHash(ctx context.Context, dir string) (string, error)
	// Tag points a tag at one commit and pushes it.
	Tag(ctx context.Context, dir, tag, sha string) error
	// TagAt answers the commit a tag points at, and whether dir carries the
	// tag at all.
	TagAt(ctx context.Context, dir, tag string) (string, bool, error)
	// HasRemote reports whether dir has an origin to push to. A checkout
	// with none is a legitimate state, not a failure.
	HasRemote(ctx context.Context, dir string) (bool, error)
	// Branch answers the branch HEAD is on. A detached HEAD answers an
	// empty string, which nothing can push.
	Branch(ctx context.Context, dir string) (string, error)
	// Push sends the branch to origin. A rejected push is ErrRejected: the
	// remote moved, and only a human can decide what wins.
	Push(ctx context.Context, dir, branch string) error
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

// Commit records the staged record. A commit writes a git object, so it
// needs an identity the host may not have: see gitident.
func (g *CLI) Commit(ctx context.Context, dir, message string) error {
	args := append(gitident.Args(ctx, g.runner, dir), "commit", "-m", message)

	_, err := g.run(ctx, dir, "committing in "+dir, args...)

	return err
}

// Staged reports whether anything is in the index waiting to be
// committed. The state engine stages only the record it wrote, so this
// is what decides whether a commit happens - the rest of the tree is
// not the engine's to judge.
func (g *CLI) Staged(ctx context.Context, dir string) (bool, error) {
	res, err := g.run(ctx, dir, "reading the index of "+dir, "diff", "--cached", "--name-only")
	if err != nil {
		return false, err
	}

	return strings.TrimSpace(res.Stdout) != "", nil
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

// Tag points a tag at one commit and pushes it. It is idempotent by refusing
// a tag that already exists rather than moving it, because a moved tag
// changes what a consumer already pinned.
func (g *CLI) Tag(ctx context.Context, dir, tag, sha string) error {
	_, exists, err := g.TagAt(ctx, dir, tag)
	if err != nil {
		return err
	}

	if exists {
		return fmt.Errorf("tagging %s: %s already exists and a tag is never moved", dir, tag)
	}

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

	_, err = g.run(ctx, dir, "pushing "+tag, "push", "origin", tag)

	return err
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
