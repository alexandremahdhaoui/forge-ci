package kubeconfigadapter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
)

const (
	fileName = "kubeconfig"

	exporter = "kind"
)

type File struct {
	path string
}

func NewFile(path string) File {
	return File{path: path}
}

func (f File) Kubeconfig(context.Context) ([]byte, error) {
	raw, err := os.ReadFile(f.path)
	if err != nil {
		return nil, fmt.Errorf("reading the kubeconfig file at %s: %w", f.path, err)
	}

	return raw, nil
}

type Kind struct {
	runner  execadapter.Runner
	cluster string
}

func NewKind(runner execadapter.Runner, cluster string) Kind {
	return Kind{runner: runner, cluster: cluster}
}

func (k Kind) Kubeconfig(ctx context.Context) ([]byte, error) {
	res, err := k.runner.Run(ctx, "", exporter, "get", "kubeconfig", "--name", k.cluster)
	if err != nil {
		return nil, fmt.Errorf("exporting the kubeconfig of cluster %s: %w", k.cluster, err)
	}

	if res.ExitCode != 0 {
		return nil, fmt.Errorf(
			"exporting the kubeconfig of cluster %s: it exited %d saying %q",
			k.cluster, res.ExitCode, strings.TrimSpace(res.Stderr))
	}

	if strings.TrimSpace(res.Stdout) == "" {
		return nil, fmt.Errorf("exporting the kubeconfig of cluster %s: it wrote nothing", k.cluster)
	}

	return []byte(res.Stdout), nil
}

func Write(dir string, raw []byte) (string, error) {
	path := filepath.Join(dir, fileName)

	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return "", fmt.Errorf("writing the kubeconfig to %s: %w", path, err)
	}

	return path, nil
}
