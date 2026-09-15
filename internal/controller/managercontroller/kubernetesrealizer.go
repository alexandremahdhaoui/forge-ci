package managercontroller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const KindSecret = "secret"

type Kubernetes interface {
	APIServer() string
	Secret(ctx context.Context, namespace, name string) (secret *corev1.Secret, found bool, err error)
}

type KubernetesRealizer struct {
	ctx     context.Context
	cluster Kubernetes
	helm    Helm
}

var _ Realizer = KubernetesRealizer{}

func NewKubernetesRealizer(
	ctx context.Context, cluster Kubernetes, helm Helm,
) (KubernetesRealizer, error) {
	var missing []string

	if cluster == nil {
		missing = append(missing, "cluster")
	}

	if helm == nil {
		missing = append(missing, "helm")
	}

	if len(missing) > 0 {
		return KubernetesRealizer{}, fmt.Errorf(
			"building the kubernetes realizer: it was handed no %s client, "+
				"and it reaches the cluster through one of each",
			strings.Join(missing, " client and no "))
	}

	return KubernetesRealizer{ctx: ctx, cluster: cluster, helm: helm}, nil
}

func (KubernetesRealizer) Kind() string {
	return "kubernetes"
}

func (r KubernetesRealizer) Realize(res citypes.Resource, opts Options) (Action, error) {
	switch res.Kind {
	case KindSecret:
		return r.realizeSecret(res)
	case KindHelmRelease:
		return r.realizeHelmRelease(res, opts)
	default:
		return Action{}, fmt.Errorf(
			"the kubernetes manager cannot realize kind %q, it knows %s and %s",
			res.Kind, KindSecret, KindHelmRelease)
	}
}

func (r KubernetesRealizer) realizeSecret(res citypes.Resource) (Action, error) {
	namespace, err := citypes.SpecString(res.Spec, "namespace")
	if err != nil {
		return Action{}, err
	}

	name, err := citypes.SpecString(res.Spec, "name")
	if err != nil {
		return Action{}, err
	}

	if namespace == "" || name == "" {
		return Action{}, errors.New("spec.namespace and spec.name are required")
	}

	id := namespace + "/" + name

	keys, err := declaredKeys(res.Spec, id)
	if err != nil {
		return Action{}, err
	}

	apiServer, err := citypes.SpecString(res.Spec, "apiServer")
	if err != nil {
		return Action{}, err
	}

	if err := r.confirmAPIServer(apiServer, "secret "+id); err != nil {
		return Action{}, err
	}

	live, found, err := r.cluster.Secret(r.ctx, namespace, name)
	if err != nil {
		return Action{}, fmt.Errorf("reading secret %s: %w", id, err)
	}

	if !found {
		return Action{}, errors.New(mintingSteps(id, keys))
	}

	if live == nil {
		return Action{}, fmt.Errorf(
			"reading secret %s: the cluster answered that it holds one and handed back nothing", id)
	}

	if missing := keysTheLiveSecretLacks(live, keys); len(missing) > 0 {
		return Action{}, fmt.Errorf(
			"reading secret %s: it holds no %s, and this declaration needs %s. "+
				"A person writes every key of this secret by hand, and nothing in this toolchain writes one",
			id, strings.Join(missing, " and no "), strings.Join(keys, ", "))
	}

	return Kept("kept secret " + id), nil
}

func mintingSteps(id string, keys []string) string {
	return "reading secret " + id + ": the cluster holds no secret of that name, " +
		"and nothing in this toolchain writes one. A person mints it by hand in three steps. " +
		"First, generate an ed25519 key pair. " +
		"Second, register the public half as a read only deploy key on the git repository " +
		"the cluster reads from. " +
		"Third, write the private half and the host keys of that repository into the cluster " +
		"as secret " + id + ", under the keys " + strings.Join(keys, ", ")
}

func keysTheLiveSecretLacks(live *corev1.Secret, keys []string) []string {
	var missing []string

	for _, key := range keys {
		if _, held := live.Data[key]; held {
			continue
		}

		if _, held := live.StringData[key]; held {
			continue
		}

		missing = append(missing, key)
	}

	return missing
}

func declaredKeys(spec map[string]any, id string) ([]string, error) {
	keys, err := citypes.SpecStringSlice(spec, "keys")
	if err != nil {
		return nil, fmt.Errorf("reading the keys of secret %s: %w", id, err)
	}

	if len(keys) == 0 {
		return nil, fmt.Errorf(
			"reading the keys of secret %s: spec.keys is required, "+
				"and it names every key the live secret must hold", id)
	}

	for _, key := range keys {
		if key == "" {
			return nil, fmt.Errorf(
				"reading the keys of secret %s: spec.keys holds a key with no name", id)
		}
	}

	return keys, nil
}
