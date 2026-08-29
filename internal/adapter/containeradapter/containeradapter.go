// Package containeradapter pushes an assembled image to a registry. It is the
// only thing here that reaches the outside world, and it holds no decision
// about what to publish.
//
// go-containerregistry speaks the registry HTTP API directly, so there is no
// daemon, no CLI and no login step: the auth handshake is part of the push.
package containeradapter

import (
	"errors"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// ErrEmptyLayout means the directory holds no manifest, so the build that was
// supposed to write it did not.
var ErrEmptyLayout = errors.New("the image layout holds no manifest")

// Registry publishes an image somewhere people can pull it.
type Registry interface {
	// Push sends the index the layout at path holds, under every ref. One
	// index and several names, so a moving tag and the version tag point at
	// the same bytes rather than at two builds that happen to match.
	Push(path string, refs []string, labels map[string]string) error
}

// Remote is the real registry. Token is the credential; GitHub Actions
// injects secrets.GITHUB_TOKEN, which is why there is no secret to create,
// seal or rotate.
type Remote struct {
	Token string
}

var _ Registry = (*Remote)(nil)

// options carries the credential. With no token the requests go out
// anonymous, which is enough to read a public registry and not enough to
// write: the docker config file is deliberately not consulted, because an
// engine that silently picks up whatever credential the machine happens to
// hold publishes to somewhere nobody named.
func (r *Remote) options() []remote.Option {
	if r.Token == "" {
		return []remote.Option{remote.WithAuth(authn.Anonymous)}
	}

	return []remote.Option{remote.WithAuth(authn.FromConfig(authn.AuthConfig{
		Username: "token",
		Password: r.Token,
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

// label writes the release's own labels into every architecture's config, so
// somebody holding only the image can find the revision it was built from.
// The build wrote what it knew; this adds what only the release knows.
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
