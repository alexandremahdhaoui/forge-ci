package artifactcontroller_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/artifactcontroller"
	"github.com/alexandremahdhaoui/forge-ci/pkg/config"
)

func TestTagNameJoinsAPrefixAndLeavesAnEmptyOneAlone(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "v0.50.0", artifactcontroller.TagName("", "v0.50.0"),
		"no prefix is the default, and the default is plain semver")
	assert.Equal(t, "forge-v0.50.0", artifactcontroller.TagName("forge", "v0.50.0"))
}

// The vocabulary is whatever a team writes, emoji included. Nothing here
// assumes conventional commits.
func TestClassifyScoresAgainstTheVocabularyItIsGiven(t *testing.T) {
	t.Parallel()

	vocab := config.Semantic{
		Major:  []string{"!:", "BREAKING CHANGE", "💥"},
		Minor:  []string{"feat:", "✨"},
		Patch:  []string{"fix:", "perf:", "🐛"},
		Ignore: []string{"docs:", "chore:", "📝"},
	}

	for subject, want := range map[string]artifactcontroller.Level{
		"feat!: the door moved":        artifactcontroller.LevelMajor,
		"feat: a new door":             artifactcontroller.LevelMinor,
		"fix: a small thing":           artifactcontroller.LevelPatch,
		"docs: how to open the door":   artifactcontroller.LevelNone,
		"💥 everything moved":           artifactcontroller.LevelMajor,
		"✨ a new door":                 artifactcontroller.LevelMinor,
		"📝 how to open it":             artifactcontroller.LevelNone,
		"a subject nothing claims":     artifactcontroller.LevelPatch,
		"chore: BREAKING CHANGE in it": artifactcontroller.LevelMajor,
	} {
		assert.Equal(t, want, artifactcontroller.Classify(vocab, "", subject), "subject %q", subject)
	}
}

// Longest match wins, so a vocabulary carrying both "feat:" and "feat!:"
// behaves the way it reads instead of by list order.
func TestClassifyTakesTheLongestMatch(t *testing.T) {
	t.Parallel()

	vocab := config.Semantic{
		Minor: []string{"feat:"},
		Major: []string{"feat!:"},
	}

	assert.Equal(t, artifactcontroller.LevelMajor, artifactcontroller.Classify(vocab, "", "feat!: moved"))
	assert.Equal(t, artifactcontroller.LevelMinor, artifactcontroller.Classify(vocab, "", "feat: added"))
}

// An exhaustive vocabulary is one where an unrecognised subject releases
// nothing, which is what unmatched: ignore is for.
func TestUnmatchedDecidesWhatAnUnknownSubjectScores(t *testing.T) {
	t.Parallel()

	assert.Equal(t, artifactcontroller.LevelPatch,
		artifactcontroller.Classify(config.Semantic{}, "", "anything at all"),
		"an empty unmatched means patch, which is the safe default")

	assert.Equal(t, artifactcontroller.LevelNone,
		artifactcontroller.Classify(config.Semantic{Unmatched: "ignore"}, "", "anything at all"))

	assert.Equal(t, artifactcontroller.LevelMinor,
		artifactcontroller.Classify(config.Semantic{Unmatched: "minor"}, "", "anything at all"))
}

func TestBumpMovesTheLevelItIsGiven(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		previous string
		level    artifactcontroller.Level
		want     string
	}{
		"patch":                   {"v0.49.3", artifactcontroller.LevelPatch, "v0.49.4"},
		"minor resets the patch":  {"v0.49.3", artifactcontroller.LevelMinor, "v0.50.0"},
		"major resets both":       {"v0.49.3", artifactcontroller.LevelMajor, "v1.0.0"},
		"nothing still moves one": {"v0.49.3", artifactcontroller.LevelNone, "v0.49.4"},
		"never released":          {"", artifactcontroller.LevelMajor, "v0.1.0"},
		"a prerelease lands":      {"v1.0.0-rc.1", artifactcontroller.LevelMinor, "v1.0.0"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := artifactcontroller.Bump(tc.previous, tc.level, "")
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A cap is why a factory that is not ready for v1 keeps releasing. The bump
// drops one level and retries rather than refusing.
func TestACapClampsRatherThanRefusing(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		previous string
		level    artifactcontroller.Level
		capTo    string
		want     string
	}{
		"a major under a v0 cap becomes a minor": {"v0.49.0", artifactcontroller.LevelMajor, "v0", "v0.50.0"},
		"a minor under a v0.50 cap becomes a patch": {
			"v0.50.3", artifactcontroller.LevelMinor, "v0.50", "v0.50.4",
		},
		"a major under a v0.50 cap falls all the way": {
			"v0.50.3", artifactcontroller.LevelMajor, "v0.50", "v0.50.4",
		},
		"a cap the bump does not reach changes nothing": {
			"v0.49.0", artifactcontroller.LevelMinor, "v0", "v0.50.0",
		},
		"a major cap above the line lets the major through": {
			"v0.49.0", artifactcontroller.LevelMajor, "v1", "v1.0.0",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := artifactcontroller.Bump(tc.previous, tc.level, tc.capTo)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A line that is already past its cap is a cap somebody lowered after the
// fact. Say so rather than inventing a version below what is released.
func TestALineAlreadyPastItsCapIsRefused(t *testing.T) {
	t.Parallel()

	_, err := artifactcontroller.Bump("v1.2.3", artifactcontroller.LevelPatch, "v0")
	require.ErrorIs(t, err, artifactcontroller.ErrPrevious)

	_, err = artifactcontroller.Bump("v0.51.0", artifactcontroller.LevelPatch, "v0.50")
	require.ErrorIs(t, err, artifactcontroller.ErrPrevious)
}

func TestBumpRefusesAPreviousItCannotRead(t *testing.T) {
	t.Parallel()

	_, err := artifactcontroller.Bump("v1.2", artifactcontroller.LevelPatch, "")
	require.ErrorIs(t, err, artifactcontroller.ErrPrevious)
}

// forge-ci's own commit starts with the self reconcile prefix. No list has
// to name it: it scores patch, and it is never unclaimed. A list that does
// name it wins, because the lists are the team's words.
func TestTheSelfReconcilePrefixScoresPatchUnlessAListNamesIt(t *testing.T) {
	t.Parallel()

	vocab := config.Semantic{Minor: []string{"feat:"}, Ignore: []string{"docs:"}, Unmatched: "error"}
	subject := "forge-ci: self reconcile"

	assert.Equal(t, artifactcontroller.LevelPatch, artifactcontroller.Classify(vocab, "forge-ci:", subject))
	assert.Empty(t, artifactcontroller.Unclaimed(vocab, "forge-ci:", []string{subject}))

	// The prefix is a prefix, not a substring: a subject that mentions it
	// in the middle is not forge-ci's.
	assert.Equal(t, []string{"revert forge-ci: self reconcile"},
		artifactcontroller.Unclaimed(vocab, "forge-ci:", []string{"revert forge-ci: self reconcile"}))

	// An empty prefix names nothing, and an unnamed prefix is unclaimed.
	assert.Equal(t, []string{subject}, artifactcontroller.Unclaimed(vocab, "", []string{subject}))

	// A list that names the prefix wins over the default.
	named := config.Semantic{Minor: []string{"forge-ci:"}}
	assert.Equal(t, artifactcontroller.LevelMinor, artifactcontroller.Classify(named, "forge-ci:", subject))

	ignored := config.Semantic{Ignore: []string{"forge-ci:"}}
	assert.Equal(t, artifactcontroller.LevelNone, artifactcontroller.Classify(ignored, "forge-ci:", subject))
}

func TestBumpRefusesACapItCannotRead(t *testing.T) {
	t.Parallel()

	_, err := artifactcontroller.Bump("v0.1.0", artifactcontroller.LevelPatch, "0.1")
	require.ErrorIs(t, err, artifactcontroller.ErrVersion)
}
