//go:build conformance

package conformance_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/engineadapter"
	"github.com/stretchr/testify/require"
)

const (
	vectorPath = "../../.forge/spec-cache/revision/cases.json"

	stateEngine = "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-state-git@v0.1.7"
)

type op struct {
	Tool      string         `json:"tool"`
	In        map[string]any `json:"in"`
	Want      map[string]any `json:"want"`
	WantError string         `json:"wantError"`
}

type transportCase struct {
	Case string `json:"case"`
	Why  string `json:"why"`
	Ops  []op   `json:"ops"`
}

type vectors struct {
	Transport []transportCase `json:"transport"`
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "forge-ci-conformance")
	if err != nil {
		panic(err)
	}

	build := exec.Command("go", "build", "-o", dir, "./cmd/...")
	build.Dir = repoRoot()
	build.Stderr = os.Stderr

	if err := build.Run(); err != nil {
		panic(err)
	}

	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		panic(err)
	}

	code := m.Run()

	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func repoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	return filepath.Dir(filepath.Dir(wd))
}

func load(t *testing.T) vectors {
	t.Helper()

	raw, err := os.ReadFile(vectorPath)
	require.NoError(t, err, "run the resolve-spec build stage first")

	var v vectors
	require.NoError(t, json.Unmarshal(raw, &v))
	require.NotEmpty(t, v.Transport, "the contract has no transport vectors")

	return v
}

func TestEveryStateEngineSatisfiesTheTransportContract(t *testing.T) {
	caller := engineadapter.NewMCPCaller("", "conformance", os.Stderr)

	for _, c := range load(t).Transport {
		t.Run(c.Case, func(t *testing.T) {
			store := t.TempDir()

			for i, o := range c.Ops {
				in := map[string]any{"spec": map[string]any{"path": store}}
				for k, v := range o.In {
					in[k] = v
				}

				var got map[string]any

				err := caller.Call(context.Background(), stateEngine, o.Tool, in, &got)

				if o.WantError != "" {
					require.Error(t, err, "op %d: %s", i, c.Why)
					require.Contains(t, err.Error(), o.WantError, "op %d", i)

					continue
				}

				require.NoError(t, err, "op %d: %s", i, c.Why)

				for key, want := range o.Want {
					require.EqualValues(t, want, got[key],
						"op %d wanted %s to be %v: %s", i, key, want, c.Why)
				}
			}
		})
	}
}
