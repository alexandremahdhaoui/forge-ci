package computecontroller

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

// A build leaves files on one runner's disk and the run record keeps only
// their paths, so a release on a runner that never built them reads
// nothing. put and get are the door out of that: put hands a run's files
// to a place this engine can serve again, get brings them back.
//
// This engine's place is a directory under the root, keyed by revision,
// and the location it hands back is one URL shape:
//
//	forge-ci-artifact://<revision>/<root-relative path>
//
// The local engine serves it from the directory alone. The GitHub engine
// serves the same directory through the workflow it renders, which uploads
// it after the build job and downloads it before the release job, so both
// engines share one controller and one URL, and only the transport differs.

const (
	// ArtifactScheme prefixes every location a put answers.
	ArtifactScheme = "forge-ci-artifact://"
	// ArtifactDir is where put keeps the files, under the root.
	ArtifactDir = ".forge-ci/artifacts"
)

var (
	ErrArtifactPath = errors.New("an artifact to put must be a path under the root")
	ErrArtifactURL  = errors.New("an artifact to get must carry a location a put answered")
	ErrArtifactSum  = errors.New("an artifact came back with different bytes")
)

// Put copies every path-located artifact into the revision's directory and
// answers the records with their locations rewritten. A record whose
// location is already a URL, an image reference say, passes through: it is
// somebody else's to serve.
func (c *Controller) Put(fs fsadapter.FS, in citypes.ArtifactPutInput) (citypes.ArtifactPutOutput, error) {
	if strings.TrimSpace(in.Revision) == "" {
		return citypes.ArtifactPutOutput{}, errors.New("putting artifacts: no revision given")
	}

	out := citypes.ArtifactPutOutput{Artifacts: make([]forge.Artifact, 0, len(in.Artifacts))}

	for _, artifact := range in.Artifacts {
		rel, local := localPath(in.Root, artifact.Location)
		if !local {
			out.Artifacts = append(out.Artifacts, artifact)

			continue
		}

		src := filepath.Join(in.Root, rel)
		dst := filepath.Join(in.Root, ArtifactDir, in.Revision, rel)

		// A directory artifact - a generated tree, an image layout - stays
		// where it was built. A release uploads files, and a tree that
		// must travel is a later refinement, not a copy of every file
		// forge ever generated.
		dir, err := fs.IsDir(src)
		if err != nil {
			return citypes.ArtifactPutOutput{}, fmt.Errorf("putting %s: %w", rel, err)
		}

		if dir {
			out.Artifacts = append(out.Artifacts, artifact)

			continue
		}

		if err := copyFile(fs, src, dst); err != nil {
			return citypes.ArtifactPutOutput{}, fmt.Errorf("putting %s: %w", rel, err)
		}

		artifact.Location = ArtifactScheme + in.Revision + "/" + filepath.ToSlash(rel)
		out.Artifacts = append(out.Artifacts, artifact)
	}

	return out, nil
}

// Get copies every artifact a put answered back to its path under the root
// and verifies the bytes against the copy put kept. A location this engine
// never answered is refused rather than guessed at.
func (c *Controller) Get(fs fsadapter.FS, in citypes.ArtifactGetInput) (citypes.ArtifactGetOutput, error) {
	out := citypes.ArtifactGetOutput{Artifacts: make([]forge.Artifact, 0, len(in.Artifacts))}

	for _, artifact := range in.Artifacts {
		rest, ok := strings.CutPrefix(artifact.Location, ArtifactScheme)
		if !ok {
			// A path is already home and a foreign URL is not ours to
			// fetch; both pass through.
			out.Artifacts = append(out.Artifacts, artifact)

			continue
		}

		revision, rel, ok := strings.Cut(rest, "/")
		if !ok || revision == "" || rel == "" {
			return citypes.ArtifactGetOutput{}, fmt.Errorf("%w: %s", ErrArtifactURL, artifact.Location)
		}

		src := filepath.Join(in.Root, ArtifactDir, revision, filepath.FromSlash(rel))
		dst := filepath.Join(in.Root, filepath.FromSlash(rel))

		if err := copyFile(fs, src, dst); err != nil {
			return citypes.ArtifactGetOutput{}, fmt.Errorf("getting %s: %w", rel, err)
		}

		want, _, err := fsadapter.Digest(src)
		if err != nil {
			return citypes.ArtifactGetOutput{}, fmt.Errorf("getting %s: %w", rel, err)
		}

		got, _, err := fsadapter.Digest(dst)
		if err != nil {
			return citypes.ArtifactGetOutput{}, fmt.Errorf("getting %s: %w", rel, err)
		}

		if want != got {
			return citypes.ArtifactGetOutput{}, fmt.Errorf("%w: %s", ErrArtifactSum, rel)
		}

		artifact.Location = filepath.ToSlash(rel)
		out.Artifacts = append(out.Artifacts, artifact)
	}

	return out, nil
}

// localPath answers the root-relative path of a location that is a file
// under the root, and false for anything else: an empty location, a URL, an
// image reference, or a path that escapes the root.
func localPath(root, location string) (string, bool) {
	if trimmed, ok := strings.CutPrefix(location, "file://"); ok {
		location = trimmed
	} else if location == "" || strings.Contains(location, ":") {
		return "", false
	}

	if filepath.IsAbs(location) {
		rel, err := filepath.Rel(root, location)
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", false
		}

		return rel, true
	}

	clean := filepath.Clean(location)
	if strings.HasPrefix(clean, "..") {
		return "", false
	}

	return clean, true
}

func copyFile(fs fsadapter.FS, src, dst string) error {
	data, err := fs.ReadFile(src)
	if err != nil {
		return err
	}

	if err := fs.MkdirAll(filepath.Dir(dst)); err != nil {
		return err
	}

	return fs.WriteFile(dst, data)
}
