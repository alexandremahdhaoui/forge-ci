package artifactcontroller

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var (
	ErrVersion  = errors.New("the version is not a semver tag")
	ErrRevision = errors.New("a release needs the revision it publishes")
	ErrDirty    = errors.New("a dirty revision was never proven and must not be released")
)

// semver is deliberately strict. A tag is what a consumer pins, so a release
// that invents its own format is one nobody can depend on.
var semver = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$`)

// Plan is what a release would do. It is computed without touching anything, so
// the decision is testable and the engine only has to carry it out.
type Plan struct {
	Version string
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

	plan := Plan{Version: in.Version, Tags: []Tag{}, Uploads: []string{}}

	names := make([]string, 0, len(in.Repos))
	for name := range in.Repos {
		names = append(names, name)
	}

	sort.Strings(names)

	for _, name := range names {
		plan.Tags = append(plan.Tags, Tag{Repo: name, SHA: in.Repos[name]})
	}

	// forge records a location, which is a URL. Only a local file can be
	// uploaded, and a container image is published by whatever built it.
	for _, artifact := range in.Artifacts {
		path, ok := strings.CutPrefix(artifact.Location, "file://")
		if !ok || path == "" {
			continue
		}

		plan.Uploads = append(plan.Uploads, path)
	}

	sort.Strings(plan.Uploads)

	return plan, nil
}
