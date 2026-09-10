package citypes_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var thePastedPrivateKey = "-----BEGIN OPENSSH PRIVATE KEY-----\n" +
	"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtz\n" +
	theKeyMarker + "\n" +
	strings.Repeat("cHJpdmF0ZWtleQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA\n", 24) +
	"-----END OPENSSH PRIVATE KEY-----\n"

func aDeclaredSecret() citypes.Resource {
	return citypes.Resource{
		Kind: "secret",
		Name: "flux-deploy-key",
		Spec: map[string]any{
			"namespace": "flux-system",
			"name":      "flux-deploy-key",
			"data":      map[string]any{"identity": thePastedPrivateKey},
		},
	}
}

func TestFormattingAResourceNamesItsKindItsNameAndItsSpecKeysInOrder(t *testing.T) {
	assert.Equal(t,
		"secret/flux-deploy-key holding spec keys data, name, namespace",
		aDeclaredSecret().String())
}

func TestNoVerbThatFormatsAResourceCanReachASpecValue(t *testing.T) {
	res := aDeclaredSecret()

	for _, verb := range []string{"%v", "%+v", "%s", "%q"} {
		printed := fmt.Sprintf(verb, res)

		assert.NotContains(t, printed, thePastedPrivateKey, verb)
		assert.NotContains(t, printed, "BEGIN OPENSSH PRIVATE KEY", verb)
		assert.NotContains(t, printed, "b3BlbnNzaC1rZXktdjEAAAAABG5vbmU", verb)
		assert.Contains(t, printed, "spec keys data, name, namespace", verb)
	}
}

func TestAResourceInsideAnotherValueIsStillFormattedThroughItsOwnString(t *testing.T) {
	in := citypes.ReconcileInput{
		Manager:   "kubernetes",
		Resources: []citypes.Resource{aDeclaredSecret()},
	}

	printed := fmt.Sprintf("%+v", in)

	assert.NotContains(t, printed, thePastedPrivateKey)
	assert.Contains(t, printed, "secret/flux-deploy-key holding spec keys data, name, namespace")
}

func TestFormattingAResourceWithNoKindAndNoNameNamesWhatIsMissing(t *testing.T) {
	res := citypes.Resource{Spec: map[string]any{"data": map[string]any{"identity": thePastedPrivateKey}}}

	assert.Equal(t, "<no kind>/<no name> holding spec keys data", res.String())
}

func TestFormattingAResourceThatCarriesNoSpecSaysItHoldsNone(t *testing.T) {
	assert.Equal(t, "directory/runs holding no spec",
		citypes.Resource{Kind: "directory", Name: "runs"}.String())
	assert.Equal(t, "directory/runs holding no spec",
		citypes.Resource{Kind: "directory", Name: "runs", Spec: map[string]any{}}.String())
}

func TestAnOwnershipCarriesNoSpecAndSoCanEchoNothing(t *testing.T) {
	printed := fmt.Sprintf("%+v",
		citypes.Ownership{Resource: aDeclaredSecret().ID(), Manager: "kubernetes"})

	assert.NotContains(t, printed, thePastedPrivateKey)
	assert.Contains(t, printed, "secret/flux-deploy-key")
}

func TestTheIDOfAResourceIsUnchangedByItsString(t *testing.T) {
	assert.Equal(t, "secret/flux-deploy-key", aDeclaredSecret().ID())
}
