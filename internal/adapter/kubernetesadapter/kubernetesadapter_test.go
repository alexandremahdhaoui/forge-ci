package kubernetesadapter

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	theNamespace  = "flux-system"
	theSecretName = "flux-deploy-key"

	theAPIServer = "https://127.0.0.1:1"
)

func declared(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: theNamespace, Name: theSecretName},
		Type:       corev1.SecretTypeOpaque,
		Data:       data,
	}
}

func writeKubeconfig(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	return path
}

func TestAClusterClientHandedNoKubeconfigFileIsRefusedByNameInsteadOfFallingBackInCluster(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"", "   "} {
		_, err := New(path)

		require.Error(t, err)
		assert.Equal(t,
			"building the client of the cluster: it was handed no kubeconfig file, "+
				"and the cluster credential is declared. nothing ambient names the cluster",
			err.Error())
	}
}

func TestTheClusterClientIsBuiltFromTheKubeconfigTheCallerNamesAndItsHostIsTheAPIServer(t *testing.T) {
	t.Parallel()

	path := writeKubeconfig(t, `apiVersion: v1
kind: Config
clusters:
  - name: here
    cluster:
      server: `+theAPIServer+`
contexts:
  - name: here
    context:
      cluster: here
current-context: here
`)

	cluster, err := New(path)
	require.NoError(t, err)
	assert.Equal(t, theAPIServer, cluster.APIServer())
}

func TestAKubeconfigNamingNoClusterRefusesByNameInsteadOfBuildingAClientThatReachesNothing(t *testing.T) {
	t.Parallel()

	_, err := New(writeKubeconfig(t, "apiVersion: v1\nkind: Config\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the client configuration of the cluster")
}

func TestAPathNamingNoKubeconfigFileRefusesInsteadOfFallingBackToAnAmbientOne(t *testing.T) {
	t.Parallel()

	_, err := New(filepath.Join(t.TempDir(), "nothing-is-here"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the client configuration of the cluster")
	assert.Contains(t, err.Error(), "nothing-is-here")
}

func TestAKubeconfigNamingAHostTheClientCannotUseRefusesSayingItWasBuildingTheClientAndNamingTheHost(t *testing.T) {
	t.Parallel()

	path := writeKubeconfig(t, `apiVersion: v1
kind: Config
clusters:
  - name: here
    cluster:
      server: "://not a url"
contexts:
  - name: here
    context:
      cluster: here
current-context: here
`)

	_, err := New(path)
	require.Error(t, err)
	assert.Equal(t,
		`building the client of the cluster: host must be a URL or a host:port pair: "://not a url"`,
		err.Error())
}

func TestReadingASecretTheClusterRefusesIsReportedAsAnErrorNamingTheSecret(t *testing.T) {
	t.Parallel()

	clientset := fake.NewClientset()
	clientset.PrependReactor("get", "secrets",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("connection refused")
		})

	_, found, err := Cluster{client: clientset}.Secret(t.Context(), theNamespace, theSecretName)
	require.Error(t, err)
	assert.False(t, found)
	assert.Contains(t, err.Error(), "getting secret "+theNamespace+"/"+theSecretName)
	assert.Contains(t, err.Error(), "connection refused")
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
