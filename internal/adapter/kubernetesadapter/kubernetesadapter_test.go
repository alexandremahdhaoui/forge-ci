package kubernetesadapter

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	theNamespace  = "flux-system"
	theSecretName = "flux-deploy-key"
)

func declared(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: theNamespace, Name: theSecretName},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}
}

func TestReadingASecretTheClusterDoesNotHoldAnswersNotFoundAndNoError(t *testing.T) {
	t.Parallel()

	cluster := Cluster{client: fake.NewClientset()}

	secret, found, err := cluster.Secret(t.Context(), theNamespace, theSecretName)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Nil(t, secret)
}

func TestReadingASecretTheClusterHoldsAnswersFoundAndItsData(t *testing.T) {
	t.Parallel()

	cluster := Cluster{client: fake.NewClientset(declared(map[string][]byte{"identity": []byte("a key")}))}

	secret, found, err := cluster.Secret(t.Context(), theNamespace, theSecretName)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, map[string][]byte{"identity": []byte("a key")}, secret.Data)
}

func TestCreatingASecretPutsItInTheClusterWithTheDataItCarried(t *testing.T) {
	t.Parallel()

	cluster := Cluster{client: fake.NewClientset()}

	require.NoError(t,
		cluster.CreateSecret(t.Context(), declared(map[string][]byte{"identity": []byte("a key")})))

	secret, found, err := cluster.Secret(t.Context(), theNamespace, theSecretName)
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, map[string][]byte{"identity": []byte("a key")}, secret.Data)
}

func TestCreatingASecretTheClusterAlreadyHoldsReportsItByName(t *testing.T) {
	t.Parallel()

	cluster := Cluster{client: fake.NewClientset(declared(nil))}

	err := cluster.CreateSecret(t.Context(), declared(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "posting secret "+theNamespace+"/"+theSecretName)
}

func TestReplacingASecretDropsAKeyTheWrittenDataNoLongerCarries(t *testing.T) {
	t.Parallel()

	held := declared(map[string][]byte{
		"identity":    []byte("a key"),
		"known_hosts": []byte("a host"),
	})
	cluster := Cluster{client: fake.NewClientset(held)}

	require.NoError(t,
		cluster.ReplaceSecret(t.Context(), declared(map[string][]byte{"identity": []byte("a new key")})))

	secret, _, err := cluster.Secret(t.Context(), theNamespace, theSecretName)
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{"identity": []byte("a new key")}, secret.Data)
}

func TestReplacingASecretTheClusterDoesNotHoldReportsItByName(t *testing.T) {
	t.Parallel()

	cluster := Cluster{client: fake.NewClientset()}

	err := cluster.ReplaceSecret(t.Context(), declared(nil))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "putting secret "+theNamespace+"/"+theSecretName)
}
