package managercontroller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	KindSecret = "secret"

	SecretHashAnnotation = "forge-ci-secret-hash"

	secretHashLength = 12
)

type Kubernetes interface {
	Secret(ctx context.Context, namespace, name string) (secret *corev1.Secret, found bool, err error)
	CreateSecret(ctx context.Context, secret *corev1.Secret) error
	ReplaceSecret(ctx context.Context, secret *corev1.Secret) error
}

type KubernetesRealizer struct {
	ctx     context.Context
	cluster Kubernetes
}

var _ Realizer = KubernetesRealizer{}

func NewKubernetesRealizer(ctx context.Context, cluster Kubernetes) KubernetesRealizer {
	return KubernetesRealizer{ctx: ctx, cluster: cluster}
}

func (KubernetesRealizer) Kind() string {
	return "kubernetes"
}

func (r KubernetesRealizer) Realize(res citypes.Resource, opts Options) (Action, error) {
	switch res.Kind {
	case KindSecret:
		return r.realizeSecret(res, opts)
	default:
		return Action{}, fmt.Errorf(
			"the kubernetes manager cannot realize kind %q, it knows %s", res.Kind, KindSecret)
	}
}

func (r KubernetesRealizer) realizeSecret(res citypes.Resource, opts Options) (Action, error) {
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

	data, err := declaredData(res.Spec, id)
	if err != nil {
		return Action{}, err
	}

	hash := hashOfData(data)

	if r.cluster == nil {
		return Action{}, fmt.Errorf("reading secret %s: this manager carries no cluster client yet", id)
	}

	live, found, err := r.cluster.Secret(r.ctx, namespace, name)
	if err != nil {
		return Action{}, fmt.Errorf("reading secret %s: %w", id, err)
	}

	if !found {
		return r.createSecret(namespace, name, id, data, hash, opts)
	}

	if live == nil {
		return Action{}, fmt.Errorf(
			"reading secret %s: the cluster answered that it holds one and handed back nothing", id)
	}

	if live.Annotations[SecretHashAnnotation] == hash && !opts.Force {
		return Kept("kept secret " + id), nil
	}

	return r.replaceSecret(live, id, data, hash, opts)
}

func (r KubernetesRealizer) createSecret(
	namespace, name, id string, data map[string][]byte, hash string, opts Options,
) (Action, error) {
	text := "create secret " + id + " holding " + declaredKeys(data)

	if opts.DryRun {
		return Kept(opts.would(text)), nil
	}

	written := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        name,
			Annotations: map[string]string{SecretHashAnnotation: hash},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}

	if err := r.cluster.CreateSecret(r.ctx, written); err != nil {
		return Action{}, fmt.Errorf("creating secret %s: %w", id, err)
	}

	return Did("created secret " + id + " holding " + declaredKeys(data)), nil
}

func (r KubernetesRealizer) replaceSecret(
	live *corev1.Secret, id string, data map[string][]byte, hash string, opts Options,
) (Action, error) {
	text := "replace the data of secret " + id + " with " + declaredKeys(data)

	if opts.DryRun {
		return Kept(opts.would(text)), nil
	}

	written := live.DeepCopy()
	written.Data = data
	written.StringData = nil

	if written.Annotations == nil {
		written.Annotations = map[string]string{}
	}

	written.Annotations[SecretHashAnnotation] = hash

	if err := r.cluster.ReplaceSecret(r.ctx, written); err != nil {
		return Action{}, fmt.Errorf("replacing the data of secret %s: %w", id, err)
	}

	return Did("replaced the data of secret " + id + " with " + declaredKeys(data)), nil
}

func declaredData(spec map[string]any, id string) (map[string][]byte, error) {
	variables, err := citypes.SpecStringMap(spec, "data")
	if err != nil {
		return nil, fmt.Errorf("reading the data of secret %s: %w", id, err)
	}

	if len(variables) == 0 {
		return nil, fmt.Errorf(
			"reading the data of secret %s: spec.data is required, and it names one environment variable per key", id)
	}

	data := make(map[string][]byte, len(variables))

	for _, key := range sortedStringKeys(variables) {
		if key == "" {
			return nil, fmt.Errorf(
				"reading the data of secret %s: spec.data holds a key with no name", id)
		}

		variable := variables[key]

		if variable == "" {
			return nil, fmt.Errorf(
				"reading the data of secret %s: key %q names no environment variable", id, key)
		}

		value := os.Getenv(variable)

		if value == "" {
			return nil, fmt.Errorf(
				"reading the data of secret %s: key %q must hold the name of an environment variable, "+
					"and no variable of that name is set",
				id, key)
		}

		data[key] = []byte(value)
	}

	return data, nil
}

func hashOfData(data map[string][]byte) string {
	joined := []byte{}

	for _, key := range sortedDataKeys(data) {
		value := data[key]

		joined = append(joined, strconv.Itoa(len(key))...)
		joined = append(joined, ':')
		joined = append(joined, key...)
		joined = append(joined, strconv.Itoa(len(value))...)
		joined = append(joined, ':')
		joined = append(joined, value...)
	}

	sum := sha256.Sum256(joined)

	return hex.EncodeToString(sum[:])[:secretHashLength]
}

func declaredKeys(data map[string][]byte) string {
	return strings.Join(sortedDataKeys(data), ", ")
}

func sortedDataKeys(data map[string][]byte) []string {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

func sortedStringKeys(held map[string]string) []string {
	keys := make([]string, 0, len(held))
	for key := range held {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}
