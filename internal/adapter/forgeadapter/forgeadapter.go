package forgeadapter

import (
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
	"sigs.k8s.io/yaml"
)

const DefaultStorePath = ".forge/artifact-store.yaml"

type Harvester interface {
	Harvest(dir string, since time.Time) (*citypes.ForgeResult, error)
}

type Store struct {
	fs fsadapter.FS
}

var _ Harvester = Store{}

func New(fs fsadapter.FS) Store {
	return Store{fs: fs}
}

func (s Store) Harvest(dir string, since time.Time) (*citypes.ForgeResult, error) {
	path := filepath.Join(dir, DefaultStorePath)

	exists, err := s.fs.Exists(path)
	if err != nil {
		return nil, fmt.Errorf("looking for the artifact store in %s: %w", dir, err)
	}

	if !exists {
		return nil, nil
	}

	raw, err := s.fs.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading the artifact store in %s: %w", dir, err)
	}

	var store forge.ArtifactStore
	if err := yaml.Unmarshal(raw, &store); err != nil {
		return nil, fmt.Errorf("parsing the artifact store in %s: %w", dir, err)
	}

	result := &citypes.ForgeResult{Artifacts: store.Artifacts}

	names := make([]string, 0, len(store.TestReports))
	for name := range store.TestReports {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		report := store.TestReports[name]
		if report == nil {
			continue
		}

		if report.StartTime.Before(since) {
			continue
		}

		result.TestReports = append(result.TestReports, *report)
	}

	return result, nil
}
