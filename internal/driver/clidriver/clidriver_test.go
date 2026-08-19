package clidriver_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/driver/clidriver"
	"github.com/stretchr/testify/require"
)

const minimal = `
name: demo
managers:
  - alias: local
    engine: "go://x/cmd/m@v0.1.0"
engines:
  - alias: here
    type: compute
    engine: "go://x/cmd/c@v0.1.0"
    manager: local
  - alias: st
    type: state
    engine: "go://x/cmd/s@v0.1.0"
    manager: local
state: st
targets:
  - alias: build
    forge: test-all
stages:
  - name: build
    substages:
      - name: default
        engine: here
        manager: local
        targets: [build]
`

func write(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "pipeline.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	return path
}

func TestValidateReportsTheShape(t *testing.T) {
	var out bytes.Buffer

	err := clidriver.New(&out).Run([]string{"validate", "--config", write(t, minimal)})
	require.NoError(t, err)
	require.Contains(t, out.String(), "demo: 0 repos, 2 engines, 1 stages")
	require.Contains(t, out.String(), "1. build (1 substages)")
}

func TestValidateNamesTheFileItRejected(t *testing.T) {
	path := write(t, strings.Replace(minimal, "state: st", "state: here", 1))

	err := clidriver.New(&bytes.Buffer{}).Run([]string{"validate", "--config", path})
	require.Error(t, err)
	require.Contains(t, err.Error(), path)
	require.Contains(t, err.Error(), "want state")
}

func TestMissingFileIsNotAValidationError(t *testing.T) {
	err := clidriver.New(&bytes.Buffer{}).Run([]string{"validate", "--config", "/nope/pipeline.yaml"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "reading /nope/pipeline.yaml")
}

func TestNoArgsIsUsage(t *testing.T) {
	require.ErrorIs(t, clidriver.New(&bytes.Buffer{}).Run(nil), clidriver.ErrUsage)
}

func TestUnknownSubcommandIsUsage(t *testing.T) {
	err := clidriver.New(&bytes.Buffer{}).Run([]string{"deploy"})
	require.ErrorIs(t, err, clidriver.ErrUsage)
	require.Contains(t, err.Error(), "deploy")
}

func TestDuplicateKeyIsRejected(t *testing.T) {
	err := clidriver.New(&bytes.Buffer{}).Run([]string{"validate", "--config", write(t, minimal+"\nstate: here\n")})
	require.Error(t, err)
	require.Contains(t, err.Error(), `key "state" already set`)
}
