package computecontroller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/forgeadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var ErrTarget = errors.New("a target needs exactly one of forge or forgeCI")

const (
	forgeBinary   = "forge"
	forgeCIBinary = "forge-ci"
)

type Controller struct {
	runner    execadapter.Runner
	harvester forgeadapter.Harvester
	now       func() time.Time
}

func New(runner execadapter.Runner, harvester forgeadapter.Harvester, now func() time.Time) *Controller {
	if now == nil {
		now = time.Now
	}

	return &Controller{runner: runner, harvester: harvester, now: now}
}

func (c *Controller) Declare(spec map[string]any) (citypes.DeclareOutput, error) {
	return citypes.DeclareOutput{Resources: []citypes.Resource{}}, nil
}

func (c *Controller) Run(ctx context.Context, in citypes.RunInput) (citypes.RunOutput, error) {
	if len(in.Targets) == 0 {
		return citypes.RunOutput{}, errors.New("running: no targets given")
	}

	var log strings.Builder

	started := c.now()
	out := citypes.RunOutput{Status: citypes.StatusPassed}

	// The revision and the version both reach every target as environment
	// variables. The revision is the tuple a build was proven with, and the
	// release side keys the distribution on the same id. The version is the
	// number the release will carry, decided before any stage ran, so a
	// binary stamps the release it ships in rather than the nearest tag its
	// own repo happens to hold.
	env := map[string]string{"FORGE_CI_REVISION": in.Revision}
	if in.Version != "" {
		env["FORGE_CI_VERSION"] = in.Version
	}

	// One mutex over everything a wave's members share: the log, the status
	// and the harvested artifacts. A wave runs its dirs at once, so all three
	// are written concurrently.
	var mu sync.Mutex

	for _, target := range in.Targets {
		binary, expanded, err := CommandFor(target, in.Params)
		if err != nil {
			return citypes.RunOutput{}, err
		}

		waves, err := citypes.WavesFor(target, in)
		if err != nil {
			return citypes.RunOutput{}, err
		}

		for _, wave := range waves {
			var (
				wg sync.WaitGroup
				// broke is the first machinery failure, kept apart from a
				// target that merely exited non-zero. A runner that could not
				// start is broken; a build that failed is a failed build.
				broke error
			)

			// A wave finishes before the next starts, and it finishes even
			// when one of its members fails. Cancelling the rest would report
			// the first failure and hide every other one, and somebody
			// reading a red run wants every repo that broke.
			for _, dir := range pathsFor(wave, in) {
				wg.Add(1)

				go func(dir string) {
					defer wg.Done()

					res, err := c.runner.RunEnv(ctx, dir, env, binary, strings.Fields(expanded)...)

					mu.Lock()
					defer mu.Unlock()

					if err != nil {
						if broke == nil {
							broke = fmt.Errorf("running target %q in %s: %w", target.Alias, dir, err)
						}

						return
					}

					// One Fprintf per dir, under the lock, so two members of
					// a wave cannot interleave halfway through a line.
					fmt.Fprintf(&log, "$ %s %s (in %s)\n%s%s", binary, expanded, dir, res.Stdout, res.Stderr)

					if res.ExitCode != 0 && out.Status != citypes.StatusFailed {
						out.Status = citypes.StatusFailed
						out.Message = fmt.Sprintf("target %q exited %d in %s", target.Alias, res.ExitCode, dir)
					}

					if binary != forgeBinary || c.harvester == nil {
						return
					}

					harvested, err := c.harvester.Harvest(dir, started)
					if err != nil {
						if broke == nil {
							broke = err
						}

						return
					}

					rebase(harvested, in.Root, dir)
					out.Forge = merge(out.Forge, harvested)
				}(dir)
			}

			wg.Wait()

			if broke != nil {
				return citypes.RunOutput{}, broke
			}

			if out.Status == citypes.StatusFailed {
				break
			}
		}

		if out.Status == citypes.StatusFailed {
			break
		}
	}

	out.Output = log.String()

	return out, nil
}

// CommandFor answers the binary and the expanded arguments one target
// runs. It is shared with the remote compute engines, so a target means
// the same thing wherever it executes.
func CommandFor(t citypes.Target, params map[string]string) (string, string, error) {
	binary, raw, err := binaryFor(t)
	if err != nil {
		return "", "", err
	}

	expanded, err := expand(raw, params)
	if err != nil {
		return "", "", fmt.Errorf("expanding target %q: %w", t.Alias, err)
	}

	return binary, expanded, nil
}

func binaryFor(t citypes.Target) (string, string, error) {
	switch {
	case t.Forge != "" && t.ForgeCI == "":
		return forgeBinary, t.Forge, nil
	case t.ForgeCI != "" && t.Forge == "":
		return forgeCIBinary, t.ForgeCI, nil
	default:
		return "", "", fmt.Errorf("target %q: %w", t.Alias, ErrTarget)
	}
}

// pathsFor puts one wave's repo names on disk. The empty name is the
// workspace root, which is what a target naming no repo runs in.
func pathsFor(wave []string, in citypes.RunInput) []string {
	paths := make([]string, 0, len(wave))

	for _, name := range wave {
		if name == "" {
			paths = append(paths, in.Root)

			continue
		}

		resolved := filepath.Join(in.Root, name)

		for _, repo := range in.Repos {
			if repo.Name == name && repo.Path != "" {
				resolved = repo.Path

				break
			}
		}

		paths = append(paths, resolved)
	}

	return paths
}

func expand(raw string, params map[string]string) (string, error) {
	if !strings.Contains(raw, "{{") {
		return raw, nil
	}

	tmpl, err := template.New("target").Option("missingkey=error").Parse(raw)
	if err != nil {
		return "", err
	}

	var b bytes.Buffer
	if err := tmpl.Execute(&b, struct{ Params map[string]string }{Params: params}); err != nil {
		return "", err
	}

	return b.String(), nil
}

// rebase rewrites harvested artifact locations to be relative to the
// pipeline root: a store records them relative to its own repo, which means
// nothing to the release side. URLs pass through untouched.
func rebase(result *citypes.ForgeResult, root, dir string) {
	if result == nil {
		return
	}

	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." {
		return
	}

	for i, artifact := range result.Artifacts {
		// A colon marks a URL or an image reference; a local relative path
		// never carries one, and only those are rebased.
		if artifact.Location == "" || strings.Contains(artifact.Location, ":") ||
			filepath.IsAbs(artifact.Location) {
			continue
		}

		result.Artifacts[i].Location = filepath.Join(rel, artifact.Location)
	}
}

func merge(into, from *citypes.ForgeResult) *citypes.ForgeResult {
	if from == nil {
		return into
	}

	if into == nil {
		return from
	}

	into.Artifacts = append(into.Artifacts, from.Artifacts...)
	into.TestReports = append(into.TestReports, from.TestReports...)

	return into
}
