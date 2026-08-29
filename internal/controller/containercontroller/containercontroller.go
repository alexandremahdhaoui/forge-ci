// Package containercontroller decides what a container release publishes. It
// reaches nothing: the decision is computed from the input alone, so it is
// testable without a registry, and the adapter only carries it out.
package containercontroller

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

// TypeContainer is the artifact type a container build records. Filtering on
// it is what keeps a container off the binary upload path and a binary out of
// the registry.
const TypeContainer = "container"

var (
	ErrVersion = errors.New("the version is not a semver tag")
	// ErrRevision means a release was asked for with no revision, so nothing
	// says what was proven.
	ErrRevision = errors.New("a release needs the revision it publishes")
	// ErrDirty means the revision covered uncommitted work, so it was never
	// proven and must not be published.
	ErrDirty = errors.New("a dirty revision was never proven and must not be released")
	// ErrImage means spec.image names no registry repository.
	ErrImage = errors.New("spec.image is required and names where the image is published")
	// ErrNoImages means the stage that builds the image did not run, or built
	// nothing. Publishing nothing quietly is how a tag ends up pointing at
	// last week's image.
	ErrNoImages = errors.New("no container artifact was built")
	// ErrLocation means a container artifact carries a location this cannot
	// read. The build writes a local layout, so a remote reference here means
	// the two sides disagree about what was built.
	ErrLocation = errors.New("a container artifact must carry a local layout path")
)

// semver is the same strictness the binary release uses: a tag is what a
// consumer pins, so a release that invents its own format is one nobody can
// depend on.
var semver = regexp.MustCompile(`^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?$`)

// Plan is what a container release would do. It computes no version and reads
// no tag line: the version arrives decided, because two authorities over one
// number is how every member of a workspace drifted onto a line of its own.
type Plan struct {
	// Image is the repository, with no tag.
	Image string

	// Tags are the references to push, longest-lived first. Every one of
	// them points at the same index.
	Tags []string

	// Layouts are the local OCI layout directories to publish, one per
	// container artifact the runs built.
	Layouts []string

	// Labels ride into the image config.
	Labels map[string]string
}

type Controller struct{}

func New() *Controller {
	return &Controller{}
}

// Declare reports what a container release needs to exist. Nothing: it writes
// to somebody else's registry and owns no resource of its own.
func (c *Controller) Declare(_ map[string]any) (citypes.DeclareOutput, error) {
	return citypes.DeclareOutput{Resources: []citypes.Resource{}}, nil
}

// Plan decides what to publish. A revision that was never proven, or one
// minted over uncommitted work, is refused rather than released.
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

	image, _ := in.Spec["image"].(string)

	image = strings.TrimSpace(image)
	if image == "" {
		return Plan{}, ErrImage
	}

	// A tag or a digest lives in the LAST path segment. A colon anywhere
	// else is a registry port: "localhost:5000/forge" is a repository and
	// not a tagged image, and reading it as one refused every registry that
	// is not on 443.
	last := image
	if i := strings.LastIndex(image, "/"); i >= 0 {
		last = image[i+1:]
	}

	if strings.ContainsAny(last, ":@") {
		return Plan{}, fmt.Errorf("%w: %q already carries a tag or a digest, and the tag is the version",
			ErrImage, image)
	}

	layouts, err := layoutsOf(in.Artifacts)
	if err != nil {
		return Plan{}, err
	}

	plan := Plan{
		Image:   image,
		Tags:    tagsFor(image, in.TagPrefix, in.Version, in.Spec),
		Layouts: layouts,
		Labels:  labelsFor(in),
	}

	return plan, nil
}

// tagsFor is the version, plus whatever moving tags the instance asked for.
// The version tag is first and it is the one that never moves; a moving tag
// is a convenience and never something to pin.
func tagsFor(image, prefix, version string, spec map[string]any) []string {
	tag := version
	if prefix != "" {
		tag = prefix + "-" + version
	}

	out := []string{image + ":" + tag}

	raw, _ := spec["movingTags"].([]any)

	seen := map[string]bool{tag: true}

	for _, entry := range raw {
		name, _ := entry.(string)

		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}

		seen[name] = true

		out = append(out, image+":"+name)
	}

	return out
}

// labelsFor records where the image came from, so somebody holding only the
// image can find the revision it was built from. The instance's own labels
// win, because it knows things this does not.
func labelsFor(in citypes.ArtifactInput) map[string]string {
	out := map[string]string{
		"org.opencontainers.image.version":  in.Version,
		"org.opencontainers.image.revision": in.Revision,
	}

	raw, _ := in.Spec["labels"].(map[string]any)
	for k, v := range raw {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}

	return out
}

// layoutsOf picks the container artifacts out of everything the runs built.
// Filtering on the type is what keeps a binary out of the registry and a
// container off the upload path.
func layoutsOf(artifacts []forge.Artifact) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}

	for _, a := range artifacts {
		if a.Type != TypeContainer {
			continue
		}

		path, found := strings.CutPrefix(a.Location, "file://")
		if !found {
			return nil, fmt.Errorf("%w: %q carries %q", ErrLocation, a.Name, a.Location)
		}

		if path == "" || seen[path] {
			continue
		}

		seen[path] = true

		out = append(out, path)
	}

	if len(out) == 0 {
		return nil, ErrNoImages
	}

	sort.Strings(out)

	return out, nil
}
