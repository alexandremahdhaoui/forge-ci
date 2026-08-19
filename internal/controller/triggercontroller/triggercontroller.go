package triggercontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var ErrWatch = errors.New("the watch trigger needs spec.watch naming at least one directory")

type Controller struct {
	git gitadapter.Git
}

func New(git gitadapter.Git) *Controller {
	return &Controller{git: git}
}

func (c *Controller) Declare(map[string]any) (citypes.DeclareOutput, error) {
	return citypes.DeclareOutput{Resources: []citypes.Resource{}}, nil
}

func (c *Controller) Poll(ctx context.Context, spec map[string]any) (citypes.TriggerOutput, error) {
	watched, err := watchList(spec)
	if err != nil {
		return citypes.TriggerOutput{}, err
	}

	parts := make([]string, 0, len(watched))

	for _, dir := range watched {
		sha, err := c.git.HeadSHA(ctx, dir)
		if err != nil {
			return citypes.TriggerOutput{}, fmt.Errorf("watching %s: %w", dir, err)
		}

		dirty, err := c.git.Dirty(ctx, dir)
		if err != nil {
			return citypes.TriggerOutput{}, fmt.Errorf("watching %s: %w", dir, err)
		}

		parts = append(parts, fmt.Sprintf("%s=%s dirty=%t", dir, sha, dirty))
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
