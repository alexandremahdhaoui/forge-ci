package gitadapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/gitident"
)

type Git interface {
	Init(ctx context.Context, dir string) error
	Clone(ctx context.Context, url, dir string) error
	IsRepo(ctx context.Context, dir string) (bool, error)
	IgnoresPath(ctx context.Context, dir, path string) (bool, error)
	Add(ctx context.Context, dir, path string) error
	Commit(ctx context.Context, dir, message string, paths ...string) error
	HeadSHA(ctx context.Context, dir string) (string, error)
	RemoteSHA(ctx context.Context, url, ref string) (string, error)
	Staged(ctx context.Context, dir string, paths ...string) (bool, error)
	LatestTag(ctx context.Context, dir, prefix string) (string, error)
	SubjectsSince(ctx context.Context, dir, tag string) ([]string, error)
	WorktreeHash(ctx context.Context, dir string, exclude ...string) (string, error)
	TreeHash(ctx context.Context, dir string, ignore ...string) (string, error)
	Tag(ctx context.Context, dir, tag, sha string) error
	TagAt(ctx context.Context, dir, tag string) (string, bool, error)
	RemoteTagAt(ctx context.Context, dir, tag string) (string, bool, error)
	HasRemote(ctx context.Context, dir string) (bool, error)
	Branch(ctx context.Context, dir string) (string, error)
	Push(ctx context.Context, dir, branch string) error
	PullRebase(ctx context.Context, dir, branch string) error
	LFSInstall(ctx context.Context, dir string) error
}

var ErrNoRemote = errors.New("the checkout has no origin to push to")

var ErrRejected = errors.New("the remote rejected the push")

type CLI struct {
	runner execadapter.Runner
}

var _ Git = (*CLI)(nil)

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
	_, err := g.run(ctx, dir, "initialising "+dir, "init", "-b", citypes.DefaultBranch)

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

func (g *CLI) IgnoresPath(ctx context.Context, dir, path string) (bool, error) {
	res, err := g.runner.Run(ctx, dir, "git", "check-ignore", "-q", path)
	if err != nil {
		return false, fmt.Errorf("asking git in %s whether it ignores %s: %w", dir, path, err)
	}

	switch res.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	}

	return false, fmt.Errorf("asking git in %s whether it ignores %s: git exited %d: %s",
		dir, path, res.ExitCode, strings.TrimSpace(res.Stderr))
}

func (g *CLI) Add(ctx context.Context, dir, path string) error {
	res, err := g.run(ctx, dir, "staging "+path, "add", path)

	if err != nil && strings.Contains(res.Stderr, ".gitignore") {
		return fmt.Errorf(
			"%w - the repo's .gitignore matches this record path; anchor output patterns to the repo root (write /build/, not build/)",
			err)
	}

	return err
}

func (g *CLI) Commit(ctx context.Context, dir, message string, paths ...string) error {
	args := append(gitident.Args(ctx, g.runner, dir), "commit", "-m", message)
	args = append(args, pathspec(paths)...)

	_, err := g.run(ctx, dir, "committing in "+dir, args...)

	return err
}

func (g *CLI) Staged(ctx context.Context, dir string, paths ...string) (bool, error) {
	args := append([]string{"diff", "--cached", "--name-only"}, pathspec(paths)...)

	res, err := g.run(ctx, dir, "reading the index of "+dir, args...)
	if err != nil {
		return false, err
	}

	return strings.TrimSpace(res.Stdout) != "", nil
}

func pathspec(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}

	return append([]string{"--"}, paths...)
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
		ref = citypes.DefaultBranch
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

func (g *CLI) WorktreeHash(ctx context.Context, dir string, exclude ...string) (string, error) {
	pathspec := []string{"--", "."}
	for _, path := range exclude {
		pathspec = append(pathspec, ":(exclude)"+path)
	}

	statusArgs := append([]string{"status", "--porcelain"}, pathspec...)

	status, err := g.run(ctx, dir, "reading status of "+dir, statusArgs...)
	if err != nil {
		return "", err
	}

	if strings.TrimSpace(status.Stdout) == "" {
		return "", nil
	}

	diffArgs := append([]string{"diff", "HEAD"}, pathspec...)

	diff, err := g.run(ctx, dir, "reading uncommitted changes in "+dir, diffArgs...)
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256([]byte(status.Stdout + "\n" + diff.Stdout))

	return hex.EncodeToString(sum[:])[:12], nil
}

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

		if citypes.SemverTag.MatchString(tag) {
			return tag, nil
		}
	}

	return "", nil
}

func (g *CLI) TreeHash(ctx context.Context, dir string, ignore ...string) (string, error) {
	res, err := g.run(ctx, dir, "listing the tree", "ls-tree", "-r", "HEAD")
	if err != nil {
		return "", err
	}

	kept := []string{}

	for _, line := range strings.Split(res.Stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		_, path, ok := strings.Cut(line, "\t")
		if !ok || Ignored(path, ignore) {
			continue
		}

		kept = append(kept, line)
	}

	sum := sha256.Sum256([]byte(strings.Join(kept, "\n")))

	return hex.EncodeToString(sum[:]), nil
}

func Ignored(path string, patterns []string) bool {
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}

		if rest, ok := strings.CutPrefix(pattern, "/"); ok {
			if matched, _ := filepathMatch(rest, path); matched {
				return true
			}

			continue
		}

		if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				return true
			}

			continue
		}

		if strings.HasPrefix(pattern, "**") {
			if matched, _ := filepathMatch(strings.TrimPrefix(pattern, "**"), "/"+path); matched {
				return true
			}

			if matched, _ := filepathMatch("*"+strings.TrimPrefix(pattern, "**"), path[strings.LastIndex(path, "/")+1:]); matched {
				return true
			}

			continue
		}

		if matched, _ := filepathMatch(pattern, path); matched {
			return true
		}
	}

	return false
}

func filepathMatch(pattern, name string) (bool, error) {
	return path.Match(pattern, name)
}

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

func (g *CLI) Tag(ctx context.Context, dir, tag, sha string) error {
	hasRemote, err := g.HasRemote(ctx, dir)
	if err != nil {
		return err
	}

	if hasRemote {
		remoteAt, onRemote, err := g.RemoteTagAt(ctx, dir, tag)
		if err != nil {
			return err
		}

		if onRemote {
			if remoteAt == sha {
				return nil
			}

			return fmt.Errorf(
				"tagging %s: %s already points at %s on origin, not %s, and a tag is never moved",
				dir, tag, remoteAt, sha)
		}
	}

	localAt, exists, err := g.TagAt(ctx, dir, tag)
	if err != nil {
		return err
	}

	if exists && localAt != sha {
		return fmt.Errorf(
			"tagging %s: %s already points at %s, not %s, and a tag is never moved",
			dir, tag, localAt, sha)
	}

	if !exists {
		args := append(gitident.Args(ctx, g.runner, dir), "tag", "-m", tag, tag, sha)

		if _, err := g.run(ctx, dir, "tagging "+tag, args...); err != nil {
			return err
		}
	}

	if !hasRemote {
		return nil
	}

	_, err = g.run(ctx, dir, "pushing "+tag, "push", "origin", tag)

	return err
}

func (g *CLI) RemoteTagAt(ctx context.Context, dir, tag string) (string, bool, error) {
	res, err := g.run(ctx, dir, "reading tag "+tag+" of origin",
		"ls-remote", "origin", "refs/tags/"+tag, "refs/tags/"+tag+"^{}")
	if err != nil {
		return "", false, err
	}

	var plain, peeled string

	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		if strings.HasSuffix(fields[1], "^{}") {
			peeled = fields[0]
		} else {
			plain = fields[0]
		}
	}

	if peeled != "" {
		return peeled, true, nil
	}

	if plain != "" {
		return plain, true, nil
	}

	return "", false, nil
}

func (g *CLI) LFSInstall(ctx context.Context, dir string) error {
	_, err := g.run(ctx, dir, "installing git-lfs filters in "+dir, "lfs", "install", "--local")
	if err != nil && strings.Contains(err.Error(), "not a git command") {
		return fmt.Errorf(
			"installing git-lfs filters in %s: git-lfs is not installed on this machine "+
				"and the store declares LFS-tracked kinds: %w", dir, err)
	}

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

func (g *CLI) PullRebase(ctx context.Context, dir, branch string) error {
	if _, err := g.run(ctx, dir, "fetching origin in "+dir, "fetch", "origin", branch); err != nil {
		return err
	}

	args := append(gitident.Args(ctx, g.runner, dir), "rebase", "origin/"+branch)

	_, err := g.run(ctx, dir, "rebasing "+dir+" onto origin/"+branch, args...)

	return err
}

func (g *CLI) TagAt(ctx context.Context, dir, tag string) (string, bool, error) {
	listed, err := g.run(ctx, dir, "reading tag "+tag, "tag", "--list", tag)
	if err != nil {
		return "", false, err
	}

	if strings.TrimSpace(listed.Stdout) == "" {
		return "", false, nil
	}

	res, err := g.run(ctx, dir, "resolving tag "+tag, "rev-list", "-n", "1", tag)
	if err != nil {
		return "", false, err
	}

	return strings.TrimSpace(res.Stdout), true, nil
}
