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
	LatestTag(ctx context.Context, dir, prefix string) (string, error)
	SubjectsSince(ctx context.Context, dir, tag string) ([]string, error)
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

// Identity the engine commits under when the host has none.
//
// No Signed-off-by trailer goes with it. That trailer is a DCO attestation, a
// human certifying they wrote the change and may submit it. A revision record
// is written by the tool, and asserting authorship on the tool's behalf would
// be a claim nobody made.
const (
	fallbackName  = "Alexandre Mahdhaoui"
	fallbackEmail = "alexandre.mahdhaoui@gmail.com"
)

// Commit records the staged record.
//
// The identity is a floor rather than an override: a host that has one keeps
// it, so a local run stays attributed to whoever ran it, and a host with none
// gets the engine's own instead of failing. A CI runner has none - git exits
// 128 with "empty ident name" - and every pipeline run died there while every
// developer machine passed, because a global identity made the gap invisible.
//
// Signing goes off with the fallback for the same reason it is needed: a host
// with no identity configured has no signing key either, and an ambient
// commit.gpgsign would turn one failure into another.
func (g *CLI) Commit(ctx context.Context, dir, message string) error {
	args := []string{"commit", "-m", message}

	if !g.hasIdentity(ctx, dir) {
		args = append([]string{
			"-c", "user.name=" + fallbackName,
			"-c", "user.email=" + fallbackEmail,
			"-c", "commit.gpgsign=false",
		}, args...)
	}

	_, err := g.run(ctx, dir, "committing in "+dir, args...)

	return err
}

// hasIdentity asks git whether it can name a committer at all. git var
// resolves config, environment and defaults together, which is the same
// answer commit itself would reach, so this cannot disagree with it.
func (g *CLI) hasIdentity(ctx context.Context, dir string) bool {
	res, err := g.runner.Run(ctx, dir, "git", "var", "GIT_COMMITTER_IDENT")

	return err == nil && res.ExitCode == 0 && strings.TrimSpace(res.Stdout) != ""
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

// LatestTag is the highest version this prefix's line carries, without the
// prefix. A repo with no tag on the line answers empty rather than failing,
// because a workspace that has never released has nothing to report.
//
// The prefix is a namespace. Reading a tag from another prefix would make two
// factories that share a repo take turns overwriting each other's line, so a
// tag that does not carry this prefix is not this line's history and is
// skipped.
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

		// Only a tag the version rule can read. git sorts every tag it
		// has, so one legacy tag sorting above the release line - "v1.2",
		// "wip" - was returned as the previous version, and the whole
		// release then failed on "not a semver tag", for every member.
		// A repo that carries release history from before this pipeline
		// existed is exactly the case this has to survive.
		if semverTag.MatchString(tag) {
			return tag, nil
		}
	}

	return "", nil
}

// SubjectsSince is every commit subject a checkout gained after tag. A tag the
// repo does not carry answers the whole history, which is the right answer for
// a member joining a line that already exists: all of it is new to that line.
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
