package managercontroller_test

import (
	"context"
	"errors"
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

	identityVariable  = "FORGE_CI_TEST_FLUX_IDENTITY"
	knownHostVariable = "FORGE_CI_TEST_FLUX_KNOWN_HOSTS"

	thePastedPrivateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
		"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtz\n" +
		"-----END OPENSSH PRIVATE KEY-----\n"

	found    = true
	notFound = false
)

func kubernetesRealizer(t *testing.T) (managercontroller.KubernetesRealizer, *managercontrollermock.MockKubernetes) {
	t.Helper()

	cluster := managercontrollermock.NewMockKubernetes(t)

	return managercontroller.NewKubernetesRealizer(t.Context(), cluster), cluster
}

func declaredSecret(data map[string]any) citypes.Resource {
	return citypes.Resource{
		Kind: managercontroller.KindSecret,
		Name: theSecretName,
		Spec: map[string]any{
			"namespace": theNamespace,
			"name":      theSecretName,
			"data":      data,
		},
	}
}

func oneKey(t *testing.T, value string) citypes.Resource {
	t.Helper()
	t.Setenv(identityVariable, value)

	return declaredSecret(map[string]any{"identity": identityVariable})
}

func twoKeys(t *testing.T, identity, knownHosts string) citypes.Resource {
	t.Helper()
	t.Setenv(identityVariable, identity)
	t.Setenv(knownHostVariable, knownHosts)

	return declaredSecret(map[string]any{
		"identity":    identityVariable,
		"known_hosts": knownHostVariable,
	})
}

func liveSecret(hash string, data map[string][]byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   theNamespace,
			Name:        theSecretName,
			Labels:      map[string]string{"owner": "a human"},
			Annotations: map[string]string{managercontroller.SecretHashAnnotation: hash},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
}

func hashWrittenBy(t *testing.T, res citypes.Resource) string {
	t.Helper()

	r, cluster := kubernetesRealizer(t)

	var written *corev1.Secret

	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(nil, notFound, nil)
	cluster.EXPECT().CreateSecret(mock.Anything, mock.Anything).
		Run(func(_ context.Context, secret *corev1.Secret) { written = secret }).Return(nil)

	_, err := r.Realize(res, plain)
	require.NoError(t, err)

	return written.Annotations[managercontroller.SecretHashAnnotation]
}

func TestTheKubernetesRealizerNamesItselfKubernetes(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "kubernetes", managercontroller.NewKubernetesRealizer(t.Context(), nil).Kind())
}

func TestTheKubernetesRealizerRefusesAKindItDoesNotKnowByName(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil)

	_, err := r.Realize(citypes.Resource{Kind: "config-map", Name: "settings"}, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cannot realize kind "config-map"`)
	assert.Contains(t, err.Error(), managercontroller.KindSecret)
}

func TestTheKubernetesRealizerRefusesASecretThatNamesNoNamespace(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil)
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

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil)
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

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil)
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

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil)
	res := citypes.Resource{
		Kind: managercontroller.KindSecret,
		Name: theSecretName,
		Spec: map[string]any{"namespace": theNamespace, "name": []any{theSecretName}},
	}

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading spec.name: a string is required, the spec holds a []interface {}")
}

func TestTheKubernetesRealizerRefusesADataBlockThatIsNotAMap(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil)

	_, err := r.Realize(declaredSecret(nil), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the data of secret "+theSecretID)
	assert.Contains(t, err.Error(), "spec.data is required")
}

func TestTheKubernetesRealizerRefusesADataBlockThatIsNotAMapOfStrings(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil)
	res := citypes.Resource{
		Kind: managercontroller.KindSecret,
		Name: theSecretName,
		Spec: map[string]any{
			"namespace": theNamespace,
			"name":      theSecretName,
			"data":      identityVariable,
		},
	}

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"reading spec.data: a map of strings is required, the spec holds a string")
}

func TestTheKubernetesRealizerRefusesADataValueThatIsNotAString(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil)

	_, err := r.Realize(declaredSecret(map[string]any{"identity": 7}), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the data of secret "+theSecretID)
	assert.Contains(t, err.Error(), "reading spec.data.identity: a string is required, the spec holds a int")
}

func TestTheKubernetesRealizerRefusesADataBlockHoldingNoKey(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil)

	_, err := r.Realize(declaredSecret(map[string]any{}), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.data is required, and it names one environment variable per key")
}

func TestTheKubernetesRealizerRefusesAKeyWithNoName(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil)

	_, err := r.Realize(declaredSecret(map[string]any{"": identityVariable}), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.data holds a key with no name")
}

func TestTheKubernetesRealizerRefusesAKeyThatNamesNoEnvironmentVariable(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil)

	_, err := r.Realize(declaredSecret(map[string]any{"identity": ""}), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `key "identity" names no environment variable`)
}

func TestTheKubernetesRealizerRefusesAKeyWhoseVariableIsEmpty(t *testing.T) {
	r := managercontroller.NewKubernetesRealizer(t.Context(), nil)

	_, err := r.Realize(oneKey(t, ""), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the data of secret "+theSecretID+
		`: key "identity" must hold the name of an environment variable, `+
		"and no variable of that name is set")
	assert.NotContains(t, err.Error(), identityVariable)
}

func TestSayingTheKeyMustHoldAVariableNameSeparatesAPastedSecretFromAnUnsetVariable(t *testing.T) {
	r := managercontroller.NewKubernetesRealizer(t.Context(), nil)

	_, pasted := r.Realize(declaredSecret(map[string]any{"identity": thePastedPrivateKey}), plain)
	require.Error(t, pasted)

	_, unset := r.Realize(oneKey(t, ""), plain)
	require.Error(t, unset)

	assert.Equal(t, pasted.Error(), unset.Error(),
		"one message serves both, so it must never send an operator hunting for a variable")
	assert.Contains(t, pasted.Error(), "must hold the name of an environment variable",
		"the message says the key is read as a name, which is how a pasted value is recognised")
}

func TestTheKubernetesRealizerNeverEchoesAPastedSecretBackOutOfItsRefusal(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil)

	_, err := r.Realize(declaredSecret(map[string]any{"identity": thePastedPrivateKey}), plain)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), thePastedPrivateKey)
	assert.NotContains(t, err.Error(), "BEGIN OPENSSH PRIVATE KEY")
	assert.NotContains(t, err.Error(), "b3BlbnNzaC1rZXktdjEAAAAABG5vbmU")
	assert.Contains(t, err.Error(), "reading the data of secret "+theSecretID+
		`: key "identity" must hold the name of an environment variable, `+
		"and no variable of that name is set")
}

func TestTheKubernetesRealizerRefusesToWorkWithNoClusterBehindIt(t *testing.T) {
	r := managercontroller.NewKubernetesRealizer(t.Context(), nil)

	_, err := r.Realize(oneKey(t, "a key"), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"reading secret "+theSecretID+": this manager carries no cluster client yet")
}

func TestTheKubernetesRealizerReportsTheSecretItCouldNotRead(t *testing.T) {
	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).
		Return(nil, notFound, errors.New("connection refused"))

	_, err := r.Realize(oneKey(t, "a key"), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading secret "+theSecretID)
	assert.Contains(t, err.Error(), "connection refused")
}

func TestTheKubernetesRealizerRefusesAClusterThatFoundASecretAndHandedBackNothing(t *testing.T) {
	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(nil, found, nil)

	_, err := r.Realize(oneKey(t, "a key"), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		"reading secret "+theSecretID+": the cluster answered that it holds one and handed back nothing")
}

func TestTheKubernetesRealizerCreatesASecretTheClusterDoesNotHold(t *testing.T) {
	r, cluster := kubernetesRealizer(t)

	var written *corev1.Secret

	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(nil, notFound, nil)
	cluster.EXPECT().CreateSecret(mock.Anything, mock.Anything).
		Run(func(_ context.Context, secret *corev1.Secret) { written = secret }).Return(nil)

	action, err := r.Realize(twoKeys(t, "a key", "a host"), plain)
	require.NoError(t, err)
	assert.Equal(t,
		"created secret "+theSecretID+" holding identity, known_hosts", action.Text)
	assert.True(t, action.Changed)
	assert.Equal(t, theNamespace, written.Namespace)
	assert.Equal(t, theSecretName, written.Name)
	assert.Equal(t, corev1.SecretTypeOpaque, written.Type)
	assert.Equal(t,
		map[string][]byte{"identity": []byte("a key"), "known_hosts": []byte("a host")}, written.Data)
	assert.NotEmpty(t, written.Annotations[managercontroller.SecretHashAnnotation])
	cluster.AssertNotCalled(t, "ReplaceSecret", mock.Anything, mock.Anything)
}

func TestNoActionLineOfTheKubernetesRealizerEverCarriesADeclaredValue(t *testing.T) {
	r, cluster := kubernetesRealizer(t)

	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(nil, notFound, nil).Once()
	cluster.EXPECT().CreateSecret(mock.Anything, mock.Anything).Return(nil).Once()

	created, err := r.Realize(oneKey(t, thePastedPrivateKey), plain)
	require.NoError(t, err)

	live := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Namespace:   theNamespace,
		Name:        theSecretName,
		Annotations: map[string]string{managercontroller.SecretHashAnnotation: "something else"},
	}}

	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(live, found, nil).Twice()
	cluster.EXPECT().ReplaceSecret(mock.Anything, mock.Anything).Return(nil).Once()

	replaced, err := r.Realize(oneKey(t, thePastedPrivateKey), plain)
	require.NoError(t, err)

	dry, err := r.Realize(oneKey(t, thePastedPrivateKey), managercontroller.Options{DryRun: true})
	require.NoError(t, err)

	for _, text := range []string{created.Text, replaced.Text, dry.Text} {
		assert.NotContains(t, text, thePastedPrivateKey)
		assert.NotContains(t, text, "BEGIN OPENSSH PRIVATE KEY")
		assert.NotContains(t, text, identityVariable)
		assert.Contains(t, text, "identity")
	}
}

func TestTheHashACreateWritesIsTwelveCharactersLong(t *testing.T) {
	assert.Len(t, hashWrittenBy(t, oneKey(t, "a key")), 12)
}

func TestTheKubernetesRealizerReportsTheSecretItCouldNotCreate(t *testing.T) {
	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(nil, notFound, nil)
	cluster.EXPECT().CreateSecret(mock.Anything, mock.Anything).Return(errors.New("forbidden"))

	_, err := r.Realize(oneKey(t, "a key"), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "creating secret "+theSecretID)
	assert.Contains(t, err.Error(), "forbidden")
}

func TestTheKubernetesRealizerKeepsASecretWhoseAnnotationMatchesTheDeclaredData(t *testing.T) {
	res := oneKey(t, "a key")
	hash := hashWrittenBy(t, res)

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).
		Return(liveSecret(hash, map[string][]byte{"identity": []byte("a key")}), found, nil)

	action, err := r.Realize(res, plain)
	require.NoError(t, err)
	assert.Equal(t, "kept secret "+theSecretID, action.Text)
	assert.False(t, action.Changed)
	cluster.AssertNotCalled(t, "CreateSecret", mock.Anything, mock.Anything)
	cluster.AssertNotCalled(t, "ReplaceSecret", mock.Anything, mock.Anything)
}

func TestASecondRunReadsBackTheAnnotationTheFirstOneWroteAndKeepsTheSecret(t *testing.T) {
	res := twoKeys(t, "a key", "a host")

	first, cluster := kubernetesRealizer(t)

	var written *corev1.Secret

	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(nil, notFound, nil)
	cluster.EXPECT().CreateSecret(mock.Anything, mock.Anything).
		Run(func(_ context.Context, secret *corev1.Secret) { written = secret }).Return(nil)

	created, err := first.Realize(res, plain)
	require.NoError(t, err)
	assert.True(t, created.Changed)

	second, again := kubernetesRealizer(t)
	again.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(written, found, nil)

	kept, err := second.Realize(res, plain)
	require.NoError(t, err)
	assert.Equal(t, "kept secret "+theSecretID, kept.Text)
	assert.False(t, kept.Changed)
}

func TestTheKubernetesRealizerReplacesTheDataOfASecretWhoseAnnotationNamesSomethingElse(t *testing.T) {
	r, cluster := kubernetesRealizer(t)

	var written *corev1.Secret

	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).
		Return(liveSecret("aaaaaaaaaaaa", map[string][]byte{"identity": []byte("an old key")}), found, nil)
	cluster.EXPECT().ReplaceSecret(mock.Anything, mock.Anything).
		Run(func(_ context.Context, secret *corev1.Secret) { written = secret }).Return(nil)

	action, err := r.Realize(oneKey(t, "a new key"), plain)
	require.NoError(t, err)
	assert.Equal(t,
		"replaced the data of secret "+theSecretID+" with identity", action.Text)
	assert.True(t, action.Changed)
	assert.Equal(t, map[string][]byte{"identity": []byte("a new key")}, written.Data)
	assert.NotEqual(t, "aaaaaaaaaaaa", written.Annotations[managercontroller.SecretHashAnnotation])
}

func TestAReplaceDropsAKeyTheDeclarationNoLongerNames(t *testing.T) {
	r, cluster := kubernetesRealizer(t)

	var written *corev1.Secret

	live := liveSecret("aaaaaaaaaaaa", map[string][]byte{
		"identity":    []byte("a key"),
		"known_hosts": []byte("a host"),
	})
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(live, found, nil)
	cluster.EXPECT().ReplaceSecret(mock.Anything, mock.Anything).
		Run(func(_ context.Context, secret *corev1.Secret) { written = secret }).Return(nil)

	action, err := r.Realize(oneKey(t, "a key"), plain)
	require.NoError(t, err)
	assert.True(t, action.Changed)
	assert.Equal(t, map[string][]byte{"identity": []byte("a key")}, written.Data)
	assert.NotContains(t, written.Data, "known_hosts")
}

func TestDroppingAKeyFromTheDeclarationAnswersADifferentHash(t *testing.T) {
	both := hashWrittenBy(t, twoKeys(t, "a key", "x"))
	one := hashWrittenBy(t, oneKey(t, "a key"))

	assert.NotEqual(t, both, one)
}

func TestTwoKeysWhoseValuesRunTogetherAnswerADifferentHashFromTheirHalves(t *testing.T) {
	joined := hashWrittenBy(t, twoKeys(t, "ab", "c"))
	split := hashWrittenBy(t, twoKeys(t, "a", "bc"))

	assert.NotEqual(t, joined, split)
}

func TestAReplaceKeepsEveryOtherFieldOfTheLiveSecret(t *testing.T) {
	r, cluster := kubernetesRealizer(t)

	var written *corev1.Secret

	live := liveSecret("aaaaaaaaaaaa", map[string][]byte{"identity": []byte("an old key")})
	live.Annotations["written-by"] = "a human"
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(live, found, nil)
	cluster.EXPECT().ReplaceSecret(mock.Anything, mock.Anything).
		Run(func(_ context.Context, secret *corev1.Secret) { written = secret }).Return(nil)

	_, err := r.Realize(oneKey(t, "a new key"), plain)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"owner": "a human"}, written.Labels)
	assert.Equal(t, "a human", written.Annotations["written-by"])
}

func TestAReplaceLeavesTheSecretTheClusterHandedBackUntouched(t *testing.T) {
	r, cluster := kubernetesRealizer(t)

	live := liveSecret("aaaaaaaaaaaa", map[string][]byte{"identity": []byte("an old key")})
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(live, found, nil)
	cluster.EXPECT().ReplaceSecret(mock.Anything, mock.Anything).Return(nil)

	_, err := r.Realize(oneKey(t, "a new key"), plain)
	require.NoError(t, err)
	assert.Equal(t, map[string][]byte{"identity": []byte("an old key")}, live.Data)
	assert.Equal(t, "aaaaaaaaaaaa", live.Annotations[managercontroller.SecretHashAnnotation])
}

func TestAReplaceWritesTheAnnotationOnASecretThatCarriedNone(t *testing.T) {
	r, cluster := kubernetesRealizer(t)

	var written *corev1.Secret

	live := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: theNamespace, Name: theSecretName},
		Data:       map[string][]byte{"identity": []byte("an old key")},
	}
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(live, found, nil)
	cluster.EXPECT().ReplaceSecret(mock.Anything, mock.Anything).
		Run(func(_ context.Context, secret *corev1.Secret) { written = secret }).Return(nil)

	action, err := r.Realize(oneKey(t, "a key"), plain)
	require.NoError(t, err)
	assert.True(t, action.Changed)
	assert.Len(t, written.Annotations[managercontroller.SecretHashAnnotation], 12)
}

func TestTheKubernetesRealizerReportsTheSecretItCouldNotReplace(t *testing.T) {
	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).
		Return(liveSecret("aaaaaaaaaaaa", nil), found, nil)
	cluster.EXPECT().ReplaceSecret(mock.Anything, mock.Anything).Return(errors.New("conflict"))

	_, err := r.Realize(oneKey(t, "a key"), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "replacing the data of secret "+theSecretID)
	assert.Contains(t, err.Error(), "conflict")
}

func TestADryRunOverASecretTheClusterDoesNotHoldWritesNothing(t *testing.T) {
	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).Return(nil, notFound, nil)

	action, err := r.Realize(twoKeys(t, "a key", "a host"), managercontroller.Options{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t,
		"would create secret "+theSecretID+" holding identity, known_hosts", action.Text)
	assert.False(t, action.Changed)
	cluster.AssertNotCalled(t, "CreateSecret", mock.Anything, mock.Anything)
}

func TestADryRunOverASecretWhoseAnnotationNamesSomethingElseWritesNothing(t *testing.T) {
	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).
		Return(liveSecret("aaaaaaaaaaaa", nil), found, nil)

	action, err := r.Realize(oneKey(t, "a key"), managercontroller.Options{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t,
		"would replace the data of secret "+theSecretID+" with identity", action.Text)
	assert.False(t, action.Changed)
	cluster.AssertNotCalled(t, "ReplaceSecret", mock.Anything, mock.Anything)
}

func TestADryRunOverASecretWithNothingToChangeAnswersKept(t *testing.T) {
	res := oneKey(t, "a key")
	hash := hashWrittenBy(t, res)

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).
		Return(liveSecret(hash, nil), found, nil)

	action, err := r.Realize(res, managercontroller.Options{DryRun: true})
	require.NoError(t, err)
	assert.Equal(t, "kept secret "+theSecretID, action.Text)
	assert.False(t, action.Changed)
}

func TestForceReplacesTheDataOfASecretWhoseAnnotationAlreadyMatches(t *testing.T) {
	res := oneKey(t, "a key")
	hash := hashWrittenBy(t, res)

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).
		Return(liveSecret(hash, nil), found, nil)
	cluster.EXPECT().ReplaceSecret(mock.Anything, mock.Anything).Return(nil)

	action, err := r.Realize(res, managercontroller.Options{Force: true})
	require.NoError(t, err)
	assert.Equal(t,
		"replaced the data of secret "+theSecretID+" with identity", action.Text)
	assert.True(t, action.Changed)
}

func TestForceInADryRunStillWritesNothing(t *testing.T) {
	res := oneKey(t, "a key")
	hash := hashWrittenBy(t, res)

	r, cluster := kubernetesRealizer(t)
	cluster.EXPECT().Secret(mock.Anything, theNamespace, theSecretName).
		Return(liveSecret(hash, nil), found, nil)

	action, err := r.Realize(res, managercontroller.Options{DryRun: true, Force: true})
	require.NoError(t, err)
	assert.Equal(t,
		"would replace the data of secret "+theSecretID+" with identity", action.Text)
	assert.False(t, action.Changed)
	cluster.AssertNotCalled(t, "ReplaceSecret", mock.Anything, mock.Anything)
}
