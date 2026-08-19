package statecontroller

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	KindRevision = "revision"
	KindRun      = "run"
	KindOwned    = "owned"
)

var (
	ErrPath = errors.New("the state engine needs spec.path naming the state repo")
	ErrKind = errors.New("unknown state kind")
)

var kinds = map[string]string{
	KindRevision: "revisions",
	KindRun:      "runs",
	KindOwned:    "owned",
}

type Controller struct {
	fs  fsadapter.FS
	git gitadapter.Git
}

func New(fs fsadapter.FS, git gitadapter.Git) *Controller {
	return &Controller{fs: fs, git: git}
}

func (c *Controller) Declare(spec map[string]any) (citypes.DeclareOutput, error) {
	root, err := rootOf(spec)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	out := citypes.DeclareOutput{Resources: []citypes.Resource{{Kind: "directory", Name: root}}}
	for _, dir := range kinds {
		out.Resources = append(out.Resources,
			citypes.Resource{Kind: "directory", Name: filepath.Join(root, dir)})
	}

	return out, nil
}

func (c *Controller) Get(ctx context.Context, in citypes.StateGetInput) (citypes.StateGetOutput, error) {
	target, err := c.pathFor(in.Spec, in.Kind, in.Key)
	if err != nil {
		return citypes.StateGetOutput{}, err
	}

	exists, err := c.fs.Exists(target)
	if err != nil {
		return citypes.StateGetOutput{}, fmt.Errorf("reading %s %q: %w", in.Kind, in.Key, err)
	}

	if !exists {
		return citypes.StateGetOutput{Found: false}, nil
	}

	payload, err := c.fs.ReadFile(target)
	if err != nil {
		return citypes.StateGetOutput{}, fmt.Errorf("reading %s %q: %w", in.Kind, in.Key, err)
	}

	return citypes.StateGetOutput{Found: true, Payload: string(payload)}, nil
}

func (c *Controller) Put(ctx context.Context, in citypes.StatePutInput) (citypes.StateGetOutput, error) {
	root, err := rootOf(in.Spec)
	if err != nil {
		return citypes.StateGetOutput{}, err
	}

	target, err := c.pathFor(in.Spec, in.Kind, in.Key)
	if err != nil {
		return citypes.StateGetOutput{}, err
	}

	if err := c.fs.WriteFile(target, []byte(in.Payload)); err != nil {
		return citypes.StateGetOutput{}, fmt.Errorf("writing %s %q: %w", in.Kind, in.Key, err)
	}

	if err := c.commit(ctx, root, in.Kind, in.Key); err != nil {
		return citypes.StateGetOutput{}, err
	}

	return citypes.StateGetOutput{Found: true, Payload: in.Payload}, nil
}

func (c *Controller) List(ctx context.Context, in citypes.StateGetInput) (citypes.StateListOutput, error) {
	root, err := rootOf(in.Spec)
	if err != nil {
		return citypes.StateListOutput{}, err
	}

	dir, ok := kinds[in.Kind]
	if !ok {
		return citypes.StateListOutput{}, fmt.Errorf("listing %q: %w", in.Kind, ErrKind)
	}

	base := filepath.Join(root, dir, filepath.FromSlash(in.Key))

	names, err := c.fs.List(base)
	if err != nil {
		return citypes.StateListOutput{}, fmt.Errorf("listing %s: %w", in.Kind, err)
	}

	keys := make([]string, 0, len(names))
	for _, n := range names {
		keys = append(keys, strings.TrimSuffix(n, ".json"))
	}

	return citypes.StateListOutput{Keys: keys}, nil
}

func (c *Controller) commit(ctx context.Context, root, kind, key string) error {
	if c.git == nil {
		return nil
	}

	isRepo, err := c.git.IsRepo(ctx, root)
	if err != nil {
		return fmt.Errorf("recording %s %q: %w", kind, key, err)
	}

	if !isRepo {
		if err := c.git.Init(ctx, root); err != nil {
			return fmt.Errorf("recording %s %q: %w", kind, key, err)
		}
	}

	if err := c.git.Add(ctx, root, "."); err != nil {
		return fmt.Errorf("recording %s %q: %w", kind, key, err)
	}

	dirty, err := c.git.Dirty(ctx, root)
	if err != nil {
		return fmt.Errorf("recording %s %q: %w", kind, key, err)
	}

	if !dirty {
		return nil
	}

	if err := c.git.Commit(ctx, root, fmt.Sprintf("ci: %s %s", kind, key)); err != nil {
		return fmt.Errorf("recording %s %q: %w", kind, key, err)
	}

	return nil
}

func (c *Controller) pathFor(spec map[string]any, kind, key string) (string, error) {
	root, err := rootOf(spec)
	if err != nil {
		return "", err
	}

	dir, ok := kinds[kind]
	if !ok {
		return "", fmt.Errorf("resolving %q: %w", kind, ErrKind)
	}

	if key == "" {
		return "", fmt.Errorf("resolving %s: key is required", kind)
	}

	clean := path.Clean("/" + strings.TrimSpace(key))

	return filepath.Join(root, dir, filepath.FromSlash(clean)+".json"), nil
}

func rootOf(spec map[string]any) (string, error) {
	root, _ := spec["path"].(string)
	if strings.TrimSpace(root) == "" {
		return "", ErrPath
	}

	return root, nil
}
