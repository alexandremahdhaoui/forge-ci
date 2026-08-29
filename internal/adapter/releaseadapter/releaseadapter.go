package releaseadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
)

// Publisher puts a release where people can fetch it.
type Publisher interface {
	Tag(ctx context.Context, dir, tag, sha string) error
	// TagAt answers the commit tag points at in dir, and whether it exists
	// there at all. There is no LatestTag: the version is decided by the
	// core, and an engine that reads a tag line to compute one is a second
	// authority.
	TagAt(ctx context.Context, dir, tag string) (string, bool, error)
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

// Tag points a tag at one commit and pushes it. It is idempotent by refusing a
// tag that already exists rather than moving it, because a moved tag changes
// what a consumer already pinned.
func (g *GH) Tag(ctx context.Context, dir, tag, sha string) error {
	existing, err := g.run(ctx, dir, "listing tags", "git", "tag", "--list", tag)
	if err != nil {
		return err
	}

	if existing != "" {
		return fmt.Errorf("tagging %s: %s already exists and a tag is never moved", dir, tag)
	}

	// Annotated, with the tag as its message: a lightweight tag dies with
	// "no tag message" on a machine whose global config signs tags, while an
	// annotated tag works signed and unsigned alike.
	if _, err := g.run(ctx, dir, "tagging "+tag, "git", "tag", "-m", tag, tag, sha); err != nil {
		return err
	}

	_, err = g.run(ctx, dir, "pushing "+tag, "git", "push", "origin", tag)

	return err
}

// TagAt answers where a tag points, and whether the repo carries it. A repo
// that does not have the tag is not an error: that is the ordinary case, and
// it is what a first release of a member looks like.
func (g *GH) TagAt(ctx context.Context, dir, tag string) (string, bool, error) {
	listed, err := g.run(ctx, dir, "reading tag "+tag, "git", "tag", "--list", tag)
	if err != nil {
		return "", false, err
	}

	if strings.TrimSpace(listed) == "" {
		return "", false, nil
	}

	// An annotated tag's own object is not the commit, so the caret asks for
	// what it points at. It is a no-op on a lightweight tag.
	sha, err := g.run(ctx, dir, "resolving tag "+tag, "git", "rev-list", "-n", "1", tag)
	if err != nil {
		return "", false, err
	}

	return strings.TrimSpace(sha), true, nil
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
