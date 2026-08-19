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
	Kind string         `json:"kind" jsonschema:"Resource kind, for example directory or table"`
	Name string         `json:"name" jsonschema:"Unique name within the kind"`
	Spec map[string]any `json:"spec,omitempty" jsonschema:"Kind specific properties"`
}

func (r Resource) ID() string {
	return r.Kind + "/" + r.Name
}

type Ownership struct {
	Resource string `json:"resource" jsonschema:"Resource id, kind slash name"`
	Manager  string `json:"manager" jsonschema:"Alias of the manager that created it"`
}

type DeclareOutput struct {
	Resources []Resource `json:"resources"`
}

type ReconcileInput struct {
	Manager   string         `json:"manager"`
	Resources []Resource     `json:"resources"`
	Owned     []Ownership    `json:"owned,omitempty"`
	Spec      map[string]any `json:"spec,omitempty"`
}

type ReconcileOutput struct {
	Owned   []Ownership `json:"owned"`
	Actions []string    `json:"actions"`
}

type RepoCheckout struct {
	Name string `json:"name"`
	Path string `json:"path"`
	SHA  string `json:"sha"`
}

type Target struct {
	Alias   string   `json:"alias"`
	Forge   string   `json:"forge,omitempty"`
	ForgeCI string   `json:"forgeCI,omitempty"`
	In      []string `json:"in,omitempty"`
}

type RunInput struct {
	Revision string            `json:"revision"`
	Stage    string            `json:"stage"`
	Substage string            `json:"substage"`
	Targets  []Target          `json:"targets"`
	Params   map[string]string `json:"params,omitempty"`
	Repos    []RepoCheckout    `json:"repos,omitempty"`
	Root     string            `json:"root"`
	Spec     map[string]any    `json:"spec,omitempty"`
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
	Revision  string            `json:"revision" jsonschema:"The revision being released"`
	Version   string            `json:"version" jsonschema:"The semver tag to publish under"`
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
