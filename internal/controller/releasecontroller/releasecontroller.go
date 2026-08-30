// Package releasecontroller tags one commit and publishes its release,
// idempotently.
//
// This is the operator's escape hatch, not the pipeline's release stage. The
// pipeline releases a whole revision through the artifact port; this
// publishes one tag somebody named by hand, which is what the generated
// release workflow does when it is dispatched.
package releasecontroller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
)

var (
	ErrRepo = errors.New("a release needs the repo to publish it in")
	ErrTag  = errors.New("a release needs the tag to publish")
	ErrSHA  = errors.New("a release needs the commit its tag points at")
)

// Report is what the run did, in the two halves it can do independently.
type Report struct {
	Tagged    bool
	Published bool
	URL       string
	Reason    string
}

type Controller struct {
	git gitadapter.Git
	api githubadapter.API
}

func New(git gitadapter.Git, api githubadapter.API) *Controller {
	return &Controller{git: git, api: api}
}

// Publish tags sha and releases it, skipping whichever half already exists.
//
// Both halves are separately idempotent because they fail separately: a run
// that tagged and then lost the network leaves the tag behind, and the
// re-run has to finish the job rather than refuse it. A tag already pointing
// somewhere else is the one case that refuses, because moving it changes
// what a consumer already pinned.
func (c *Controller) Publish(ctx context.Context, dir, repo, tag, sha string) (Report, error) {
	switch {
	case strings.TrimSpace(repo) == "":
		return Report{}, ErrRepo
	case strings.TrimSpace(tag) == "":
		return Report{}, ErrTag
	case strings.TrimSpace(sha) == "":
		return Report{}, ErrSHA
	}

	out := Report{}

	at, found, err := c.git.TagAt(ctx, dir, tag)
	if err != nil {
		return out, fmt.Errorf("publishing %s: %w", tag, err)
	}

	switch {
	case found && at != sha:
		return out, fmt.Errorf(
			"publishing %s: it already points at %s, not %s, and a tag is never moved", tag, at, sha)
	case !found:
		if err := c.git.Tag(ctx, dir, tag, sha); err != nil {
			return out, fmt.Errorf("publishing %s: %w", tag, err)
		}

		out.Tagged = true
	}

	existing, found, err := c.api.ReleaseByTag(ctx, repo, tag)
	if err != nil {
		return out, fmt.Errorf("publishing %s: %w", tag, err)
	}

	if found {
		out.URL = existing.HTMLURL
		out.Reason = "a release already exists for this tag"

		return out, nil
	}

	release, err := c.api.CreateRelease(ctx, repo, tag)
	if err != nil {
		return out, fmt.Errorf("publishing %s: %w", tag, err)
	}

	out.Published = true
	out.URL = release.HTMLURL
	out.Reason = "published"

	return out, nil
}
