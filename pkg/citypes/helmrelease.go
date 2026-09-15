package citypes

type HelmRelease struct {
	Namespace       string
	Name            string
	Chart           string
	Version         string
	Repository      string
	Values          map[string]any
	CreateNamespace bool
	Status          string
}
