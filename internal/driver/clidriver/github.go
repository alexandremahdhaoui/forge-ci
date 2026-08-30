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

func (d *Driver) clients(c credentials) (Publisher, Announcer, error) {
	if d.github == nil {
		return nil, nil, ErrNoGit
	}

	publisher, announcer := d.github(*c.tokenEnv, *c.apiBaseURL)

	return publisher, announcer, nil
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

	publisher, _, err := d.clients(creds)
	if err != nil {
		return err
	}

	report, err := publisher.Publish(ctx, *dir, *repo, *tag, *sha)
	if err != nil {
		return err
	}

	return d.write(fmt.Sprintf("%s: %s %s\n", *tag, report.Reason, report.URL))
}

// reportFailure files the issue that says a run nobody was watching failed.
//
// The step that calls this is the only thing that can see a job which died
// before forge-ci was alive - a missing binary, a checkout that failed - so
// it stays a workflow step. What it no longer carries is the decision: the
// dedupe lives in a controller with a test.
func (d *Driver) reportFailure(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("report-failure", flag.ContinueOnError)
	fs.SetOutput(d.out)

	repo := fs.String("repo", "", "owner/name of the repo to file the issue in")
	title := fs.String("title", "", "issue title; an open issue with this exact title suppresses a second")
	body := fs.String("body", "", "issue body")
	creds := bindCredentials(fs)

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parsing flags: %w", err)
	}

	_, announcer, err := d.clients(creds)
	if err != nil {
		return err
	}

	report, err := announcer.Announce(ctx, *repo, *title, *body)
	if err != nil {
		return err
	}

	return d.write(fmt.Sprintf("%s %s\n", report.Reason, report.URL))
}
