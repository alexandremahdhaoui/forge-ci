package citypes

const (
	TalosBundleNamespace = "kube-system"
	TalosBundleSecret    = "talos-secret-bundle"
	TalosBundleKey       = "bundle.yaml"

	TalosRecoveryBundleFile      = "secrets.yaml"
	TalosRecoveryTalosconfigFile = "talosconfig"
)

type TalosContext struct {
	Endpoints []string
	Node      string
}
