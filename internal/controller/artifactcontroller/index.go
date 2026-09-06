package artifactcontroller

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// The distribution index maps one proven revision to the digest of every
// binary the release carries, per platform. forge-factory consumes it into
// the store; the JSON here is that contract's producing half. forge-ci
// knows nothing about what the binaries are - names, platforms and digests
// arrive from the pipeline's own uploads.

// Index is one revision's distribution.
type Index struct {
	Revision  string  `json:"revision"`
	CreatedAt string  `json:"createdAt,omitempty"`
	Release   Release `json:"release,omitempty"`
	Tools     []Tool  `json:"tools"`
}

// Release names where the aggregated release lives.
type Release struct {
	Repo string `json:"repo,omitempty"`
	Tag  string `json:"tag,omitempty"`
}

// Tool is one distributed binary across its platforms.
type Tool struct {
	Name      string              `json:"name"`
	Platforms map[string]Platform `json:"platforms"`
}

// Platform is one concrete binary.
type Platform struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size,omitempty"`
	Asset  string `json:"asset"`
}

// UploadDigest is one upload the adapter hashed: the upload with its sha256
// hex and size.
type UploadDigest struct {
	Upload
	Digest string
	Size   int64
}

// BuildIndex renders the distribution index for one release: every upload
// whose record named a tool and a platform becomes a tool entry; a plain
// asset carries no name and stays out of the index. The digests are what
// the adapter measured on the actual files - the index never claims a byte
// nobody hashed.
//
// BuildIndex writes the index a consumer verifies against: the revision, the
// release it travels in, and every binary's digest.
//
// It carries no per-tool version. The pipeline tags each member on its own
// line, and a tool's binary name says nothing about which member built it -
// so the only version available here was the release's own, stamped onto
// every tool alike. An index saying forge is v0.1.5 when forge was tagged
// v0.44.4 is worse than an index that does not say.
func BuildIndex(revision, createdAt string, release Release, files []UploadDigest) ([]byte, error) {
	if strings.TrimSpace(revision) == "" {
		return nil, ErrRevision
	}

	byName := map[string]*Tool{}
	names := []string{}

	for _, file := range files {
		if file.Name == "" || file.OS == "" || file.Arch == "" {
			continue
		}

		if file.Digest == "" {
			return nil, fmt.Errorf("upload %s carries no digest; the index never claims a byte nobody hashed", file.Path)
		}

		name, platform := file.Name, file.OS+"/"+file.Arch

		tool, ok := byName[name]
		if !ok {
			tool = &Tool{Name: name, Platforms: map[string]Platform{}}
			byName[name] = tool
			names = append(names, name)
		}

		if _, dup := tool.Platforms[platform]; dup {
			return nil, fmt.Errorf("tool %s carries two binaries for %s; a distribution is unambiguous", name, platform)
		}

		tool.Platforms[platform] = Platform{
			Digest: "sha256:" + file.Digest,
			Size:   file.Size,
			Asset:  file.Asset,
		}
	}

	sort.Strings(names)

	index := Index{Revision: revision, CreatedAt: createdAt, Release: release, Tools: []Tool{}}
	for _, name := range names {
		index.Tools = append(index.Tools, *byName[name])
	}

	raw, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding the distribution index: %w", err)
	}

	return append(raw, '\n'), nil
}
