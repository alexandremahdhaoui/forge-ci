package artifactcontroller

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
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
	Version   string              `json:"version,omitempty"`
	Platforms map[string]Platform `json:"platforms"`
}

// Platform is one concrete binary.
type Platform struct {
	Digest string `json:"digest"`
	Size   int64  `json:"size,omitempty"`
	Asset  string `json:"asset"`
}

// UploadDigest is one upload the adapter hashed: its path, sha256 hex and
// size.
type UploadDigest struct {
	Path   string
	Digest string
	Size   int64
}

// platformSuffix is the `name_os_arch` convention a cross-built binary
// travels under. The segments are whatever the pipeline named; only the
// shape is ours.
var platformSuffix = regexp.MustCompile(`^(.+)_([a-z0-9]+)_([a-z0-9]+)$`)

// BuildIndex renders the distribution index for one release: every upload
// whose file name carries the `name_os_arch` platform suffix becomes a
// tool entry; anything else is a plain asset and stays out of the index.
// The digests are what the adapter measured on the actual files - the
// index never claims a byte nobody hashed.
func BuildIndex(revision, version, createdAt string, release Release, files []UploadDigest) ([]byte, error) {
	if strings.TrimSpace(revision) == "" {
		return nil, ErrRevision
	}

	byName := map[string]*Tool{}
	names := []string{}

	for _, file := range files {
		base := path.Base(file.Path)

		m := platformSuffix.FindStringSubmatch(base)
		if m == nil {
			continue
		}

		if file.Digest == "" {
			return nil, fmt.Errorf("upload %s carries no digest; the index never claims a byte nobody hashed", file.Path)
		}

		name, platform := m[1], m[2]+"/"+m[3]

		tool, ok := byName[name]
		if !ok {
			tool = &Tool{Name: name, Version: version, Platforms: map[string]Platform{}}
			byName[name] = tool
			names = append(names, name)
		}

		if _, dup := tool.Platforms[platform]; dup {
			return nil, fmt.Errorf("tool %s carries two binaries for %s; a distribution is unambiguous", name, platform)
		}

		tool.Platforms[platform] = Platform{
			Digest: "sha256:" + file.Digest,
			Size:   file.Size,
			Asset:  base,
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
