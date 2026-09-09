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

func TestTalosconfigEnvIsEmptyWhenTheManagerSpecNamesNoneSoTheRealizerNamesTheVariableItReads(t *testing.T) {
	t.Parallel()

	for name, spec := range map[string]map[string]interface{}{
		"the key is absent": {},
		"the key is nil":    {"talosconfigEnv": nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			variable, err := talosconfigEnv(spec)
			require.NoError(t, err)
			assert.Empty(t, variable)
		})
	}
}

func TestTalosconfigEnvIsWhatTheManagerSpecNames(t *testing.T) {
	t.Parallel()

	variable, err := talosconfigEnv(map[string]interface{}{"talosconfigEnv": "T0_TALOSCONFIG"})
	require.NoError(t, err)
	assert.Equal(t, "T0_TALOSCONFIG", variable)
}

func TestTalosconfigEnvRefusesASpecThatHoldsSomethingOtherThanAString(t *testing.T) {
	t.Parallel()

	_, err := talosconfigEnv(map[string]interface{}{"talosconfigEnv": 7})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading spec.talosconfigEnv")
	assert.Contains(t, err.Error(), "a string is required")
	assert.Contains(t, err.Error(), "int")
}
