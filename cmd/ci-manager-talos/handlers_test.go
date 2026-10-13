package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyModeIsEmptyWhenTheManagerSpecNamesNone(t *testing.T) {
	t.Parallel()

	for name, spec := range map[string]map[string]interface{}{
		"the key is absent": {},
		"the key is nil":    {"applyMode": nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			mode, err := applyMode(spec)
			require.NoError(t, err)
			assert.Empty(t, mode)
		})
	}
}

func TestApplyModeIsWhatTheManagerSpecNames(t *testing.T) {
	t.Parallel()

	mode, err := applyMode(map[string]interface{}{"applyMode": "staged"})
	require.NoError(t, err)
	assert.Equal(t, "staged", mode)
}

func TestApplyModeRefusesASpecThatHoldsSomethingOtherThanAString(t *testing.T) {
	t.Parallel()

	_, err := applyMode(map[string]interface{}{"applyMode": []string{"staged"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading spec.applyMode")
	assert.Contains(t, err.Error(), "a string is required")
	assert.Contains(t, err.Error(), "[]string")
}

func TestTheReconcileToolRefusesAManagerSpecThatStillSetsTheRetiredTalosconfigEnvByName(t *testing.T) {
	t.Parallel()

	for name, value := range map[string]interface{}{
		"a variable name": "T0_TALOSCONFIG",
		"an empty value":  "",
		"a nil value":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			out, err := NewHandlers().Reconcile(t.Context(), ReconcileInput{
				Manager: "talos",
				Spec:    Spec{"talosconfigEnv": value},
			})
			require.Error(t, err)
			assert.Nil(t, out)
			assert.Equal(t,
				"reading the spec of manager talos: this manager refuses spec.talosconfigEnv: "+
					"each resource names its own talosconfig variable, written by the engine that declares it",
				err.Error())
		})
	}
}
