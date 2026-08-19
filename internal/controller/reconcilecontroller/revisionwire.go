package reconcilecontroller

import (
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-revision-spec/pkg/revisiontypes"
)

// The wire type is generated from forge-revision-spec's schema. forge-ci writes
// revisions and forge-factory reads them, and both speak this one contract
// without either importing the other.
//
// It is mapped here rather than used inside the controller, because a generated
// wire type is not an internal type. A change to the schema is a compile error
// at this boundary instead of a silent misread on the other side.
func toWire(revision citypes.Revision) revisiontypes.Revision {
	wire := revisiontypes.Revision{Id: revision.ID, CreatedAt: revision.CreatedAt}

	if len(revision.Repos) > 0 {
		repos := make(map[string]string, len(revision.Repos))
		for name, sha := range revision.Repos {
			repos[name] = sha
		}

		wire.Repos = &repos
	}

	if len(revision.Dirty) > 0 {
		dirty := append([]string{}, revision.Dirty...)
		wire.Dirty = &dirty
	}

	return wire
}
