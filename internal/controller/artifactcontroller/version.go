package artifactcontroller

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
)

// Level is how far a release moves. Ordered, so the highest thing seen in a
// range of commits wins by comparison rather than by a switch nobody updates.
type Level int

const (
	// LevelNone releases nothing. It is what an empty range scores, and what
	// a vocabulary that ignores everything it saw scores.
	LevelNone Level = iota
	LevelPatch
	LevelMinor
	LevelMajor
)

func (l Level) String() string {
	switch l {
	case LevelMajor:
		return "major"
	case LevelMinor:
		return "minor"
	case LevelPatch:
		return "patch"
	default:
		return "none"
	}
}

func levelOf(name string) Level {
	switch name {
	case "major":
		return LevelMajor
	case "minor":
		return LevelMinor
	case "patch":
		return LevelPatch
	default:
		return LevelNone
	}
}

// TagName joins a prefix and a version the one way tags are read back. An
// empty prefix is the whole point of the default: v0.50.0 and nothing else.
func TagName(prefix, version string) string {
	if prefix == "" {
		return version
	}

	return prefix + "-" + version
}

// Classify scores one commit subject against the vocabulary. Longest match
// wins, so a vocabulary carrying both "feat:" and "feat!:" behaves the way it
// reads rather than by list order.
func Classify(vocab config.Semantic, subject string) Level {
	subject = strings.TrimSpace(subject)

	best, bestLen := LevelNone, -1

	consider := func(level Level, tokens []string) {
		for _, token := range tokens {
			if token == "" || !strings.Contains(subject, token) {
				continue
			}

			if len(token) > bestLen {
				best, bestLen = level, len(token)
			}
		}
	}

	// Ignore is considered alongside the rest and scores LevelNone, so a
	// longer ignore token beats a shorter releasing one.
	consider(LevelNone, vocab.Ignore)
	consider(LevelPatch, vocab.Patch)
	consider(LevelMinor, vocab.Minor)
	consider(LevelMajor, vocab.Major)

	if bestLen >= 0 {
		return best
	}

	if vocab.Unmatched == "" {
		return LevelPatch
	}

	return levelOf(vocab.Unmatched)
}

// HighestLevel is what a range of commits asks for: the strongest claim any
// one subject makes. An empty range asks for nothing.
func HighestLevel(vocab config.Semantic, subjects []string) Level {
	out := LevelNone

	for _, s := range subjects {
		if l := Classify(vocab, s); l > out {
			out = l
		}
	}

	return out
}

// Bump moves previous by level, then clamps under cap. A bump the cap forbids
// drops one level and retries, so a factory holding at v0 keeps releasing
// instead of stopping. LevelNone still moves the patch: a green run that
// produced artifacts has something to publish, and a release nobody can name
// is worse than a patch nobody asked for.
func Bump(previous string, level Level, capTo string) (string, error) {
	if strings.TrimSpace(previous) == "" {
		return "v0.1.0", nil
	}

	m := semver.FindStringSubmatch(previous)
	if m == nil {
		return "", fmt.Errorf("%w: %q", ErrPrevious, previous)
	}

	major, err := strconv.Atoi(m[1])
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrPrevious, previous)
	}

	minor, err := strconv.Atoi(m[2])
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrPrevious, previous)
	}

	patch, err := strconv.Atoi(m[3])
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrPrevious, previous)
	}

	// A prerelease is released as the version it was a candidate for, so the
	// bump is already spent.
	if m[4] != "" {
		return fmt.Sprintf("v%d.%d.%d", major, minor, patch), nil
	}

	capMajor, capMinor, capped, err := parseCap(capTo)
	if err != nil {
		return "", err
	}

	if capped && (major > capMajor || (capMajor == major && capMinor >= 0 && minor > capMinor)) {
		return "", fmt.Errorf("%w: %q is already past the cap %q", ErrPrevious, previous, capTo)
	}

	if level < LevelPatch {
		level = LevelPatch
	}

	for {
		next := apply(major, minor, patch, level)

		if !capped || withinCap(next, capMajor, capMinor) {
			return format(next), nil
		}

		if level == LevelPatch {
			// Unreachable while previous is within the cap, because a patch
			// moves neither the major nor the minor. It stands so a future
			// cap shape cannot turn this loop into a silent one.
			return "", fmt.Errorf("%w: no bump fits under the cap %q", ErrPrevious, capTo)
		}

		level--
	}
}

type triple struct{ major, minor, patch int }

func apply(major, minor, patch int, level Level) triple {
	switch level {
	case LevelMajor:
		return triple{major + 1, 0, 0}
	case LevelMinor:
		return triple{major, minor + 1, 0}
	default:
		return triple{major, minor, patch + 1}
	}
}

func format(t triple) string {
	return fmt.Sprintf("v%d.%d.%d", t.major, t.minor, t.patch)
}

// parseCap reads "v0" or "v0.50". The minor is -1 when the cap names none,
// which means every minor under that major is allowed.
func parseCap(capTo string) (major, minor int, ok bool, err error) {
	capTo = strings.TrimSpace(capTo)
	if capTo == "" {
		return 0, 0, false, nil
	}

	rest, cut := strings.CutPrefix(capTo, "v")
	if !cut {
		return 0, 0, false, fmt.Errorf("%w: cap %q", ErrVersion, capTo)
	}

	parts := strings.Split(rest, ".")
	if len(parts) > 2 {
		return 0, 0, false, fmt.Errorf("%w: cap %q", ErrVersion, capTo)
	}

	major, err = strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false, fmt.Errorf("%w: cap %q", ErrVersion, capTo)
	}

	minor = -1

	if len(parts) == 2 {
		minor, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, false, fmt.Errorf("%w: cap %q", ErrVersion, capTo)
		}
	}

	return major, minor, true, nil
}

func withinCap(t triple, capMajor, capMinor int) bool {
	if t.major > capMajor {
		return false
	}

	if capMinor >= 0 && t.major == capMajor && t.minor > capMinor {
		return false
	}

	return true
}
