package releaseadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
)

// Publisher puts a release where people can fetch it.
type Publisher interface {
	Tag(ctx context.Context, dir, version, sha string) error
	// LatestTag answers the highest semver tag dir carries, "" when none.
	LatestTag(ctx context.Context, dir string) (string, error)
	// TaggedAt answers whether any tag already points at sha in dir.
	TaggedAt(ctx context.Context, dir, sha string) (bool, error)
	// Release creates the release named by tag in dir's repo, attaches the
	// files, and answers its URL.
	Release(ctx context.Context, dir, tag string, files []string) (string, error)
}

// GH shells out to the GitHub CLI. It is the only thing here that reaches the
// outside world, and it holds no decision about what to publish.
type GH struct {
	runner execadapter.Runner
}

var _ Publisher = (*GH)(nil)

// semverTag is the tag shape the version rule reads: strict vMAJOR.MINOR.PATCH
// with an optional prerelease. It is the same expression artifactcontroller
// parses with, because a tag this does not match is a tag that cannot become
// the next version.
var semverTag = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$`)

func New(runner execadapter.Runner) *GH {
	return &GH{runner: runner}
}

func (g *GH) run(ctx context.Context, dir, what, name string, args ...string) (string, error) {
	res, err := g.runner.Run(ctx, dir, name, args...)
	if err != nil {
		return "", fmt.Errorf("%s: %w", what, err)
	}

	if res.ExitCode != 0 {
		return "", fmt.Errorf("%s: exit %d: %s", what, res.ExitCode, strings.TrimSpace(res.Stderr))
	}

	return strings.TrimSpace(res.Stdout), nil
}

// Tag points a version at one commit and pushes it. It is idempotent by
// refusing a tag that already exists rather than moving it, because a moved tag
// changes what a consumer already pinned.
func (g *GH) Tag(ctx context.Context, dir, version, sha string) error {
	existing, err := g.run(ctx, dir, "listing tags", "git", "tag", "--list", version)
	if err != nil {
		return err
	}

	if existing != "" {
		return fmt.Errorf("tagging %s: %s already exists and a tag is never moved", dir, version)
	}

	// Annotated, with the version as its message: a lightweight tag dies
	// with "no tag message" on a machine whose global config signs tags,
	// while an annotated tag works signed and unsigned alike.
	if _, err := g.run(ctx, dir, "tagging "+version, "git", "tag", "-m", version, version, sha); err != nil {
		return err
	}

	_, err = g.run(ctx, dir, "pushing "+version, "git", "push", "origin", version)

	return err
}

// LatestTag answers the highest semver tag the repo carries. A repo that
// has never been tagged answers empty, and its next version starts fresh.
func (g *GH) LatestTag(ctx context.Context, dir string) (string, error) {
	out, err := g.run(ctx, dir, "listing tags of "+dir, "git", "tag", "--sort=-v:refname")
	if err != nil {
		return "", err
	}

	for _, line := range strings.Split(out, "\n") {
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

// TaggedAt answers whether any tag already points at the commit: a re-run
// of an already-released revision must not stack a second tag on it.
func (g *GH) TaggedAt(ctx context.Context, dir, sha string) (bool, error) {
	out, err := g.run(ctx, dir, "reading tags at "+sha, "git", "tag", "--points-at", sha)
	if err != nil {
		return false, err
	}

	return strings.TrimSpace(out) != "", nil
}

// Release creates the release and attaches the files, and returns its URL.
func (g *GH) Release(ctx context.Context, dir, tag string, files []string) (string, error) {
	args := []string{"release", "create", tag, "--generate-notes"}

	for _, f := range files {
		args = append(args, filepath.Clean(f))
	}

	return g.run(ctx, dir, "creating release "+tag, "gh", args...)
}

// DigestFile measures one file: its sha256 hex and size. The index is built
// from these, so it never claims a byte nobody hashed.
func DigestFile(path string) (string, int64, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", 0, fmt.Errorf("digesting %s: %w", path, err)
	}

	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:]), int64(len(data)), nil
}
