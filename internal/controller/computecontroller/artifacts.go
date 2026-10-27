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

const (
	ArtifactScheme = "forge-ci-artifact://"
	ArtifactDir    = citypes.ArtifactDir
)

var (
	ErrArtifactPath = errors.New("an artifact to put must be a path under the root")
	ErrArtifactURL  = errors.New("an artifact to get must carry a location a put answered")
	ErrArtifactSum  = errors.New("an artifact came back with different bytes")
)

func (c *Controller) Put(in citypes.ArtifactPutInput) (citypes.ArtifactPutOutput, error) {
	if strings.TrimSpace(in.Revision) == "" {
		return citypes.ArtifactPutOutput{}, errors.New("putting artifacts: no revision given")
	}

	fs := c.fs

	out := citypes.ArtifactPutOutput{Artifacts: make([]forge.Artifact, 0, len(in.Artifacts))}

	for _, artifact := range in.Artifacts {
		rel, local := localPath(in.Root, artifact.Location)
		if !local {
			out.Artifacts = append(out.Artifacts, artifact)

			continue
		}

		src := filepath.Join(in.Root, rel)
		dst := filepath.Join(in.Root, ArtifactDir, in.Revision, rel)

		dir, err := fs.IsDir(src)
		if err != nil {
			return citypes.ArtifactPutOutput{}, fmt.Errorf("putting %s: %w", rel, err)
		}

		if dir && !treeTravels(artifact) {
			out.Artifacts = append(out.Artifacts, artifact)

			continue
		}

		if dir {
			err = copyTree(fs, src, dst)
		} else {
			err = copyFile(fs, src, dst)
		}

		if err != nil {
			return citypes.ArtifactPutOutput{}, fmt.Errorf("putting %s: %w", rel, err)
		}

		artifact.Location = ArtifactScheme + in.Revision + "/" + filepath.ToSlash(rel)
		out.Artifacts = append(out.Artifacts, artifact)
	}

	return out, nil
}

func (c *Controller) Get(in citypes.ArtifactGetInput) (citypes.ArtifactGetOutput, error) {
	fs := c.fs
	out := citypes.ArtifactGetOutput{Artifacts: make([]forge.Artifact, 0, len(in.Artifacts))}

	for _, artifact := range in.Artifacts {
		rest, ok := strings.CutPrefix(artifact.Location, ArtifactScheme)
		if !ok {
			out.Artifacts = append(out.Artifacts, artifact)

			continue
		}

		revision, rel, ok := strings.Cut(rest, "/")
		if !ok || revision == "" || rel == "" {
			return citypes.ArtifactGetOutput{}, fmt.Errorf("%w: %s", ErrArtifactURL, artifact.Location)
		}

		src := filepath.Join(in.Root, ArtifactDir, revision, filepath.FromSlash(rel))
		dst := filepath.Join(in.Root, filepath.FromSlash(rel))

		here, err := fs.Exists(src)
		if err != nil {
			return citypes.ArtifactGetOutput{}, fmt.Errorf("getting %s: %w", rel, err)
		}

		if !here {
			continue
		}

		dir, err := fs.IsDir(src)
		if err != nil {
			return citypes.ArtifactGetOutput{}, fmt.Errorf("getting %s: %w", rel, err)
		}

		if dir {
			files, err := fs.Walk(src)
			if err != nil {
				return citypes.ArtifactGetOutput{}, fmt.Errorf("getting %s: %w", rel, err)
			}

			for _, f := range files {
				if err := restoreFile(fs, filepath.Join(src, filepath.FromSlash(f)), filepath.Join(dst, filepath.FromSlash(f))); err != nil {
					return citypes.ArtifactGetOutput{}, fmt.Errorf("getting %s: %w", rel+"/"+f, err)
				}
			}

			artifact.Location = "file://" + dst
			out.Artifacts = append(out.Artifacts, artifact)

			continue
		}

		if err := restoreFile(fs, src, dst); err != nil {
			return citypes.ArtifactGetOutput{}, fmt.Errorf("getting %s: %w", rel, err)
		}

		artifact.Location = filepath.ToSlash(rel)
		out.Artifacts = append(out.Artifacts, artifact)
	}

	return out, nil
}

func treeTravels(artifact forge.Artifact) bool {
	return artifact.Type == forge.TypeContainer
}

func restoreFile(fs fsadapter.FS, src, dst string) error {
	if err := copyFile(fs, src, dst); err != nil {
		return err
	}

	want, _, err := fsadapter.Digest(src)
	if err != nil {
		return err
	}

	got, _, err := fsadapter.Digest(dst)
	if err != nil {
		return err
	}

	if want != got {
		return ErrArtifactSum
	}

	return nil
}

func copyTree(fs fsadapter.FS, src, dst string) error {
	files, err := fs.Walk(src)
	if err != nil {
		return err
	}

	for _, f := range files {
		if err := copyFile(fs, filepath.Join(src, filepath.FromSlash(f)), filepath.Join(dst, filepath.FromSlash(f))); err != nil {
			return err
		}
	}

	return nil
}

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

	if err := fs.WriteFile(dst, data); err != nil {
		return err
	}

	mode, err := fs.Mode(src)
	if err != nil {
		return err
	}

	return fs.Chmod(dst, mode)
}
