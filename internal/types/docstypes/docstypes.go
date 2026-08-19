package docstypes

type Tool struct {
	Name string `json:"name"`
	Does string `json:"does"`
}

type SpecKey struct {
	Key      string `json:"key"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
	Means    string `json:"means"`
}

type Example struct {
	Tool  string `json:"tool"`
	Input string `json:"input"`
}

type Engine struct {
	Name    string    `json:"name"`
	Port    string    `json:"port"`
	Tagline string    `json:"tagline"`
	Why     string    `json:"why"`
	Tools   []Tool    `json:"tools"`
	Spec    []SpecKey `json:"spec,omitempty"`
	Example Example   `json:"example"`
	Notes   string    `json:"notes,omitempty"`
	Version string    `json:"version"`
}

type Decision struct {
	Title    string `json:"title"`
	Decided  string `json:"decided"`
	Why      string `json:"why"`
	Costs    string `json:"costs"`
	Reversed bool   `json:"reversed,omitempty"`
}

type Decisions struct {
	Groups []DecisionGroup `json:"groups"`
}

type DecisionGroup struct {
	Name      string     `json:"name"`
	Decisions []Decision `json:"decisions"`
}

type File struct {
	Path    string
	Content string
}
