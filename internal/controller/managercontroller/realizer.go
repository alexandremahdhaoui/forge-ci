package managercontroller

import (
	"fmt"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	KindDirectory = "directory"
	KindFile      = "file"
)

type LocalRealizer struct {
	fs fsadapter.FS
}

var _ Realizer = LocalRealizer{}

func NewLocalRealizer(fs fsadapter.FS) LocalRealizer {
	return LocalRealizer{fs: fs}
}

func (LocalRealizer) Kind() string {
	return "local"
}

func (r LocalRealizer) Realize(res citypes.Resource) (string, error) {
	switch res.Kind {
	case KindDirectory:
		if err := r.fs.MkdirAll(res.Name); err != nil {
			return "", err
		}

		return "created directory " + res.Name, nil
	case KindFile:
		exists, err := r.fs.Exists(res.Name)
		if err != nil {
			return "", err
		}

		if exists {
			return "kept file " + res.Name, nil
		}

		body, _ := res.Spec["content"].(string)

		if err := r.fs.WriteFile(res.Name, []byte(body)); err != nil {
			return "", err
		}

		return "created file " + res.Name, nil
	default:
		return "", fmt.Errorf("the local manager cannot realize kind %q, it knows %s and %s",
			res.Kind, KindDirectory, KindFile)
	}
}

type DryRunRealizer struct{}

var _ Realizer = DryRunRealizer{}

func NewDryRunRealizer() DryRunRealizer {
	return DryRunRealizer{}
}

func (DryRunRealizer) Kind() string {
	return "dryrun"
}

func (DryRunRealizer) Realize(res citypes.Resource) (string, error) {
	return "would realize " + res.ID(), nil
}
