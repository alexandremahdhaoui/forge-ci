package managercontroller_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/managercontrollermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	theNamespace  = "flux-system"
	theSecretName = "flux-deploy-key"
	theSecretID   = theNamespace + "/" + theSecretName

	theKeyMarker = "ZZZTOPSECRETZZZclientprivatekey"

	theDeclaredMint = "Ask the person who holds the credential to write it into the cluster"

	found    = true
	notFound = false
)

var thePastedPrivateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtz\n" +
	theKeyMarker + "\n" +
	strings.Repeat("cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n", 24) +
	"-----END OPENSSH PRIVATE KEY-----\n"

func kubernetesRealizer(t *testing.T) (managercontroller.KubernetesRealizer, *managercontrollermock.MockKubernetes) {
	t.Helper()

	cluster := managercontrollermock.NewMockKubernetes(t)
	cluster.EXPECT().APIServer().Return(theAPIServer)

	return realizerHolding(t, cluster, managercontrollermock.NewMockHelm(t)), cluster
}

func realizerHolding(
	t *testing.T, cluster managercontroller.Kubernetes, helm managercontroller.Helm,
) managercontroller.KubernetesRealizer {
	t.Helper()

	r, err := managercontroller.NewKubernetesRealizer(t.Context(), cluster, helm)
	require.NoError(t, err)

	return r
}

func realizerThatMustNotReachAPort(t *testing.T) managercontroller.KubernetesRealizer {
	t.Helper()

	return realizerHolding(t,
		managercontrollermock.NewMockKubernetes(t), managercontrollermock.NewMockHelm(t))
}

func declaredSecret(keys any) citypes.Resource {
	return citypes.Resource{
		Kind: managercontroller.KindSecret,
		Name: theSecretName,
		Spec: map[string]any{
			"namespace": theNamespace,
			"name":      theSecretName,
			"keys":      keys,
			"apiServer": theAPIServer,
		},
	}
}

func oneKey() citypes.Resource {
	return declaredSecret([]any{"identity"})
}

func twoKeys() citypes.Resource {
	return declaredSecret([]any{"identity", "known_hosts"})
}

func liveSecret(data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: theNamespace,
			Name:      theSecretName,
			Labels:    map[string]string{"owner": "a human"},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
}

func TestBuildingTheKubernetesRealizerWithNoClusterClientIsRefusedByName(t *testing.T) {
	t.Parallel()

	_, err := managercontroller.NewKubernetesRealizer(
		t.Context(), nil, managercontrollermock.NewMockHelm(t))
	require.Error(t, err)
	assert.Equal(t, "building the kubernetes realizer: it was handed no cluster client, "+
		"and it reaches the cluster through one of each", err.Error())
}

func TestBuildingTheKubernetesRealizerWithNoHelmClientIsRefusedByName(t *testing.T) {
	t.Parallel()

	_, err := managercontroller.NewKubernetesRealizer(
		t.Context(), managercontrollermock.NewMockKubernetes(t), nil)
	require.Error(t, err)
	assert.Equal(t, "building the kubernetes realizer: it was handed no helm client, "+
		"and it reaches the cluster through one of each", err.Error())
}

func TestBuildingTheKubernetesRealizerWithNeitherClientNamesBothOfThem(t *testing.T) {
	t.Parallel()

	_, err := managercontroller.NewKubernetesRealizer(t.Context(), nil, nil)
	require.Error(t, err)
	assert.Equal(t, "building the kubernetes realizer: it was handed no cluster client "+
		"and no helm client, and it reaches the cluster through one of each", err.Error())
}

func TestTheKubernetesRealizerNamesItselfKubernetes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "kubernetes", realizerThatMustNotReachAPort(t).Kind())
}

func TestTheKubernetesRealizerRefusesAKindItDoesNotKnowByName(t *testing.T) {
	t.Parallel()

	r := realizerThatMustNotReachAPort(t)

	_, err := r.Realize(citypes.Resource{Kind: "config-map", Name: "settings"}, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot realize kind "config-map"`)
	assert.Contains(t, err.Error(), managercontroller.KindSecret)
}

func TestTheKubernetesRealizerRefusesASecretThatNamesNoNamespace(t *testing.T) {
	t.Parallel()

	r := realizerThatMustNotReachAPort(t)
	res := citypes.Resource{
		Kind: managercontroller.KindSecret,
		Name: theSecretName,
		Spec: map[string]any{"name": theSecretName},
	}

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.namespace and spec.name are required")
}

func TestTheKubernetesRealizerRefusesASecretThatNamesNoName(t *testing.T) {
	t.Parallel()

	r := realizerThatMustNotReachAPort(t)
	res := citypes.Resource{
		Kind: managercontroller.KindSecret,
		Name: theSecretName,
		Spec: map[string]any{"namespace": theNamespace, "name": ""},
	}

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.namespace and spec.name are required")
}

func TestTheKubernetesRealizerRefusesANamespaceThatIsNotAString(t *testing.T) {
	t.Parallel()

	r := realizerThatMustNotReachAPort(t)
	res := citypes.Resource{
		Kind: managercontroller.KindSecret,
		Name: theSecretName,
		Spec: map[string]any{"namespace": 7, "name": theSecretName},
	}

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading spec.namespace: a string is required, the spec holds a int")
}

func TestTheKubernetesRealizerRefusesANameThatIsNotAString(t *testing.T) {
	t.Parallel()

	r := realizerThatMustNotReachAPort(t)
	res := citypes.Resource{
		Kind: managercontroller.KindSecret,
		Name: theSecretName,
		Spec: map[string]any{"namespace": theNamespace, "name": []any{theSecretName}},
	}

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading spec.name: a string is required, the spec holds a []interface {}")
}

func TestTheKubernetesRealizerRefusesASecretDeclaringNoKeys(t *testing.T) {
	t.Parallel()

	r := realizerThatMustNotReachAPort(t)

	_, err := r.Realize(declaredSecret(nil), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the keys of secret "+theSecretID+
		": spec.keys is required, and it names every key the live secret must hold")
}

func TestTheKubernetesRealizerRefusesAnEmptyKeyList(t *testing.T) {
	t.Parallel()

	r := realizerThatMustNotReachAPort(t)

	_, err := r.Realize(declaredSecret([]any{}), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.keys is required, and it names every key the live secret must hold")
}

func TestTheKubernetesRealizerRefusesAKeyListThatIsNotAList(t *testing.T) {
	t.Parallel()

	r := realizerThatMustNotReachAPort(t)

	_, err := r.Realize(declaredSecret("identity"), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the keys of secret "+theSecretID)
	assert.Contains(t, err.Error(),
		"reading spec.keys: a list of strings is required, the spec holds a string")
}

func TestTheKubernetesRealizerRefusesAKeyThatIsNotAString(t *testing.T) {
	t.Parallel()

	r := realizerThatMustNotReachAPort(t)

	_, err := r.Realize(declaredSecret([]any{"identity", 7}), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"reading spec.keys[1]: a string is required, the spec holds a int")
}

func TestTheKubernetesRealizerRefusesAKeyWithNoName(t *testing.T) {
	t.Parallel()

	r := realizerThatMustNotReachAPort(t)

	_, err := r.Realize(declaredSecret([]any{""}), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.keys holds a key with no name")
}

func TestASecretIsRefusedWhenTheLiveClusterIsNotTheOneTheDeclarationNames(t *testing.T) {
	t.Parallel()

	cluster := managercontrollermock.NewMockKubernetes(t)
	cluster.EXPECT().APIServer().Return(anotherAPIServer)

	r := realizerHolding(t, cluster, managercontrollermock.NewMockHelm(t))

	_, err := r.Realize(oneKey(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the api server holding secret "+theSecretID)
	assert.Contains(t, err.Error(), theAPIServer)
	assert.Contains(t, err.Error(), anotherAPIServer)
	cluster.AssertNotCalled(t, "Secret", mock.Anything, mock.Anything, mock.Anything)
}

func TestASecretDeclarationCarryingNoAPIServerIsRefusedByName(t *testing.T) {
	t.Parallel()

	r := realizerThatMustNotReachAPort(t)

	res := oneKey()
	delete(res.Spec, "apiServer")

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading secret "+theSecretID+": spec.apiServer is required")
}

func TestASecretAPIServerKeyThatIsNotAStringIsRefusedByName(t *testing.T) {
	t.Parallel()

	r := realizerThatMustNotReachAPort(t)

	res := oneKey()
	res.Spec["apiServer"] = 7

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading spec.apiServer: a string is required, the spec holds a int")
}

func TestAClusterClientThatNamesNoAPIServerRefusesASecretByName(t *testing.T) {
	t.Parallel()

	cluster := managercontrollermock.NewMockKubernetes(t)
	cluster.EXPECT().APIServer().Return("")

	r := realizerHolding(t, cluster, managercontrollermock.NewMockHelm(t))

	_, err := r.Realize(oneKey(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the api server holding secret "+theSecretID+
		": the cluster client names no api server")
}

func TestTheKubernetesRealizerReportsTheSecretItCouldNotRead(t *testing.T) {
	t.Parallel()

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).
		Return(nil, notFound, errors.New("connection refused"))

	_, err := r.Realize(oneKey(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading secret "+theSecretID)
	assert.Contains(t, err.Error(), "connection refused")
}

func TestTheKubernetesRealizerRefusesAClusterThatFoundASecretAndHandedBackNothing(t *testing.T) {
	t.Parallel()

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(nil, found, nil)

	_, err := r.Realize(oneKey(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"reading secret "+theSecretID+": the cluster answered that it holds one and handed back nothing")
}

func TestASecretHoldingEveryDeclaredKeyIsKept(t *testing.T) {
	t.Parallel()

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(
		liveSecret(map[string][]byte{
			"identity":    []byte("a key"),
			"known_hosts": []byte("a host"),
		}), found, nil)

	action, err := r.Realize(twoKeys(), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept secret "+theSecretID+", holding identity, known_hosts", action.Text)
	assert.False(t, action.Changed)
}

func TestASecretHoldingMoreKeysThanTheDeclarationNamesIsStillKept(t *testing.T) {
	t.Parallel()

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(
		liveSecret(map[string][]byte{
			"identity":    []byte("a key"),
			"known_hosts": []byte("a host"),
			"a-third-one": []byte("something a person added"),
		}), found, nil)

	action, err := r.Realize(twoKeys(), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept secret "+theSecretID+", holding identity, known_hosts", action.Text)
	assert.False(t, action.Changed)
}

func TestTheActionLineOfAKeptSecretNamesEveryKeyItConfirmed(t *testing.T) {
	t.Parallel()

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(
		liveSecret(map[string][]byte{"identity": []byte("a key")}), found, nil)

	action, err := r.Realize(oneKey(), plain)
	require.NoError(t, err)
	assert.Equal(t, "kept secret "+theSecretID+", holding identity", action.Text)
}

func TestASecretHoldingADeclaredKeyWithNothingInItIsRefusedByThatKey(t *testing.T) {
	t.Parallel()

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(
		liveSecret(map[string][]byte{
			"identity": []byte("a key"), "known_hosts": {},
		}), found, nil)

	_, err := r.Realize(twoKeys(), plain)
	require.Error(t, err)
	assert.Equal(t, "reading secret "+theSecretID+
		": it holds known_hosts with nothing in it, and this declaration needs a value "+
		"under identity, known_hosts. A person writes every key of this secret by hand, "+
		"and nothing in this toolchain writes one", err.Error())
}

func TestASecretHoldingEveryDeclaredKeyEmptyNamesEveryOneOfThem(t *testing.T) {
	t.Parallel()

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(
		liveSecret(map[string][]byte{"identity": {}, "known_hosts": {}}), found, nil)

	_, err := r.Realize(twoKeys(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "it holds identity and known_hosts with nothing in it")
}

func TestASecretMissingADeclaredKeyIsRefusedByThatKey(t *testing.T) {
	t.Parallel()

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(
		liveSecret(map[string][]byte{"identity": []byte("a key")}), found, nil)

	_, err := r.Realize(twoKeys(), plain)
	require.Error(t, err)
	assert.Equal(t, "reading secret "+theSecretID+
		": it holds no known_hosts, and this declaration needs identity, known_hosts. "+
		"A person writes every key of this secret by hand, "+
		"and nothing in this toolchain writes one", err.Error())
}

func TestASecretMissingEveryDeclaredKeyNamesEveryOneOfThem(t *testing.T) {
	t.Parallel()

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).
		Return(liveSecret(nil), found, nil)

	_, err := r.Realize(twoKeys(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "it holds no identity and no known_hosts")
}

func TestASecretTheClusterDoesNotHoldNamesItsKeysAndNoWayOfMintingItWhenTheDeclarationWroteNone(t *testing.T) {
	t.Parallel()

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(nil, notFound, nil)

	_, err := r.Realize(twoKeys(), plain)
	require.Error(t, err)
	assert.Equal(t, "reading secret "+theSecretID+
		": the cluster holds no secret of that name, nothing in this toolchain writes one, "+
		"and it must hold identity, known_hosts", err.Error())
}

func TestASecretTheClusterDoesNotHoldNamesTheEntryHoldingTheMintingStepsAndNeverEchoesThem(t *testing.T) {
	t.Parallel()

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(nil, notFound, nil)

	res := twoKeys()
	res.Spec["mint"] = theDeclaredMint

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.Equal(t, "reading secret "+theSecretID+
		": the cluster holds no secret of that name, nothing in this toolchain writes one, "+
		"and it must hold identity, known_hosts. The pipeline file says how a person mints it, "+
		"under spec.mint of the secret entry naming "+theSecretID, err.Error())
	assert.NotContains(t, err.Error(), theDeclaredMint)
}

func TestTheManagerNamesNoKeyTypeAndNoProductOfItsOwnWhenASecretIsAbsent(t *testing.T) {
	t.Parallel()

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(nil, notFound, nil)

	_, err := r.Realize(twoKeys(), plain)
	require.Error(t, err)

	for _, word := range []string{"ed25519", "deploy key", "ssh", "three steps"} {
		assert.NotContains(t, err.Error(), word)
	}
}

func TestAMintThatIsNotAStringIsRefusedByName(t *testing.T) {
	t.Parallel()

	r := realizerThatMustNotReachAPort(t)

	res := twoKeys()
	res.Spec["mint"] = 7

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading spec.mint: a string is required, the spec holds a int")
}

func TestNoRefusalOverASecretEverCarriesAValueTheClusterHolds(t *testing.T) {
	t.Parallel()

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(
		liveSecret(map[string][]byte{"identity": []byte(thePastedPrivateKey)}), found, nil)

	_, err := r.Realize(twoKeys(), plain)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), thePastedPrivateKey)
	assert.NotContains(t, err.Error(), theKeyMarker)
	assert.NotContains(t, err.Error(), "BEGIN OPENSSH PRIVATE KEY")
	assert.Contains(t, err.Error(), "it holds no known_hosts")
}

func TestADryRunOverASecretAnswersExactlyWhatARealRunAnswers(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		live *corev1.Secret
		held bool
	}{
		{name: "a secret holding every declared key", live: liveSecret(map[string][]byte{
			"identity": []byte("a key"), "known_hosts": []byte("a host"),
		}), held: found},
		{name: "a secret missing a declared key", live: liveSecret(nil), held: found},
		{name: "a secret the cluster does not hold", live: nil, held: notFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r, cluster := kubernetesRealizer(t)
			cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).
				Return(tc.live, tc.held, nil).Twice()

			applied, appliedErr := r.Realize(twoKeys(), plain)
			dry, dryErr := r.Realize(twoKeys(), managercontroller.Options{DryRun: true})

			assert.Equal(t, applied, dry)

			if appliedErr == nil {
				assert.NoError(t, dryErr)

				return
			}

			require.Error(t, dryErr)
			assert.Equal(t, appliedErr.Error(), dryErr.Error())
		})
	}
}

func TestADryRunReadsTheClusterBecauseAPlanIsOnlyWorthReadingIfItCameFromTheComparisonARealRunMakes(t *testing.T) {
	t.Parallel()

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(
		liveSecret(map[string][]byte{"identity": []byte("a key")}), found, nil).Once()

	action, err := r.Realize(oneKey(), managercontroller.Options{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, "kept secret "+theSecretID+", holding identity", action.Text)
	cluster.AssertNumberOfCalls(t, "Secret", 1)
}

func TestForceOverASecretAnswersExactlyWhatAPlainRunAnswers(t *testing.T) {
	t.Parallel()

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(
		liveSecret(map[string][]byte{
			"identity": []byte("a key"), "known_hosts": []byte("a host"),
		}), found, nil).Twice()

	plainRun, err := r.Realize(twoKeys(), plain)
	require.NoError(t, err)

	forced, err := r.Realize(twoKeys(), managercontroller.Options{Force: true})
	require.NoError(t, err)
	assert.Equal(t, plainRun, forced)
}
