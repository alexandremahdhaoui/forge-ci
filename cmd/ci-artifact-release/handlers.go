package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/releaseadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactcontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
	"github.com/alexandremahdhaoui/forge/pkg/forge"
)

// NewHandlers wires the release into the generated tool surface.
//
// A generated wire type is not an internal type, so the mapping happens here.
// It is explicit field by field, except for the artifacts: forge owns that
// record, its schema lives in forge, and a copy here would drift from what
// forge emits. Those travel as the JSON they already are.
func NewHandlers() Handlers {
	ctrl := artifactcontroller.New()

	return Handlers{
		Declare: func(_ context.Context, in DeclareInput) (*DeclareOutput, error) {
			out, err := ctrl.Declare(in.Spec)
			if err != nil {
				return nil, err
			}

			return &DeclareOutput{Resources: fromResources(out.Resources)}, nil
		},
		Publish: func(ctx context.Context, in ArtifactInput) (*ArtifactOutput, error) {
			artifacts, err := toArtifacts(in.Artifacts)
			if err != nil {
				return nil, err
			}

			out, err := publish(ctx, ctrl, publisherFor(in.Spec), citypes.ArtifactInput{
				Revision:  in.Revision,
				Version:   in.Version,
				Repos:     in.Repos,
				Artifacts: artifacts,
				Spec:      in.Spec,
			})
			if err != nil {
				return nil, err
			}

			return &ArtifactOutput{
				Published: out.Published,
				Url:       out.URL,
				Reason:    out.Reason,
				Tagged:    out.Tagged,
			}, nil
		},
	}
}

// toArtifacts reads forge's own records off the wire. Unmarshalling is the
// mapping here on purpose: the shape is whatever forge emits, and describing
// it again is what we are avoiding.
func toArtifacts(in []ForgeArtifact) ([]forge.Artifact, error) {
	out := make([]forge.Artifact, 0, len(in))

	for _, a := range in {
		raw, err := json.Marshal(a)
		if err != nil {
			return nil, err
		}

		var artifact forge.Artifact

		if err := json.Unmarshal(raw, &artifact); err != nil {
			return nil, err
		}

		out = append(out, artifact)
	}

	return out, nil
}

func fromResources(in []citypes.Resource) []Resource {
	out := make([]Resource, 0, len(in))

	for _, r := range in {
		out = append(out, Resource{Kind: r.Kind, Name: r.Name, Spec: r.Spec})
	}

	return out
}

// publisherFor picks how to reach GitHub: the gh CLI when the host carries
// it, else the REST API directly with the token the spec names (default
// GITHUB_TOKEN) against spec.apiBaseURL (default the public API).
func publisherFor(spec map[string]any) releaseadapter.Publisher {
	if _, err := exec.LookPath("gh"); err == nil {
		return releaseadapter.New(execadapter.New())
	}

	tokenEnv, _ := spec["tokenEnv"].(string)
	if tokenEnv == "" {
		tokenEnv = "GITHUB_TOKEN"
	}

	base, _ := spec["apiBaseURL"].(string)

	return releaseadapter.NewAPI(execadapter.New(), base, os.Getenv(tokenEnv))
}

// publish carries out what the controller decided. The decision is not made
// here, so what gets released is testable without a network.
func publish(
	ctx context.Context,
	ctrl *artifactcontroller.Controller,
	publisher releaseadapter.Publisher,
	in citypes.ArtifactInput,
) (citypes.ArtifactOutput, error) {
	plan, err := ctrl.Plan(in)
	if err != nil {
		return citypes.ArtifactOutput{Reason: err.Error()}, err
	}

	root, _ := in.Spec["root"].(string)
	if root == "" {
		root = "."
	}

	// The release is created in a repo, and the workspace root is not one. It
	// belongs in the repo that holds the workspace files, because that is what
	// a release of the whole workspace is a release of.
	home, _ := in.Spec["releaseIn"].(string)
	if home == "" {
		return citypes.ArtifactOutput{Reason: "spec.releaseIn names no repo to create the release in"},
			fmt.Errorf("releasing %s: spec.releaseIn is required", plan.Version)
	}

	out := citypes.ArtifactOutput{Tagged: []string{}}

	// Each member is tagged on ITS OWN version line: one shared number
	// breaks the moment a member carries history from before this
	// pipeline existed. A commit some tag already points at is a member
	// already released - a re-run stacks nothing on it.
	for _, tag := range plan.Tags {
		dir := root + "/" + tag.Repo

		released, err := publisher.TaggedAt(ctx, dir, tag.SHA)
		if err != nil {
			return out, fmt.Errorf("releasing %s: %w", tag.Repo, err)
		}

		if released {
			continue
		}

		previous, err := publisher.LatestTag(ctx, dir)
		if err != nil {
			return out, fmt.Errorf("releasing %s: %w", tag.Repo, err)
		}

		version, err := artifactcontroller.NextVersion(previous)
		if err != nil {
			return out, fmt.Errorf("releasing %s: %w", tag.Repo, err)
		}

		if err := publisher.Tag(ctx, dir, version, tag.SHA); err != nil {
			return out, fmt.Errorf("releasing %s: %w", version, err)
		}

		out.Tagged = append(out.Tagged, tag.Repo)
	}

	uploads, err := expandAssets(root, plan.Uploads, in.Spec)
	if err != nil {
		return out, fmt.Errorf("releasing %s: %w", plan.Version, err)
	}

	assets, err := stageAssets(root, plan, uploads, in.Revision)
	if err != nil {
		return out, fmt.Errorf("releasing %s: %w", plan.Version, err)
	}

	// One aggregated release per revision: every binary the runs built plus
	// the index that pins their digests, under the dist tag - the
	// register/revision is one thing, built together and released together.
	url, err := publisher.Release(ctx, filepath.Join(root, home), plan.DistTag, assets)
	if err != nil {
		return out, fmt.Errorf("releasing %s: %w", plan.Version, err)
	}

	out.Published = true
	out.URL = url

	return out, nil
}

// expandAssets widens the uploads with spec.assets: root-relative globs the
// instance names, the door for cross-built binaries no artifact record
// carries. A glob that matches nothing is an error - a distribution that
// silently shrinks is worse than one that fails.
func expandAssets(root string, uploads []string, spec map[string]any) ([]string, error) {
	raw, _ := spec["assets"].([]any)

	out := append([]string{}, uploads...)
	seen := map[string]bool{}

	for _, path := range out {
		seen[path] = true
	}

	for _, entry := range raw {
		pattern, _ := entry.(string)
		if pattern == "" {
			continue
		}

		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			return nil, fmt.Errorf("expanding assets glob %q: %w", pattern, err)
		}

		if len(matches) == 0 {
			return nil, fmt.Errorf("assets glob %q matches nothing; a distribution never shrinks silently", pattern)
		}

		for _, match := range matches {
			rel, err := filepath.Rel(root, match)
			if err != nil {
				rel = match
			}

			if seen[rel] {
				continue
			}

			seen[rel] = true

			out = append(out, rel)
		}
	}

	sort.Strings(out)

	return out, nil
}

// stageAssets resolves the uploads against the root, digests each one, and
// writes the distribution index beside them in a staging dir. The index is
// built from the measured digests, never from claims.
func stageAssets(root string, plan artifactcontroller.Plan, uploads []string, revision string) ([]string, error) {
	assets := make([]string, 0, len(uploads)+1)
	digests := make([]artifactcontroller.UploadDigest, 0, len(uploads))

	for _, upload := range uploads {
		path := upload
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}

		digest, size, err := releaseadapter.DigestFile(path)
		if err != nil {
			return nil, err
		}

		assets = append(assets, path)
		digests = append(digests, artifactcontroller.UploadDigest{Path: path, Digest: digest, Size: size})
	}

	index, err := artifactcontroller.BuildIndex(
		revision, time.Now().UTC().Format(time.RFC3339),
		artifactcontroller.Release{Tag: plan.DistTag}, digests)
	if err != nil {
		return nil, err
	}

	staging, err := os.MkdirTemp("", "ci-artifact-release-*")
	if err != nil {
		return nil, fmt.Errorf("staging the index: %w", err)
	}

	indexPath := filepath.Join(staging, "index.json")
	if err := os.WriteFile(indexPath, index, 0o600); err != nil {
		return nil, fmt.Errorf("staging the index: %w", err)
	}

	return append(assets, indexPath), nil
}
