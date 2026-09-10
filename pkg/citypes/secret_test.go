package citypes_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const theKeyMarker = "ZZZTOPSECRETZZZclientprivatekey"

type theSlotHolder struct {
	Name  string
	Value citypes.Secret
}

func TestTheFixtureMatchesARealKeyInSizeAndClearsThePathCeiling(t *testing.T) {
	t.Parallel()

	assert.Greater(t, len(thePastedPrivateKey), 1400)
	assert.Greater(t, len(thePastedPrivateKey), 255)
}

func TestEveryFormattingVerbThatReachesAStringPrintsRedactedInsteadOfTheSecret(t *testing.T) {
	t.Parallel()

	held := citypes.Secret(thePastedPrivateKey)

	for _, verb := range []string{"%v", "%+v", "%#v", "%+#v", "%#+v", "%s", "%q"} {
		printed := fmt.Sprintf(verb, held)

		assert.NotContains(t, printed, theKeyMarker, verb)
		assert.NotContains(t, printed, "BEGIN OPENSSH PRIVATE KEY", verb)
		assert.Contains(t, printed, citypes.Redacted, verb)
	}

	assert.Equal(t, fmt.Sprintf("%x", citypes.Redacted), fmt.Sprintf("%x", held))
	assert.Equal(t, fmt.Sprintf("%X", citypes.Redacted), fmt.Sprintf("%X", held))
}

func TestASecretInAStructFieldIsRedactedByEveryVerbIncludingTheSharpVFamily(t *testing.T) {
	t.Parallel()

	held := theSlotHolder{Name: "flux-deploy-key", Value: citypes.Secret(thePastedPrivateKey)}

	for _, verb := range []string{"%v", "%+v", "%#v", "%+#v", "%#+v", "%s", "%q"} {
		printed := fmt.Sprintf(verb, held)

		assert.NotContains(t, printed, theKeyMarker, verb)
		assert.Contains(t, printed, "flux-deploy-key", verb)
		assert.Contains(t, printed, citypes.Redacted, verb)
	}
}

func TestASecretInAMapOrASliceOrAnErrorChainIsRedactedToo(t *testing.T) {
	t.Parallel()

	held := citypes.Secret(thePastedPrivateKey)

	assert.NotContains(t, fmt.Sprintf("%v", map[string]citypes.Secret{"key": held}), theKeyMarker)
	assert.NotContains(t, fmt.Sprintf("%#v", map[string]citypes.Secret{"key": held}), theKeyMarker)
	assert.NotContains(t, fmt.Sprintf("%v", []citypes.Secret{held}), theKeyMarker)
	assert.NotContains(t, fmt.Sprintf("%#v", []citypes.Secret{held}), theKeyMarker)
	assert.NotContains(t, fmt.Errorf("dialing with %v", held).Error(), theKeyMarker)
	assert.NotContains(t, fmt.Sprint(held), theKeyMarker)
}

func TestASecretReadFromAnUnsetVariableIsEmpty(t *testing.T) {
	t.Parallel()

	assert.Equal(t, citypes.Secret(""), citypes.SecretFromEnv("FORGE_CI_NO_SUCH_VARIABLE_IS_SET"))
}

func TestASecretReadFromAVariableCarriesItsValueAndStillNeverPrintsIt(t *testing.T) {
	t.Setenv("FORGE_CI_A_PASTED_SLOT", thePastedPrivateKey)

	held := citypes.SecretFromEnv("FORGE_CI_A_PASTED_SLOT")

	assert.Equal(t, thePastedPrivateKey, string(held))
	assert.NotContains(t, fmt.Sprintf("%v %#v %q", held, held, held), theKeyMarker)
}

func TestAResourceHoldingAPastedSecretIsRedactedBySharpVAsWellAsByV(t *testing.T) {
	t.Parallel()

	res := citypes.Resource{
		Kind: "secret",
		Name: "flux-deploy-key",
		Spec: map[string]any{"identity": thePastedPrivateKey},
	}

	for _, verb := range []string{"%v", "%+v", "%#v", "%+#v", "%#+v", "%s"} {
		printed := fmt.Sprintf(verb, res)

		assert.NotContains(t, printed, theKeyMarker, verb)
		assert.Contains(t, printed, "secret/flux-deploy-key", verb)
		assert.Contains(t, printed, "identity", verb)
	}
}
