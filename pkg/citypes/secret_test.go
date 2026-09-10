package citypes_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const aRealSizedPrivateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAABlwAAAAdzc2gtcn\n" +
	"NhAAAAAwEAAQAAAYEAZZZTOPSECRETZZZclientprivatekeyZZZTOPSECRETZZZclient\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n" +
	"-----END OPENSSH PRIVATE KEY-----\n"

const theKeyMarker = "ZZZTOPSECRETZZZclientprivatekey"

type theSlotHolder struct {
	Name  string
	Value citypes.Secret
}

func TestTheFixtureIsAsLargeAsAnOpenSSHPrivateKeyAndOverThePathCeiling(t *testing.T) {
	t.Parallel()

	assert.Greater(t, len(aRealSizedPrivateKey), 1400)
	assert.Greater(t, len(aRealSizedPrivateKey), 255)
}

func TestEveryFormattingVerbThatReachesAStringPrintsRedactedInsteadOfTheSecret(t *testing.T) {
	t.Parallel()

	held := citypes.Secret(aRealSizedPrivateKey)

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

	held := theSlotHolder{Name: "flux-deploy-key", Value: citypes.Secret(aRealSizedPrivateKey)}

	for _, verb := range []string{"%v", "%+v", "%#v", "%+#v", "%#+v", "%s", "%q"} {
		printed := fmt.Sprintf(verb, held)

		assert.NotContains(t, printed, theKeyMarker, verb)
		assert.Contains(t, printed, "flux-deploy-key", verb)
		assert.Contains(t, printed, citypes.Redacted, verb)
	}
}

func TestASecretInAMapOrASliceOrAnErrorChainIsRedactedToo(t *testing.T) {
	t.Parallel()

	held := citypes.Secret(aRealSizedPrivateKey)

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
	t.Setenv("FORGE_CI_A_PASTED_SLOT", aRealSizedPrivateKey)

	held := citypes.SecretFromEnv("FORGE_CI_A_PASTED_SLOT")

	assert.Equal(t, aRealSizedPrivateKey, string(held))
	assert.NotContains(t, fmt.Sprintf("%v %#v %q", held, held, held), theKeyMarker)
}

func TestAResourceHoldingAPastedSecretIsRedactedBySharpVAsWellAsByV(t *testing.T) {
	t.Parallel()

	res := citypes.Resource{
		Kind: "secret",
		Name: "flux-deploy-key",
		Spec: map[string]any{"identity": aRealSizedPrivateKey},
	}

	for _, verb := range []string{"%v", "%+v", "%#v", "%+#v", "%#+v", "%s"} {
		printed := fmt.Sprintf(verb, res)

		assert.NotContains(t, printed, theKeyMarker, verb)
		assert.Contains(t, printed, "secret/flux-deploy-key", verb)
		assert.Contains(t, printed, "identity", verb)
	}
}
