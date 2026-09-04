package citypes

import (
	"time"

	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
)

type Resource struct {
	Kind string `json:"kind" jsonschema:"Resource kind, for example directory or table"`
	Name string `json:"name" jsonschema:"Unique name within the kind"`
	// BootstrapOnly marks a resource an apply must not touch. A credential
	// cannot be converged: it is written blind, because nothing can be read
	// back to compare against, so reconciling one is only writing it again -
	// and doing that on every run means every run must hold the rights to
	// rewrite it. Only a bootstrap is responsible for credentials.
	BootstrapOnly bool           `json:"bootstrapOnly,omitempty" jsonschema:"Realized by a bootstrap and never by an apply"`
	Spec          map[string]any `json:"spec,omitempty" jsonschema:"Kind specific properties"`
}

func (r Resource) ID() string {
	return r.Kind + "/" + r.Name
}

type Ownership struct {
	Resource string `json:"resource" jsonschema:"Resource id, kind slash name"`
	Manager  string `json:"manager" jsonschema:"Alias of the manager that created it"`
}

// DeclareInput is what an engine is asked with when it reports what it
// needs: its spec, the pipeline root, and the pipeline's stages by name. The
// stages are for a compute engine that renders the run as one job per stage
// or per substage - it needs the names before anything runs. An engine that
// renders nothing ignores them.
type DeclareInput struct {
	Spec   map[string]any  `json:"spec,omitempty"`
	Root   string          `json:"root,omitempty"`
	Stages []DeclaredStage `json:"stages,omitempty"`
}

// DeclaredStage is one stage by name with its substages, in order. An engine
// that renders jobs is handed these, so DisplayName is what a person should
// see where an engine would otherwise show the identifier.
type DeclaredStage struct {
	Name        string             `json:"name"`
	DisplayName string             `json:"displayName,omitempty"`
	Substages   []DeclaredSubstage `json:"substages,omitempty"`
}

// DeclaredSubstage is one substage by name.
type DeclaredSubstage struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	// Needs names the substages of the same stage this one waits on, for
	// an engine that renders one job per substage and has to wire the
	// order between them.
	Needs []string `json:"needs,omitempty"`
}

type DeclareOutput struct {
	Resources []Resource `json:"resources"`
}

// DefaultCommitPrefix is what the commit a manager writes to make its own
// changes durable starts with, when the pipeline names no other. The core
// scores a subject starting with it as a patch, so a converge push proves
// the revision and releases; a factory that lists the prefix in a semantic
// list overrides that.
const DefaultCommitPrefix = "forge-ci:"

// SelfReconcileMessage is the commit message a manager writes under a
// prefix: the prefix, then the two words that say who wrote it and why.
func SelfReconcileMessage(prefix string) string {
	if prefix == "" {
		prefix = DefaultCommitPrefix
	}

	return prefix + " self reconcile"
}

type ReconcileInput struct {
	Manager   string      `json:"manager"`
	Resources []Resource  `json:"resources"`
	Owned     []Ownership `json:"owned,omitempty"`
	// Bootstrap says whether this is the provisioning ceremony. A resource
	// marked BootstrapOnly is handed over either way - ownership is what
	// stops another manager claiming it - but it is realized only when this
	// is true.
	Bootstrap bool           `json:"bootstrap,omitempty"`
	Spec      map[string]any `json:"spec,omitempty"`
	// DryRun asks what would happen and forbids every write. The manager
	// reads actual state exactly as it always does and settles nothing, so
	// a plan that says nothing would change is a promise the next run keeps.
	DryRun bool `json:"dryRun,omitempty"`
	// Force rewrites a resource that exists and cannot be compared. Only a
	// write-only resource needs it: a put over an existing Actions secret
	// silently replaces whatever was there, and two operators with different
	// credentials leave one winner. Without this, an existing one is kept.
	Force bool `json:"force,omitempty"`
	// CommitPrefix is what the commit a settle writes starts with: the
	// pipeline's versioning.selfReconcileCommitPrefix, so the release
	// decision recognizes the commit it caused. Empty means the default.
	CommitPrefix string `json:"commitPrefix,omitempty"`
}

type ReconcileOutput struct {
	Owned   []Ownership `json:"owned"`
	Actions []string    `json:"actions"`
	// Changed says the manager found a difference between what was declared
	// and what existed, and closed it. The caller stops the run: the tree it
	// was about to measure is the tree this reconcile just rewrote, and the
	// change is already durable, so the next run reads the corrected state
	// and this is false.
	Changed bool `json:"changed,omitempty"`
	// Published says the settle delivered the changes somewhere outside this
	// machine - a git push. It is what the caller's stop decision hangs on:
	// a published change re-fires the pipeline, so the run may stop
	// superseded; a change nobody published cannot re-trigger anything, and
	// a run that stopped for one would strand the pipeline forever.
	Published bool `json:"published,omitempty"`
}

type RepoCheckout struct {
	Name string `json:"name"`
	Path string `json:"path"`
	SHA  string `json:"sha"`
	// Needs names the repos that must finish before this one starts, when a
	// target runs in both. A compute engine groups a target's dirs into
	// waves from these edges; with none declared every dir is its own wave,
	// in the order given, which is one at a time.
	Needs []string `json:"needs,omitempty"`
}

type Target struct {
	Alias   string   `json:"alias"`
	Forge   string   `json:"forge,omitempty"`
	ForgeCI string   `json:"forgeCI,omitempty"`
	In      []string `json:"in,omitempty"`
}

type RunInput struct {
	Revision string `json:"revision"`
	// Version is the number this apply would release under, derived before
	// the first stage. A compute target stamps it into what it builds, so a
	// binary reports the release it shipped in rather than the nearest tag
	// its own repo happens to carry.
	Version  string `json:"version,omitempty"`
	Stage    string `json:"stage"`
	Substage string `json:"substage"`
	// Sync asks the compute engine to converge the workspace before any
	// target runs: manifests first (sync), then the dependency closure
	// (lock). On the wire rather than a core shell-out because the machine
	// that must converge is the one the engine runs targets on - a remote
	// runner would never be reached by anything the core executed here.
	Sync    bool              `json:"sync,omitempty"`
	Targets []Target          `json:"targets"`
	Params  map[string]string `json:"params,omitempty"`
	Repos   []RepoCheckout    `json:"repos,omitempty"`
	Root    string            `json:"root"`
	Spec    map[string]any    `json:"spec,omitempty"`
}

type ForgeResult struct {
	TestReports []forge.TestReport `json:"testReports,omitempty"`
	Artifacts   []forge.Artifact   `json:"artifacts,omitempty"`
}

type RunOutput struct {
	Status  Status       `json:"status"`
	Message string       `json:"message,omitempty"`
	Output  string       `json:"output,omitempty"`
	Forge   *ForgeResult `json:"forge,omitempty"`
}

type GateResult struct {
	Alias   string `json:"alias"`
	Status  Status `json:"status"`
	Message string `json:"message,omitempty"`
}

type GateInput struct {
	Run  Run            `json:"run"`
	Spec map[string]any `json:"spec,omitempty"`
}

type PromotionInput struct {
	Stage string         `json:"stage"`
	Runs  []Run          `json:"runs"`
	Spec  map[string]any `json:"spec,omitempty"`
}

type PromotionOutput struct {
	Advance bool   `json:"advance"`
	Reason  string `json:"reason"`
}

// ArtifactInput is what a release engine is handed. The revision is what was
// proven, the version is what to call it, and the artifacts are what forge
// already built.
type ArtifactInput struct {
	Revision string `json:"revision" jsonschema:"The revision being released"`
	// Version is decided by the core and by nothing else. An engine that
	// computes its own is a second authority, and two authorities are how
	// every member of a workspace drifted onto its own version line.
	Version string `json:"version" jsonschema:"The semver tag to publish under"`
	// TagPrefix namespaces the tag when one repo is released by more than
	// one factory. Empty is the default and means the tag is the version.
	TagPrefix string            `json:"tagPrefix,omitempty" jsonschema:"Namespace in front of the tag, empty for none"`
	Repos     map[string]string `json:"repos,omitempty" jsonschema:"Repo name to commit SHA"`
	Artifacts []forge.Artifact  `json:"artifacts,omitempty" jsonschema:"What forge built"`
	Spec      map[string]any    `json:"spec,omitempty" jsonschema:"Engine specific configuration"`
}

// ArtifactOutput says what reached the outside world.
type ArtifactOutput struct {
	Published bool     `json:"published" jsonschema:"Whether anything was published"`
	URL       string   `json:"url,omitempty" jsonschema:"Where it landed"`
	Reason    string   `json:"reason,omitempty" jsonschema:"Why it did not publish"`
	Tagged    []string `json:"tagged,omitempty" jsonschema:"Repos that carry the tag now"`
	// Index is the distribution index the engine staged, as JSON text: every
	// asset's digest under the revision and tag. The core records it beside
	// the release so a later run can say whether the bytes it built are the
	// bytes already released. A string and never bytes: a byte field crosses
	// the wire as base64 and the generated schema rejects it.
	Index string `json:"index,omitempty" jsonschema:"The staged distribution index as JSON text"`
}

// ArtifactPutInput hands the files a run built to the compute engine's own
// world, so a later phase or a later run can ask for them back. A build
// leaves files on one runner's disk and the run record keeps only their
// paths; the release then reads paths on a runner that never built them.
type ArtifactPutInput struct {
	Revision  string           `json:"revision"`
	Artifacts []forge.Artifact `json:"artifacts"`
	Root      string           `json:"root"`
	Spec      map[string]any   `json:"spec,omitempty"`
}

// ArtifactPutOutput is the same records with each location rewritten to a
// URL the engine can serve again.
type ArtifactPutOutput struct {
	Artifacts []forge.Artifact `json:"artifacts"`
}

// ArtifactGetInput brings artifacts a put handed out back to paths under
// root.
type ArtifactGetInput struct {
	Revision  string           `json:"revision"`
	Artifacts []forge.Artifact `json:"artifacts"`
	Root      string           `json:"root"`
	Spec      map[string]any   `json:"spec,omitempty"`
}

// ArtifactGetOutput is the same records with each location a path under
// root again, bytes verified.
type ArtifactGetOutput struct {
	Artifacts []forge.Artifact `json:"artifacts"`
}

type Revision struct {
	ID        string            `json:"id"`
	CreatedAt time.Time         `json:"createdAt"`
	Repos     map[string]string `json:"repos,omitempty"`
	Dirty     []string          `json:"dirty,omitempty"`
}

type Run struct {
	Revision  string       `json:"revision"`
	Stage     string       `json:"stage"`
	Substage  string       `json:"substage"`
	Engine    string       `json:"engine"`
	Status    Status       `json:"status"`
	StartedAt time.Time    `json:"startedAt"`
	Duration  float64      `json:"duration"`
	Message   string       `json:"message,omitempty"`
	Output    string       `json:"output,omitempty"`
	Forge     *ForgeResult `json:"forge,omitempty"`
	Gates     []GateResult `json:"gates,omitempty"`
}

type StateGetInput struct {
	Kind string         `json:"kind"`
	Key  string         `json:"key,omitempty"`
	Spec map[string]any `json:"spec,omitempty"`
}

type StatePutInput struct {
	Kind    string         `json:"kind"`
	Key     string         `json:"key"`
	Payload string         `json:"payload"`
	Spec    map[string]any `json:"spec,omitempty"`
}

type StateGetOutput struct {
	Found   bool   `json:"found"`
	Payload string `json:"payload,omitempty"`
}

type StateListOutput struct {
	Keys []string `json:"keys"`
}

type TriggerOutput struct {
	Changed     bool   `json:"changed"`
	Reason      string `json:"reason,omitempty"`
	Fingerprint string `json:"fingerprint"`
}
