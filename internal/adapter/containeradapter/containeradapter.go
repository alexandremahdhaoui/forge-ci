package containeradapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

var ErrEmptyLayout = errors.New("the image layout holds no manifest")

type Registry interface {
	Push(path string, refs []string, labels map[string]string) error
}

type Remote struct {
	Token citypes.Secret
}

var _ Registry = (*Remote)(nil)

func (r *Remote) options() []remote.Option {
	if r.Token == "" {
		return []remote.Option{remote.WithAuth(authn.Anonymous)}
	}

	return []remote.Option{remote.WithAuth(authn.FromConfig(authn.AuthConfig{
		Username: "token",
		Password: string(r.Token),
	}))}
}

func (r *Remote) Push(path string, refs []string, labels map[string]string) error {
	idx, err := layout.ImageIndexFromPath(path)
	if err != nil {
		return fmt.Errorf("reading the image layout at %s: %w", path, err)
	}

	manifest, err := idx.IndexManifest()
	if err != nil {
		return fmt.Errorf("reading the index at %s: %w", path, err)
	}

	if len(manifest.Manifests) == 0 {
		return fmt.Errorf("%w: %s", ErrEmptyLayout, path)
	}

	labelled, err := label(idx, labels)
	if err != nil {
		return err
	}

	for _, ref := range refs {
		parsed, err := name.ParseReference(ref)
		if err != nil {
			return fmt.Errorf("reading the reference %q: %w", ref, err)
		}

		if err := remote.WriteIndex(parsed, labelled, r.options()...); err != nil {
			return fmt.Errorf("pushing %s: %w", ref, err)
		}
	}

	return nil
}

func (r *Remote) Digest(ctx context.Context, tag string) (string, error) {
	parsed, err := name.NewTag(tag)
	if err != nil {
		return "", fmt.Errorf("reading the tag %q: %w", tag, err)
	}

	descriptor, err := remote.Get(parsed, append(r.options(), remote.WithContext(ctx))...)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", tag, err)
	}

	return descriptor.Digest.String(), nil
}

func label(idx v1.ImageIndex, labels map[string]string) (v1.ImageIndex, error) {
	if len(labels) == 0 {
		return idx, nil
	}

	manifest, err := idx.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("reading the index: %w", err)
	}

	out := mutate.IndexMediaType(v1.ImageIndex(empty.Index), manifest.MediaType)

	for _, descriptor := range manifest.Manifests {
		img, err := idx.Image(descriptor.Digest)
		if err != nil {
			return nil, fmt.Errorf("reading %s from the index: %w", descriptor.Digest, err)
		}

		cf, err := img.ConfigFile()
		if err != nil {
			return nil, fmt.Errorf("reading the config of %s: %w", descriptor.Digest, err)
		}

		cf = cf.DeepCopy()
		if cf.Config.Labels == nil {
			cf.Config.Labels = map[string]string{}
		}

		for k, v := range labels {
			cf.Config.Labels[k] = v
		}

		relabelled, err := mutate.ConfigFile(img, cf)
		if err != nil {
			return nil, fmt.Errorf("labelling %s: %w", descriptor.Digest, err)
		}

		out = mutate.AppendManifests(out, mutate.IndexAddendum{
			Add:        relabelled,
			Descriptor: v1.Descriptor{Platform: descriptor.Platform},
		})
	}

	return out, nil
}
