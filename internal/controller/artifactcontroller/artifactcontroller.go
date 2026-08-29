package artifactcontroller

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var (
	ErrVersion  = errors.New("the version is not a semver tag")
	ErrPrevious = errors.New("the previous version is not a semver tag")
	ErrRevision = errors.New("a release needs the revision it publishes")
	ErrDirty    = errors.New("a dirty revision was never proven and must not be released")
	// ErrCollision means two artifacts travel under one file name, so the
	// release would overwrite one with the other.
	ErrCollision = errors.New("two artifacts claim the same asset name")
)

// semver is deliberately strict. A tag is what a consumer pins, so a release
// that invents its own format is one nobody can depend on.
var semver = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$`)

// Plan is what a release would do. It is computed without touching anything, so
// the decision is testable and the engine only has to carry it out.
type Plan struct {
	Version string

	// TagName is the one tag every member carries and the release is created
	// under: the version, with the factory's prefix in front of it when it
	// has one. One number for the whole workspace, so a consumer who knows
	// the version of one member knows the version of all of them.
	TagName string

	Tags    []Tag
	Uploads []string
}

// Tag is one repo to tag at one commit.
type Tag struct {
	Repo string
	SHA  string
}

type Controller struct{}

func New() *Controller {
	return &Controller{}
}

// Declare reports what a release needs to exist. Nothing: a release writes to
// someone else's system and owns no resource of its own.
func (c *Controller) Declare(_ map[string]any) (citypes.DeclareOutput, error) {
	return citypes.DeclareOutput{Resources: []citypes.Resource{}}, nil
}

// Plan decides what to publish. A revision that was never proven, or one minted
// over uncommitted work, is refused rather than released.
func (c *Controller) Plan(in citypes.ArtifactInput) (Plan, error) {
	if strings.TrimSpace(in.Revision) == "" {
		return Plan{}, ErrRevision
	}

	if strings.HasSuffix(in.Revision, "-dirty") {
		return Plan{}, fmt.Errorf("%w: %s", ErrDirty, in.Revision)
	}

	if !semver.MatchString(in.Version) {
		return Plan{}, fmt.Errorf("%w: %q", ErrVersion, in.Version)
	}

	plan := Plan{
		Version: in.Version,
		TagName: TagName(in.TagPrefix, in.Version),
		Tags:    []Tag{},
		Uploads: []string{},
	}

	names := make([]string, 0, len(in.Repos))
	for name := range in.Repos {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		plan.Tags = append(plan.Tags, Tag{Repo: name, SHA: in.Repos[name]})
	}

	// What uploads: exactly what travels, and a thing travels only under the
	// `name_os_arch` convention - the name says which machine it runs on, so
	// a consumer can pick its own. A host build carries no platform in its
	// name and stays home; a container image or a URL is somebody else's to
	// serve, and a local path never carries a colon.
	seen := map[string]bool{}
	byName := map[string]string{}

	for _, artifact := range in.Artifacts {
		path := artifact.Location
		if trimmed, ok := strings.CutPrefix(path, "file://"); ok {
			path = trimmed
		} else if strings.Contains(path, ":") {
			continue
		}

		if path == "" || seen[path] {
			continue
		}

		if !platformSuffix.MatchString(filepath.Base(path)) {
			continue
		}

		seen[path] = true

		// One release, one asset per name: two artifacts that travel under
		// the same file name would overwrite each other in the release, so
		// the collision is refused and both sources are named. This is a
		// declaration mistake in the repos, not a machine failure.
		base := filepath.Base(path)
		if first, clash := byName[base]; clash {
			return Plan{}, fmt.Errorf("%w: %q is built by both %s and %s", ErrCollision, base, first, path)
		}

		byName[base] = path

		plan.Uploads = append(plan.Uploads, path)
	}

	sort.Strings(plan.Uploads)

	return plan, nil
}
