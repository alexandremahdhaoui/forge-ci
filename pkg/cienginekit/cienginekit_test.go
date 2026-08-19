package cienginekit_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/pkg/cienginekit"
	"github.com/stretchr/testify/require"
)

type in struct {
	Name string `json:"name"`
}

type out struct {
	Greeting string `json:"greeting"`
}

var errBoom = errors.New("boom")

func engine() cienginekit.Engine {
	return cienginekit.Engine{
		Name:    "ci-test",
		Version: "v0",
		Tools: []cienginekit.Tool{
			cienginekit.NewTool("greet", "Say hello.", func(_ context.Context, i in) (out, error) {
				return out{Greeting: "hello " + i.Name}, nil
			}),
			cienginekit.NewTool("boom", "Always fails.", func(_ context.Context, _ in) (out, error) {
				return out{}, errBoom
			}),
		},
	}
}

func pipeFiles(t *testing.T, body string) (*os.File, *os.File, func() string) {
	t.Helper()

	dir := t.TempDir()

	inPath := filepath.Join(dir, "in")
	require.NoError(t, os.WriteFile(inPath, []byte(body), 0o600))

	stdin, err := os.Open(inPath)
	require.NoError(t, err)

	t.Cleanup(func() { _ = stdin.Close() })

	outPath := filepath.Join(dir, "out")

	stdout, err := os.Create(outPath)
	require.NoError(t, err)

	t.Cleanup(func() { _ = stdout.Close() })

	return stdin, stdout, func() string {
		require.NoError(t, stdout.Sync())

		body, err := os.ReadFile(outPath)
		require.NoError(t, err)

		return string(body)
	}
}

func TestAToolReadsJSONFromStdinAndWritesJSONToStdout(t *testing.T) {
	stdin, stdout, read := pipeFiles(t, `{"name":"world"}`)

	require.NoError(t, engine().RunCLI([]string{"greet"}, stdin, stdout))

	var got out
	require.NoError(t, json.Unmarshal([]byte(read()), &got))
	require.Equal(t, "hello world", got.Greeting)
}

func TestEmptyStdinIsAllowed(t *testing.T) {
	stdin, stdout, read := pipeFiles(t, "")

	require.NoError(t, engine().RunCLI([]string{"greet"}, stdin, stdout))
	require.Contains(t, read(), `"greeting": "hello "`)
}

func TestAToolErrorIsPropagated(t *testing.T) {
	stdin, stdout, _ := pipeFiles(t, `{}`)

	require.ErrorIs(t, engine().RunCLI([]string{"boom"}, stdin, stdout), errBoom)
}

func TestBadJSONNamesTheTool(t *testing.T) {
	stdin, stdout, _ := pipeFiles(t, `not json`)

	err := engine().RunCLI([]string{"greet"}, stdin, stdout)
	require.Error(t, err)
	require.Contains(t, err.Error(), "decoding input for greet")
}

func TestNoToolListsWhatIsAvailable(t *testing.T) {
	err := engine().RunCLI(nil, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "greet")
	require.Contains(t, err.Error(), "boom")
}

func TestAnUnknownToolListsWhatIsAvailable(t *testing.T) {
	err := engine().RunCLI([]string{"fly"}, nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown tool "fly"`)
	require.True(t, strings.Contains(err.Error(), "greet"))
}

func TestANilStdinIsTreatedAsEmpty(t *testing.T) {
	_, stdout, read := pipeFiles(t, "")

	require.NoError(t, engine().RunCLI([]string{"greet"}, nil, stdout))
	require.Contains(t, read(), "greeting")
}

func TestAClosedStdinIsReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "closed")
	require.NoError(t, os.WriteFile(path, nil, 0o600))

	f, err := os.Open(path)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	_, stdout, _ := pipeFiles(t, "")

	err = engine().RunCLI([]string{"greet"}, f, stdout)
	require.Error(t, err)
	require.Contains(t, err.Error(), "reading stdin")
}
