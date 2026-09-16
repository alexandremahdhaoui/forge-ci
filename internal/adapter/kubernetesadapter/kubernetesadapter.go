package kubernetesadapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

type Cluster struct {
	client    kubernetes.Interface
	apiServer string
}

func New(kubeconfigPath string) (Cluster, error) {
	if strings.TrimSpace(kubeconfigPath) == "" {
		return Cluster{}, errors.New(
			"building the client of the cluster: it was handed no kubeconfig file, " +
				"and the cluster credential is declared. nothing ambient names the cluster")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	if err != nil {
		return Cluster{}, fmt.Errorf("reading the client configuration of the cluster: %w", err)
	}

	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return Cluster{}, fmt.Errorf("building the client of the cluster: %w", err)
	}

	return Cluster{client: client, apiServer: config.Host}, nil
}

func (c Cluster) APIServer() string {
	return c.apiServer
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
