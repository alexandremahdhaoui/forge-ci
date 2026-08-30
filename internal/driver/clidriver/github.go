package clidriver

import (
	"context"
	"flag"
	"fmt"
)

// DefaultTokenEnv is where a GitHub credential is read from when a command
// names no other. Actions injects it into every job, so the ordinary case
// needs no flag.
const DefaultTokenEnv = "GITHUB_TOKEN"

// credentials are the two flags every GitHub verb carries: which environment
// variable holds the token, and which API answers. Both are flags rather
// than ambient values, because a tool that picks up whichever token the host
// happens to carry cannot be pointed at the one the pipeline declared.
type credentials struct {
	tokenEnv   *string
	apiBaseURL *string
}

func bindCredentials(fs *flag.FlagSet) credentials {
	return credentials{
		tokenEnv: fs.String("token-env", DefaultTokenEnv,
			"environment variable holding the GitHub token"),
		apiBaseURL: fs.String("api-base-url", "",
			"GitHub API base; empty means the public API, a fake in tests, a mirror in an airgap"),
	}
}

func (d *Driver) publisher(c credentials) (Publisher, error) {
	if d.github == nil {
		return nil, ErrNoGit
	}

	return d.github(*c.tokenEnv, *c.apiBaseURL), nil
}

// release tags one commit and publishes its release. It is what the
// generated release workflow runs when somebody dispatches it, and both
// halves are separately idempotent so a re-run finishes an interrupted one.
func (d *Driver) release(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	fs.SetOutput(d.out)

	repo := fs.String("repo", "", "owner/name of the repo to publish the release in")
	tag := fs.String("tag", "", "the tag to publish")
	sha := fs.String("sha", "", "the commit the tag points at")
	dir := fs.String("dir", ".", "checkout the tag is written in")
	creds := bindCredentials(fs)

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parsing flags: %w", err)
	}

	publisher, err := d.publisher(creds)
	if err != nil {
		return err
	}

	report, err := publisher.Publish(ctx, *dir, *repo, *tag, *sha)
	if err != nil {
		return err
	}

	return d.write(fmt.Sprintf("%s: %s %s\n", *tag, report.Reason, report.URL))
}

