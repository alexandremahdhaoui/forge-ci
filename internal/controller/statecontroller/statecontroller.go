package statecontroller

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
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
	ErrLFS   = errors.New("spec.lfs must name declared kinds")
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

// lfsFor answers which kinds ride git LFS. A kind named here has its payload
// stored as an LFS object rather than a diffable blob: the register's
// dependency locks are the case - the content only needs to persist and be
// verified by hash, never to diff. Every name must be a declared kind, so a
// typo is a refusal here and not a rule that silently tracks nothing.
func lfsFor(spec map[string]any) (map[string]bool, error) {
	raw, ok := spec["lfs"]
	if !ok {
		return nil, nil
	}

	list, ok := raw.([]any)
	if !ok {
		return nil, ErrLFS
	}

	known, err := kindsFor(spec)
	if err != nil {
		return nil, err
	}

	out := make(map[string]bool, len(list))

	for _, e := range list {
		name, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("reading lfs entry %v: %w", e, ErrLFS)
		}

		if _, declared := known[name]; !declared {
			return nil, fmt.Errorf("lfs names %q, which no kind declares: %w", name, ErrLFS)
		}

		out[name] = true
	}

	return out, nil
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

	// The kinds are validated and NOT declared. A kind's directory exists
	// the moment its first record is written - Put creates the path - and
	// an empty kind is genuinely nothing to converge. Declaring one was
	// worse than useless: git cannot carry an empty directory, so every
	// fresh CI clone lacked it, every reconcile re-created it and reported
	// a change, and the run stopped superseded - forever, because a
	// directory creation pushes nothing that could fire the superseding
	// run. Only the store root is declared.
	if _, err := kindsFor(spec); err != nil {
		return citypes.DeclareOutput{}, err
	}

	return citypes.DeclareOutput{Resources: []citypes.Resource{{Kind: "directory", Name: root}}}, nil
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

	lfs, err := lfsFor(in.Spec)
	if err != nil {
		return citypes.StateGetOutput{}, fmt.Errorf("writing %s %q: %w", in.Kind, in.Key, err)
	}

	targets := []string{target}

	if lfs[in.Kind] {
		known, err := kindsFor(in.Spec)
		if err != nil {
			return citypes.StateGetOutput{}, err
		}

		attrs, err := c.ensureLFSAttributes(root, known[in.Kind])
		if err != nil {
			return citypes.StateGetOutput{}, fmt.Errorf("writing %s %q: %w", in.Kind, in.Key, err)
		}

		// The attributes travel in the same scoped commit as the record: a
		// rule with no filter configured, or a filter with no rule, is a
		// store that lies about what its blobs are.
		targets = append(targets, attrs)
	}

	committed, err := c.commit(ctx, root, targets, in.Kind, in.Key, lfs[in.Kind])
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

// ensureLFSAttributes keeps the one line that marks a kind's directory as
// LFS-tracked, and answers the .gitattributes path. Appending is convergent:
// a line already there is left alone, and nothing this engine did not write
// is touched.
func (c *Controller) ensureLFSAttributes(root, dir string) (string, error) {
	path := filepath.Join(root, ".gitattributes")
	rule := dir + "/** filter=lfs diff=lfs merge=lfs -text"

	existing := ""

	exists, err := c.fs.Exists(path)
	if err != nil {
		return "", err
	}

	if exists {
		raw, err := c.fs.ReadFile(path)
		if err != nil {
			return "", err
		}

		existing = string(raw)
	}

	for _, line := range strings.Split(existing, "\n") {
		if strings.TrimSpace(line) == rule {
			return path, nil
		}
	}

	if existing != "" && !strings.HasSuffix(existing, "\n") {
		existing += "\n"
	}

	if err := c.fs.WriteFile(path, []byte(existing+rule+"\n")); err != nil {
		return "", err
	}

	return path, nil
}

// commit records exactly the files this write produced, and answers whether a
// commit happened. An identical payload stages nothing, and a caller that
// pushed on that would push nothing every time a run re-recorded a record it
// already had.
//
// Only the named paths are staged, asked about and committed. The store often
// shares a repo with other work, and this used to ask git about the whole
// index and then commit with no pathspec, so a human's staged file was swept
// into a "ci:" commit and pushed under the engine's name.
func (c *Controller) commit(ctx context.Context, root string, targets []string, kind, key string, lfs bool) (bool, error) {
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

	// The filters go in before the first add: the clean filter is what turns
	// the blob into a pointer, and adding without it commits full content
	// under an attribute that promises otherwise.
	if lfs {
		if err := c.git.LFSInstall(ctx, root); err != nil {
			return false, fmt.Errorf("recording %s %q: %w", kind, key, err)
		}
	}

	// git -C root resolves pathspecs inside the repo, so each target is
	// handed over relative to it - a store named by a relative path would
	// otherwise stage a root-prefixed path that exists nowhere.
	//
	// A target that does not resolve under the root is refused rather than
	// passed through absolute. Handing `git -C root` a path outside the repo
	// is not a record this engine can write, and falling back to it turned a
	// bad spec.path into a confusing git error somewhere else.
	relTargets := make([]string, 0, len(targets))

	for _, target := range targets {
		relTarget, err := filepath.Rel(root, target)
		if err != nil || strings.HasPrefix(relTarget, "..") {
			return false, fmt.Errorf(
				"recording %s %q: %s is outside the state root %s", kind, key, target, root)
		}

		if err := c.git.Add(ctx, root, relTarget); err != nil {
			return false, fmt.Errorf("recording %s %q: %w", kind, key, err)
		}

		relTargets = append(relTargets, relTarget)
	}

	staged, err := c.git.Staged(ctx, root, relTargets...)
	if err != nil {
		return false, fmt.Errorf("recording %s %q: %w", kind, key, err)
	}

	if !staged {
		return false, nil
	}

	if err := c.git.Commit(ctx, root, fmt.Sprintf("ci: %s %s", kind, key), relTargets...); err != nil {
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
