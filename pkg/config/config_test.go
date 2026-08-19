package config_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
	"github.com/stretchr/testify/require"
)

const vectorPath = "../../.forge/spec-cache/cases.json"

type vectors struct {
	Valid []struct {
		Case string          `json:"case"`
		Why  string          `json:"why"`
		Doc  json.RawMessage `json:"doc"`
	} `json:"valid"`
	Invalid []struct {
		Case     string          `json:"case"`
		Why      string          `json:"why"`
		Error    string          `json:"error"`
		Semantic bool            `json:"semantic"`
		Doc      json.RawMessage `json:"doc"`
	} `json:"invalid"`
}

func load(t *testing.T) vectors {
	t.Helper()

	raw, err := os.ReadFile(vectorPath)
	require.NoError(t, err, "run the resolve-spec build stage first")

	var v vectors
	require.NoError(t, json.Unmarshal(raw, &v))
	require.NotEmpty(t, v.Valid)
	require.NotEmpty(t, v.Invalid)

	return v
}

func TestValidVectorsParse(t *testing.T) {
	for _, c := range load(t).Valid {
		t.Run(c.Case, func(t *testing.T) {
			_, err := config.Parse(c.Doc)
			require.NoError(t, err, c.Why)
		})
	}
}

func TestInvalidVectorsAreRejected(t *testing.T) {
	for _, c := range load(t).Invalid {
		t.Run(c.Case, func(t *testing.T) {
			_, err := config.Parse(c.Doc)
			require.Error(t, err, c.Why)
			require.Contains(t, err.Error(), c.Error)
		})
	}
}

func TestSemanticVectorsNeedTheSecondPass(t *testing.T) {
	for _, c := range load(t).Invalid {
		if !c.Semantic {
			continue
		}

		t.Run(c.Case, func(t *testing.T) {
			var p config.Pipeline
			require.NoError(t, json.Unmarshal(c.Doc, &p),
				"a semantic vector must be structurally well formed")
			require.Error(t, p.Validate())
		})
	}
}

func TestUnknownKeyIsRejected(t *testing.T) {
	_, err := config.Parse([]byte(`{"name":"x","pipelines":[]}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "pipelines")
}

func TestEveryPortIsAccepted(t *testing.T) {
	for _, p := range []config.Port{
		config.PortCompute,
		config.PortState,
		config.PortTrigger,
		config.PortGate,
		config.PortPromotion,
		config.PortArtifact,
	} {
		require.NotEmpty(t, string(p))
		require.Equal(t, strings.ToLower(string(p)), string(p))
	}
}
