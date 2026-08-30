package statecontroller

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
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
	ErrPath  = errors.New("the state engine needs spec.path naming the state repo")
	ErrKind  = errors.New("unknown state kind")
	ErrKinds = errors.New("spec.kinds must be kebab-case names")
)

var kinds = map[string]string{
	KindRevision: "revisions",
	KindRun:      "runs",
	KindOwned:    "owned",
}

var kindPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// kindsFor merges spec.kinds over the built-in kinds. An extra kind maps to a
// directory of its own name, so a caller can store new record families without
// this engine learning what they mean.
func kindsFor(spec map[string]any) (map[string]string, error) {
	extras, ok := spec["kinds"]
	if !ok {
		return kinds, nil
	}

	list, ok := extras.([]any)
	if !ok {
		return nil, ErrKinds
	}

	merged := make(map[string]string, len(kinds)+len(list))
	for k, v := range kinds {
		merged[k] = v
	}

	for _, e := range list {
		name, ok := e.(string)
		if !ok || !kindPattern.MatchString(name) {
			return nil, fmt.Errorf("adding kind %v: %w", e, ErrKinds)
		}

		if _, exists := merged[name]; !exists {
			merged[name] = name
		}
	}

	return merged, nil
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

	known, err := kindsFor(spec)
	if err != nil {
		return citypes.DeclareOutput{}, err
	}

	dirs := make([]string, 0, len(known))
	for _, dir := range known {
		dirs = append(dirs, dir)
	}

	sort.Strings(dirs)

	out := citypes.DeclareOutput{Resources: []citypes.Resource{{Kind: "directory", Name: root}}}
	for _, dir := range dirs {
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

	committed, err := c.commit(ctx, root, target, in.Kind, in.Key)
	if err != nil {
		return citypes.StateGetOutput{}, err
	}

	if committed {
		if err := c.push(ctx, root); err != nil {
			return citypes.StateGetOutput{}, fmt.Errorf("recording %s %q: %w", in.Kind, in.Key, err)
		}
	}

	return citypes.StateGetOutput{Found: true, Payload: in.Payload}, nil
}

// push sends the store to its remote, which is what makes a record outlive
// the run that wrote it.
//
// Without it a CI run reads whatever the remote holds, commits, and throws
// the commit away with the container. Every minted revision, every run
// record and the ownership record died that way. forge-factory reads
// revisions/<id>.json from this repo's REMOTE to pin a remote run's member
// shas, so the record was never found and every remote run fell back to
// floating tags with only a log line to say so.
//
// There is no flag for this. A store that does not persist is not a store,
// and a knob to turn persistence off is a knob that configures the bug
// above. The remote is the declaration: a store that has one is pushed, and
// one that does not is a scratch store and is left alone.
func (c *Controller) push(ctx context.Context, root string) error {
	if c.git == nil {
		return nil
	}

	has, err := c.git.HasRemote(ctx, root)
	if err != nil {
		return err
	}

	if !has {
		// A scratch store with no origin is a legitimate state, and a test
		// fixture is always in it.
		return nil
	}

	branch, err := c.git.Branch(ctx, root)
	if err != nil {
		return err
	}

	if branch == "" {
		return nil
	}

	err = c.git.Push(ctx, root, branch)
	if !errors.Is(err, gitadapter.ErrRejected) {
		return err
	}

	// The remote moved under this run. Every record is a file of its own, so
	// replaying this run's commits on top of the other run's is the whole
	// resolution: neither run loses a record and nobody picks a winner.
	if err := c.git.PullRebase(ctx, root, branch); err != nil {
		return err
	}

	return c.git.Push(ctx, root, branch)
}

func (c *Controller) List(ctx context.Context, in citypes.StateGetInput) (citypes.StateListOutput, error) {
	root, err := rootOf(in.Spec)
	if err != nil {
		return citypes.StateListOutput{}, err
	}

	known, err := kindsFor(in.Spec)
	if err != nil {
		return citypes.StateListOutput{}, err
	}

	dir, ok := known[in.Kind]
	if !ok {
		return citypes.StateListOutput{}, fmt.Errorf("listing %q: %w", in.Kind, ErrKind)
	}

	base := filepath.Join(root, dir, filepath.FromSlash(in.Key))

	names, err := c.fs.Walk(base)
	if err != nil {
		return citypes.StateListOutput{}, fmt.Errorf("listing %s: %w", in.Kind, err)
	}

	keys := make([]string, 0, len(names))
	for _, n := range names {
		keys = append(keys, strings.TrimSuffix(n, ".json"))
	}

	return citypes.StateListOutput{Keys: keys}, nil
}

// commit records exactly the file this write produced. It stages that
// one path and commits only what is staged: the store often shares a
// repo with other work, and sweeping a dirty tree into a "ci:" commit
// buries changes the engine had no business recording.
// commit records the written file and answers whether a commit happened. An
// identical payload stages nothing, and a caller that pushed on that would
// push nothing every time a run re-recorded a record it already had.
func (c *Controller) commit(ctx context.Context, root, target, kind, key string) (bool, error) {
	if c.git == nil {
		return false, nil
	}

	isRepo, err := c.git.IsRepo(ctx, root)
	if err != nil {
		return false, fmt.Errorf("recording %s %q: %w", kind, key, err)
	}

	if !isRepo {
		if err := c.git.Init(ctx, root); err != nil {
			return false, fmt.Errorf("recording %s %q: %w", kind, key, err)
		}
	}

	// git -C root resolves pathspecs inside the repo, so the target is
	// handed over relative to it - a store named by a relative path would
	// otherwise stage a root-prefixed path that exists nowhere.
	relTarget := target
	if rel, err := filepath.Rel(root, target); err == nil && !strings.HasPrefix(rel, "..") {
		relTarget = rel
	}

	if err := c.git.Add(ctx, root, relTarget); err != nil {
		return false, fmt.Errorf("recording %s %q: %w", kind, key, err)
	}

	staged, err := c.git.Staged(ctx, root)
	if err != nil {
		return false, fmt.Errorf("recording %s %q: %w", kind, key, err)
	}

	if !staged {
		return false, nil
	}

	if err := c.git.Commit(ctx, root, fmt.Sprintf("ci: %s %s", kind, key)); err != nil {
		return false, fmt.Errorf("recording %s %q: %w", kind, key, err)
	}

	return true, nil
}

func (c *Controller) pathFor(spec map[string]any, kind, key string) (string, error) {
	root, err := rootOf(spec)
	if err != nil {
		return "", err
	}

	known, err := kindsFor(spec)
	if err != nil {
		return "", err
	}

	dir, ok := known[kind]
	if !ok {
		return "", fmt.Errorf("resolving %q: %w", kind, ErrKind)
	}

	if key == "" {
		return "", fmt.Errorf("resolving %s: key is required", kind)
	}

	clean := path.Clean("/" + strings.TrimSpace(key))

	return filepath.Join(root, dir, filepath.FromSlash(clean)+".json"), nil
}

// rootOf answers where the state repo is on disk.
//
// spec.path is relative to the pipeline root, not to whatever directory
// forge-ci happened to start in. The core passes that root in spec.root, and
// joining is a no-op for the ordinary case where they are the same. Where
// they are not - the generated CI does `cd <member>` then `--root ..` - the
// store used to land one level too deep, inside a member whose .gitignore
// then swallowed every record.
//
// An absolute path is taken as written; an instance naming one means it.
func rootOf(spec map[string]any) (string, error) {
	dir, _ := spec["path"].(string)
	if strings.TrimSpace(dir) == "" {
		return "", ErrPath
	}

	root, _ := spec["root"].(string)
	if root == "" || filepath.IsAbs(dir) {
		return dir, nil
	}

	return filepath.Join(root, dir), nil
}
