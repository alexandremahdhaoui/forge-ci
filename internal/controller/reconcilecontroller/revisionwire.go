package reconcilecontroller

import (
	"encoding/json"
	"reflect"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge-revision-spec/pkg/revisiontypes"
)

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

func sameBeyondItsTimestamp(payload string, revision citypes.Revision) (bool, error) {
	var recorded revisiontypes.Revision
	if err := json.Unmarshal([]byte(payload), &recorded); err != nil {
		return false, err
	}

	minted := toWire(revision)
	recorded.CreatedAt = minted.CreatedAt

	return reflect.DeepEqual(recorded, minted), nil
}
