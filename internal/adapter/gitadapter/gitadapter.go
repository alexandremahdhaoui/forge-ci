package gitadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
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
	LatestTag(ctx context.Context, dir string) (string, error)
	WorktreeHash(ctx context.Context, dir string) (string, error)
}

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

func (g *CLI) Commit(ctx context.Context, dir, message string) error {
	_, err := g.run(ctx, dir, "committing in "+dir, "commit", "-m", message)

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

// LatestTag is the highest semver tag a checkout carries. A repo with no tag
// answers empty rather than failing, because a workspace that has never
// released has nothing to report.
func (g *CLI) LatestTag(ctx context.Context, dir string) (string, error) {
	res, err := g.run(ctx, dir, "listing tags", "tag", "--sort=-v:refname")
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(res.Stdout, "\n") {
		// Only a tag the version rule can read. git sorts every tag it
		// has, so one legacy tag sorting above the release line - "v1.2",
		// "wip" - was returned as the previous version, and the whole
		// release then failed on "not a semver tag", for every member.
		// A repo that carries release history from before this pipeline
		// existed is exactly the case this has to survive.
		if tag := strings.TrimSpace(line); semverTag.MatchString(tag) {
			return tag, nil
		}
	}

	return "", nil
}
