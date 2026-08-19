package releaseadapter

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
)

// Publisher puts a release where people can fetch it.
type Publisher interface {
	Tag(ctx context.Context, dir, version, sha string) error
	Release(ctx context.Context, dir, version string, files []string) (string, error)
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

	if _, err := g.run(ctx, dir, "tagging "+version, "git", "tag", version, sha); err != nil {
		return err
	}

	_, err = g.run(ctx, dir, "pushing "+version, "git", "push", "origin", version)

	return err
}

// Release creates the release and attaches the files, and returns its URL.
func (g *GH) Release(ctx context.Context, dir, version string, files []string) (string, error) {
	args := []string{"release", "create", version, "--generate-notes"}

	for _, f := range files {
		args = append(args, filepath.Clean(f))
	}

	return g.run(ctx, dir, "creating release "+version, "gh", args...)
}
