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
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var ErrTarget = errors.New("a target needs a binary")

type Controller struct {
	runner    execadapter.Runner
	harvester forgeadapter.Harvester
	fs        fsadapter.FS
	git       gitadapter.Git
	now       func() time.Time
}

func New(
	runner execadapter.Runner, harvester forgeadapter.Harvester, fs fsadapter.FS, git gitadapter.Git,
	now func() time.Time,
) *Controller {
	if now == nil {
		now = time.Now
	}

	return &Controller{runner: runner, harvester: harvester, fs: fs, git: git, now: now}
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

	limit := outputLimit(in.Spec)

	env := map[string]string{"FORGE_CI_REVISION": in.Revision}
	if in.Version != "" {
		env["FORGE_CI_VERSION"] = in.Version
	}

	if in.Sync {
		for _, verb := range []string{"sync", "lock"} {
			res, err := c.runner.RunEnv(ctx, in.Root, env, "forge-factory", verb)
			if err != nil {
				return citypes.RunOutput{}, fmt.Errorf("converging the workspace (forge-factory %s): %w", verb, err)
			}

			fmt.Fprintf(&log, "$ forge-factory %s (in %s)\n%s%s", verb, in.Root, res.Stdout, res.Stderr)

			if res.ExitCode != 0 {
				out.Status = citypes.StatusFailed
				out.Message = fmt.Sprintf("forge-factory %s exited %d in %s", verb, res.ExitCode, in.Root)
				out.Output = tail(log.String(), limit)

				return out, nil
			}
		}
	}

	var mu sync.Mutex

	for _, target := range in.Targets {
		binary, args, err := CommandFor(target, in.Params)
		if err != nil {
			return citypes.RunOutput{}, err
		}

		shown := strings.Join(append([]string{binary}, args...), " ")

		waves, err := citypes.WavesFor(target, in)
		if err != nil {
			return citypes.RunOutput{}, err
		}

		for _, wave := range waves {
			var (
				wg    sync.WaitGroup
				broke error
			)

			for _, dir := range pathsFor(wave, in) {
				wg.Add(1)

				go func(dir string) {
					defer wg.Done()

					res, err := c.runIn(ctx, dir, env, binary, args)

					mu.Lock()
					defer mu.Unlock()

					if err != nil {
						if broke == nil {
							broke = fmt.Errorf("running target %q in %s: %w", target.Alias, dir, err)
						}

						return
					}

					fmt.Fprintf(&log, "$ %s (in %s)\n%s%s", shown, dir, res.Stdout, res.Stderr)

					if res.ExitCode != 0 && out.Status != citypes.StatusFailed {
						out.Status = citypes.StatusFailed
						out.Message = fmt.Sprintf("target %q exited %d in %s", target.Alias, res.ExitCode, dir)
					}

					if c.harvester == nil {
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

	out.Output = tail(log.String(), limit)

	return out, nil
}

func (c *Controller) runIn(
	ctx context.Context, dir string, env map[string]string, binary string, args []string,
) (execadapter.Result, error) {
	if err := c.giveACloneItsEnvrc(ctx, dir); err != nil {
		return execadapter.Result{}, err
	}

	return c.runner.RunEnv(ctx, dir, env, binary, args...)
}

func (c *Controller) giveACloneItsEnvrc(ctx context.Context, dir string) error {
	isDir, err := c.fs.IsDir(dir)
	if err != nil {
		return fmt.Errorf("inspecting %s before its target runs: %w", dir, err)
	}

	if !isDir {
		return nil
	}

	envrc := filepath.Join(dir, ".envrc")

	exists, err := c.fs.Exists(envrc)
	if err != nil {
		return fmt.Errorf("looking for the .envrc in %s: %w", dir, err)
	}

	if exists {
		return nil
	}

	repo, err := c.git.IsRepo(ctx, dir)
	if err != nil {
		return fmt.Errorf("asking whether %s is a git repository before its target runs: %w", dir, err)
	}

	if !repo {
		return nil
	}

	ignored, err := c.git.IgnoresPath(ctx, dir, ".envrc")
	if err != nil {
		return err
	}

	if !ignored {
		return errors.New(
			"it holds no .envrc and its git does not ignore .envrc, " +
				"so the engine will not write one git could carry. add /.envrc to its .gitignore")
	}

	if err := c.fs.WriteFile(envrc, nil); err != nil {
		return fmt.Errorf("writing an empty .envrc in %s: %w", dir, err)
	}

	return nil
}

func CommandFor(t citypes.Target, params map[string]string) (string, []string, error) {
	if strings.TrimSpace(t.Binary) == "" {
		return "", nil, fmt.Errorf("target %q: %w", t.Alias, ErrTarget)
	}

	args := make([]string, 0, len(t.Args))

	for _, raw := range t.Args {
		expanded, err := expand(raw, params)
		if err != nil {
			return "", nil, fmt.Errorf("expanding target %q: %w", t.Alias, err)
		}

		args = append(args, expanded)
	}

	return t.Binary, args, nil
}

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

func rebase(result *citypes.ForgeResult, root, dir string) {
	if result == nil {
		return
	}

	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." {
		return
	}

	for i, artifact := range result.Artifacts {
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

const DefaultOutputLimit = 16384

func outputLimit(spec map[string]any) int {
	switch v := spec["outputLimit"].(type) {
	case float64:
		if v > 0 {
			return int(v)
		}
	case int:
		if v > 0 {
			return v
		}
	}

	return DefaultOutputLimit
}

func tail(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	return "... earlier output dropped ...\n" + s[len(s)-limit:]
}
