package kubernetesadapter

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type Cluster struct {
	client kubernetes.Interface
}

func New() (Cluster, error) {
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return Cluster{}, fmt.Errorf("reading the client configuration of the cluster: %w", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return Cluster{}, fmt.Errorf("building the client of the cluster: %w", err)
	}

	return Cluster{client: client}, nil
}

func (c Cluster) Secret(ctx context.Context, namespace, name string) (*corev1.Secret, bool, error) {
	secret, err := c.client.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("getting secret %s/%s: %w", namespace, name, err)
	}

	return secret, true, nil
}

func (c Cluster) CreateSecret(ctx context.Context, secret *corev1.Secret) error {
	_, err := c.client.CoreV1().Secrets(secret.Namespace).Create(ctx, secret, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("posting secret %s/%s: %w", secret.Namespace, secret.Name, err)
	}

	return nil
}

func (c Cluster) ReplaceSecret(ctx context.Context, secret *corev1.Secret) error {
	_, err := c.client.CoreV1().Secrets(secret.Namespace).Update(ctx, secret, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("putting secret %s/%s: %w", secret.Namespace, secret.Name, err)
	}

	return nil
}
