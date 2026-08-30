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

func (r LocalRealizer) Realize(res citypes.Resource) (Action, error) {
	switch res.Kind {
	case KindDirectory:
		// Existence is read first because MkdirAll cannot say whether it
		// made anything. Reporting a creation that did not happen would
		// stop every run as though the pipeline had converged something.
		exists, err := r.fs.Exists(res.Name)
		if err != nil {
			return Action{}, err
		}

		if exists {
			return Kept("kept directory " + res.Name), nil
		}

		if err := r.fs.MkdirAll(res.Name); err != nil {
			return Action{}, err
		}

		return Did("created directory " + res.Name), nil
	case KindFile:
		exists, err := r.fs.Exists(res.Name)
		if err != nil {
			return Action{}, err
		}

		if exists {
			return Kept("kept file " + res.Name), nil
		}

		body, _ := res.Spec["content"].(string)

		if err := r.fs.WriteFile(res.Name, []byte(body)); err != nil {
			return Action{}, err
		}

		return Did("created file " + res.Name), nil
	default:
		return Action{}, fmt.Errorf("the local manager cannot realize kind %q, it knows %s and %s",
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

// Realize reports what would happen and never that anything did. A dry run
// moves nothing, so it must never stop a run: the whole point is to say what
// an apply would do without doing it.
func (DryRunRealizer) Realize(res citypes.Resource) (Action, error) {
	return Kept("would realize " + res.ID()), nil
}
