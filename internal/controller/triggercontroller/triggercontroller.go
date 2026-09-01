package triggercontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var ErrWatch = errors.New("the watch trigger needs spec.watch naming at least one directory")

type Controller struct {
	git gitadapter.Git
	fs  fsadapter.FS
}

func New(git gitadapter.Git) *Controller {
	// The real filesystem, not an injection point: the only read is the
	// factory's generated-file manifest at the root the poll runs from,
	// and an absent file is the ordinary case a fixture wants anyway.
	return &Controller{git: git, fs: fsadapter.New()}
}

func (c *Controller) Declare(spec map[string]any) (citypes.DeclareOutput, error) {
	notify, err := parseNotify(spec)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	if notify == nil {
		return citypes.DeclareOutput{Resources: []citypes.Resource{}}, nil
	}

	watched, err := watchList(spec)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	resources, err := notifyResources(watched, notify)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	return citypes.DeclareOutput{Resources: resources}, nil
}

func (c *Controller) Poll(ctx context.Context, spec map[string]any) (citypes.TriggerOutput, error) {
	watched, err := watchList(spec)
	if err != nil {
		return citypes.TriggerOutput{}, err
	}

	parts := make([]string, 0, len(watched))

	// The factory's generated files are excluded from the movement
	// measurement, with the same list the revision's dirty measurement
	// uses: a poll and an apply that disagree on what counts as a move
	// re-split two decisions one commit already unified.
	exclusions := generatedExclusions(c.fs, ".")

	for _, dir := range watched {
		sha, err := c.git.HeadSHA(ctx, dir)
		if err != nil {
			return citypes.TriggerOutput{}, fmt.Errorf("watching %s: %w", dir, err)
		}

		worktree, err := c.git.WorktreeHash(ctx, dir, exclusions[dir]...)
		if err != nil {
			return citypes.TriggerOutput{}, fmt.Errorf("watching %s: %w", dir, err)
		}

		parts = append(parts, fmt.Sprintf("%s=%s+%s", dir, sha, worktree))
	}

	sort.Strings(parts)

	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	fingerprint := hex.EncodeToString(sum[:])

	previous, _ := spec["previous"].(string)

	out := citypes.TriggerOutput{Fingerprint: fingerprint, Changed: previous != fingerprint}
	if out.Changed {
		out.Reason = "the watched repos moved"
		if previous == "" {
			out.Reason = "first look at the watched repos"
		}
	}

	return out, nil
}

func watchList(spec map[string]any) ([]string, error) {
	raw, ok := spec["watch"].([]any)
	if !ok {
		if typed, ok := spec["watch"].([]string); ok && len(typed) > 0 {
			return typed, nil
		}

		return nil, ErrWatch
	}

	dirs := make([]string, 0, len(raw))

	for _, item := range raw {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			dirs = append(dirs, s)
		}
	}

	if len(dirs) == 0 {
		return nil, ErrWatch
	}

	return dirs, nil
}

// generatedManifestPath is where forge-factory records the files it
// generates, root-relative - the same contract file the revision's dirty
// measurement reads. This controller keeps its own copy of the reading:
// controllers do not import each other, and the two measurements must
// agree on the list or a poll fires for churn an apply will not see.
const generatedManifestPath = ".forge/factory-generated.json"

type generatedManifest struct {
	Version int      `json:"version"`
	Files   []string `json:"files"`
}

// generatedExclusions maps each watched directory to the repo-relative
// paths the factory generates inside it. An absent or unreadable manifest
// answers nothing, and the measurement is then what it always was.
func generatedExclusions(fs fsadapter.FS, root string) map[string][]string {
	raw, err := fs.ReadFile(filepath.Join(root, filepath.FromSlash(generatedManifestPath)))
	if err != nil {
		return map[string][]string{}
	}

	var manifest generatedManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return map[string][]string{}
	}

	exclusions := map[string][]string{}

	for _, file := range manifest.Files {
		repo, rest, found := strings.Cut(path.Clean(file), "/")
		if !found || repo == "" || rest == "" || strings.HasPrefix(repo, ".") {
			continue
		}

		exclusions[repo] = append(exclusions[repo], rest)
	}

	return exclusions
}
