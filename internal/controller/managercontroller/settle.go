package managercontroller

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
)

var _ Settler = GitHubRealizer{}

// Settle commits and pushes the files this reconcile converged.
//
// The manager owns this, not the core. What durable means belongs to the
// world a manager talks to: files in a git checkout are durable once pushed,
// a function is durable once deployed, and a file the local manager wrote is
// already durable where it lies. The core only learns that something changed.
//
// Only the paths handed in are staged. A reconcile that swept up whatever
// else was uncommitted would publish a human's half-finished work under the
// pipeline's name, which is not the pipeline's to publish. That is why this
// stages an explicit list and never the worktree.
//
// message is the commit subject, handed in by the caller. It names no repo
// and no file: the diff already says which, and the subject is what a human
// scanning the log reads to know this commit was not theirs - and what the
// release decision reads to know it was forge-ci's.
func (r GitHubRealizer) Settle(paths []string, message string) (Action, bool, error) {
	if r.git == nil {
		return Action{}, false, nil
	}

	byRepo := map[string][]string{}

	for _, p := range paths {
		dir, rel, ok := r.repoOf(p)
		if !ok {
			// A converged file outside any checkout has nothing to commit
			// it to. It is on disk, which for a path nobody tracks is as
			// durable as it gets.
			continue
		}

		byRepo[dir] = append(byRepo[dir], rel)
	}

	dirs := make([]string, 0, len(byRepo))
	for dir := range byRepo {
		dirs = append(dirs, dir)
	}

	sort.Strings(dirs)

	var (
		lines     []string
		failures  []error
		published bool
	)

	for _, dir := range dirs {
		line, pushed, err := r.settleRepo(dir, byRepo[dir], message)
		if err != nil {
			failures = append(failures, err)

			continue
		}

		published = published || pushed

		lines = append(lines, line)
	}

	if len(failures) > 0 {
		return Action{}, false, errors.Join(failures...)
	}

	return Did(strings.Join(lines, "\n")), published, nil
}

// settleRepo commits one checkout's converged paths and answers whether the
// commit was pushed. Committed-but-unpushed (no remote, detached HEAD) is
// durable on this machine and published nowhere - the caller's stop
// decision needs the difference.
func (r GitHubRealizer) settleRepo(dir string, paths []string, message string) (string, bool, error) {
	sort.Strings(paths)

	for _, p := range paths {
		if err := r.git.Add(r.ctx, dir, p); err != nil {
			return "", false, err
		}
	}

	staged, err := r.git.Staged(r.ctx, dir, paths...)
	if err != nil {
		return "", false, err
	}

	if !staged {
		// A file deleted by hand and rewritten to exactly what HEAD carries.
		// It converged, so the run still stops, and there is nothing to
		// commit. Reporting a commit that never happened is a lie in the log
		// an operator reads to find out what the pipeline did.
		return fmt.Sprintf("nothing to commit in %s: what changed already matches HEAD", dir), false, nil
	}

	if err := r.git.Commit(r.ctx, dir, message, paths...); err != nil {
		return "", false, err
	}

	sha, err := r.git.HeadSHA(r.ctx, dir)
	if err != nil {
		return "", false, err
	}

	short := sha
	if len(short) > 12 {
		short = short[:12]
	}

	has, err := r.git.HasRemote(r.ctx, dir)
	if err != nil {
		return "", false, err
	}

	if !has {
		return fmt.Sprintf("committed %s @ %s; %v, so nothing was pushed",
			dir, short, gitadapter.ErrNoRemote), false, nil
	}

	branch, err := r.git.Branch(r.ctx, dir)
	if err != nil {
		return "", false, err
	}

	if branch == "" {
		// A detached HEAD is what a checkout of a tag looks like. Picking a
		// branch here would push the commit somewhere nobody named.
		return fmt.Sprintf("committed %s @ %s; HEAD is detached, so nothing was pushed", dir, short), false, nil
	}

	if err := r.git.Push(r.ctx, dir, branch); err != nil {
		return "", false, err
	}

	return fmt.Sprintf("committed and pushed %s @ %s to %s", dir, short, branch), true, nil
}

// repoOf answers the checkout a converged path belongs to and the path
// relative to it, walking up from the file until a .git appears. The walk
// stops at the pipeline root: a pipeline never commits above its own
// workspace, whatever a resource name says.
func (r GitHubRealizer) repoOf(name string) (dir, rel string, ok bool) {
	full := name
	if r.root != "" && !filepath.IsAbs(full) {
		full = filepath.Join(r.root, full)
	}

	root := r.root
	if root == "" {
		root = "."
	}

	root = filepath.Clean(root)

	for at := filepath.Dir(full); ; at = filepath.Dir(at) {
		exists, err := r.fs.Exists(filepath.Join(at, ".git"))
		if err == nil && exists {
			rel, err := filepath.Rel(at, full)
			if err != nil {
				return "", "", false
			}

			return at, rel, true
		}

		if at == root || at == filepath.Dir(at) {
			return "", "", false
		}
	}
}
