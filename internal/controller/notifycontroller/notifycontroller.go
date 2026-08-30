// Package notifycontroller decides how a run that nobody is watching raises
// its own alarm.
//
// It exists because that decision used to live in a generated workflow's
// shell block, where no test could reach it and where it called a binary
// that is not in every image a job runs in. The step that invokes this still
// has to exist - a job can die before forge-ci is alive to speak, and only
// the runner can see that - but it now carries one line and no logic.
package notifycontroller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
)

var (
	ErrRepo  = errors.New("a failure report needs the repo to file it in")
	ErrTitle = errors.New("a failure report needs a title to dedupe on")
)

// Report is what happened, for the operator reading the run log.
type Report struct {
	// Filed is false when an issue was already open under this title.
	Filed bool
	URL   string
	// Reason says which of those two it was, in one line.
	Reason string
}

type Controller struct {
	api githubadapter.API
}

func New(api githubadapter.API) *Controller {
	return &Controller{api: api}
}

// Announce files one issue for a failing run, at most once.
//
// The dedupe is on the exact title and it is the whole point: a job that
// fails every morning should leave one issue open, not thirty. One instance
// failed every morning for eight days and the first person to look found it
// by listing runs on a hunch, which is what an unwatched run costs.
//
// A failure that comes back after a green run is news again, so a closed
// issue does not suppress a new one. Only an open one does.
func (c *Controller) Announce(ctx context.Context, repo, title, body string) (Report, error) {
	if strings.TrimSpace(repo) == "" {
		return Report{}, ErrRepo
	}

	if strings.TrimSpace(title) == "" {
		return Report{}, ErrTitle
	}

	open, found, err := c.api.OpenIssueByTitle(ctx, repo, title)
	if err != nil {
		return Report{}, fmt.Errorf("announcing the failure of %q: %w", title, err)
	}

	if found {
		return Report{
			Filed:  false,
			URL:    open.HTMLURL,
			Reason: "an issue is already open for this; not filing another",
		}, nil
	}

	issue, err := c.api.CreateIssue(ctx, repo, title, body)
	if err != nil {
		return Report{}, fmt.Errorf("announcing the failure of %q: %w", title, err)
	}

	return Report{Filed: true, URL: issue.HTMLURL, Reason: "filed"}, nil
}
