package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/execadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/gitadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/githubadapter"
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

			out, err := publish(ctx, ctrl, gitadapter.New(execadapter.New()), apiFor(in.Spec), citypes.ArtifactInput{
				Revision:  in.Revision,
				Version:   in.Version,
				TagPrefix: in.TagPrefix,
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
		out = append(out, Resource{
			Kind: r.Kind, Name: r.Name, BootstrapOnly: r.BootstrapOnly, Spec: r.Spec,
		})
	}

	return out
}

// apiFor builds the GitHub client from the spec: the token the spec names
// (default GITHUB_TOKEN) against spec.apiBaseURL (default the public API).
//
// There is no gh path. gh reads whatever GH_TOKEN or GITHUB_TOKEN the host
// happens to carry, so it cannot be pointed at the credential the pipeline
// declares, and it is not in every image a job runs in. Both are the same
// defect: a dependency on the ambient environment that is invisible until
// the environment changes.
func apiFor(spec map[string]any) githubadapter.API {
	tokenEnv, _ := spec["tokenEnv"].(string)
	if tokenEnv == "" {
		tokenEnv = "GITHUB_TOKEN"
	}

	base, _ := spec["apiBaseURL"].(string)

	return githubadapter.New(nil, base, os.Getenv(tokenEnv))
}

// publish carries out what the controller decided. The decision is not made
// here, so what gets released is testable without a network.
func publish(
	ctx context.Context,
	ctrl *artifactcontroller.Controller,
	git gitadapter.Git,
	api githubadapter.API,
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
	//
	// The pipeline names it. Reading it off a checkout's origin remote is one
	// more thing that works until the checkout is not the one anybody meant,
	// and every other engine here is told what it acts on.
	home, _ := in.Spec["repo"].(string)
	if home == "" {
		return citypes.ArtifactOutput{Reason: "spec.repo names no repo to create the release in"},
			fmt.Errorf("releasing %s: spec.repo is required", plan.Version)
	}

	out := citypes.ArtifactOutput{Tagged: []string{}}

	// Every member carries the SAME tag, and this engine computes no part of
	// it. The version arrives decided; deriving one here per member is what
	// put every repo on a version line of its own, so the arithmetic lives
	// in the core and nowhere else.
	for _, tag := range plan.Tags {
		dir := root + "/" + tag.Repo

		// The remote is the authority, not the local tag list: a CI checkout
		// is a fresh clone that carries no tags, so reading only the local
		// list re-created an already-published tag and the push was rejected
		// by the only party that knew. A remote that cannot be read is an
		// error, never "absent" - falling through would repeat that mistake.
		hasRemote, err := git.HasRemote(ctx, dir)
		if err != nil {
			return out, fmt.Errorf("releasing %s: %w", tag.Repo, err)
		}

		if hasRemote {
			at, found, err := git.RemoteTagAt(ctx, dir, plan.TagName)
			if err != nil {
				return out, fmt.Errorf("releasing %s: %w", tag.Repo, err)
			}

			if found {
				// Same commit: this member is already released at this
				// version and a re-run stacks nothing on it. Converged.
				if at == tag.SHA {
					continue
				}

				// Different commit: the version is being re-pointed, which
				// changes what a consumer already pinned. Refuse it and
				// name both commits rather than moving the tag.
				return out, fmt.Errorf(
					"releasing %s: %s already points at %s, not %s, and a tag is never moved",
					tag.Repo, plan.TagName, at, tag.SHA)
			}
		}

		// Tag re-decides on the union of local and remote: a leftover local
		// tag at the same commit is an interrupted run, and the push is what
		// finishes it; one at a different commit is refused there.
		if err := git.Tag(ctx, dir, plan.TagName, tag.SHA); err != nil {
			return out, fmt.Errorf("releasing %s: %w", plan.TagName, err)
		}

		out.Tagged = append(out.Tagged, tag.Repo)
	}

	// A revision already fully published converges rather than re-releasing:
	// every member's tag was already on its remote and the release exists,
	// so there is no work left and no build output to demand - a re-run's
	// fresh clone has none, its substages were skipped as already passed.
	// An interrupted run - tags pushed, release missing - falls through and
	// finishes the job.
	if len(plan.Tags) > 0 && len(out.Tagged) == 0 {
		existing, found, err := api.ReleaseByTag(ctx, home, plan.TagName)
		if err != nil {
			return out, fmt.Errorf("releasing %s: %w", plan.Version, err)
		}

		if found {
			out.URL = existing.HTMLURL
			out.Reason = plan.Version + " is already released; converged"

			return out, nil
		}
	}

	uploads, err := expandAssets(root, plan.Uploads, in.Spec)
	if err != nil {
		return out, fmt.Errorf("releasing %s: %w", plan.Version, err)
	}

	assets, err := stageAssets(root, plan, uploads, in.Revision)
	if err != nil {
		return out, fmt.Errorf("releasing %s: %w", plan.Version, err)
	}

	// One aggregated release per version: every binary the runs built plus
	// the index that pins their digests, under the same tag the members
	// carry - the workspace is one thing, built together and released
	// together, under one number.
	release, err := api.CreateRelease(ctx, home, plan.TagName)
	if err != nil {
		return out, fmt.Errorf("releasing %s: %w", plan.Version, err)
	}

	// The upload URL comes from the release itself and is on a different
	// host from the API base, which is why the client sends to a full URL
	// rather than joining a path onto the base.
	for _, asset := range assets {
		if err := api.UploadAsset(ctx, release.UploadURL, asset); err != nil {
			return out, fmt.Errorf("releasing %s: %w", plan.Version, err)
		}
	}

	out.Published = true
	out.URL = release.HTMLURL

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

// resolveUpload places one upload against the root. An artifact record may
// already carry an absolute path, in which case the root says nothing about
// it.
func resolveUpload(root, upload string) string {
	if filepath.IsAbs(upload) {
		return upload
	}

	return filepath.Join(root, upload)
}

// stageAssets resolves the uploads against the root, digests each one, and
// writes the distribution index beside them in a staging dir. The index is
// built from the measured digests, never from claims.
func stageAssets(root string, plan artifactcontroller.Plan, uploads []string, revision string) ([]string, error) {
	assets := make([]string, 0, len(uploads)+1)
	digests := make([]artifactcontroller.UploadDigest, 0, len(uploads))

	for _, upload := range uploads {
		// Absolute, not merely joined. An asset is read from disk by this
		// process, whose working directory is nobody's to promise, so a
		// path that is still relative here is read against the wrong
		// directory and the release fails on a file that is sitting there.
		// Joining is not enough on its own: filepath.Join(".", x) is a
		// clean relative path and looks correct right up to the point
		// something else supplies the working directory.
		//
		// The driver absolutises the root already. This does not trust
		// that, because the cost is one syscall and the failure it
		// prevents lands after the tags are cut.
		path, err := filepath.Abs(resolveUpload(root, upload))
		if err != nil {
			return nil, fmt.Errorf("resolving upload %s: %w", upload, err)
		}

		digest, size, err := fsadapter.Digest(path)
		if err != nil {
			return nil, err
		}

		assets = append(assets, path)
		digests = append(digests, artifactcontroller.UploadDigest{Path: path, Digest: digest, Size: size})
	}

	index, err := artifactcontroller.BuildIndex(
		revision, time.Now().UTC().Format(time.RFC3339),
		artifactcontroller.Release{Tag: plan.TagName}, digests)
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
