package triggercontroller_test

import (
	"context"
	"errors"
	"testing"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/triggercontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/gitadaptermock"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

func TestFirstLookAlwaysCountsAsChanged(t *testing.T) {
	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HeadSHA(mock.Anything, "/repo").Return("abc", nil).Once()
	git.EXPECT().Dirty(mock.Anything, "/repo").Return(false, nil).Once()

	out, err := triggercontroller.New(git).Poll(context.Background(), map[string]any{
		"watch": []any{"/repo"},
	})
	require.NoError(t, err)
	require.True(t, out.Changed)
	require.Equal(t, "first look at the watched repos", out.Reason)
	require.NotEmpty(t, out.Fingerprint)
}

func TestTheSameStateIsNotAChange(t *testing.T) {
	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HeadSHA(mock.Anything, "/repo").Return("abc", nil).Twice()
	git.EXPECT().Dirty(mock.Anything, "/repo").Return(false, nil).Twice()

	c := triggercontroller.New(git)

	first, err := c.Poll(context.Background(), map[string]any{"watch": []any{"/repo"}})
	require.NoError(t, err)

	second, err := c.Poll(context.Background(), map[string]any{
		"watch": []any{"/repo"}, "previous": first.Fingerprint,
	})
	require.NoError(t, err)
	require.False(t, second.Changed)
	require.Equal(t, first.Fingerprint, second.Fingerprint)
}

func TestANewCommitIsAChange(t *testing.T) {
	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HeadSHA(mock.Anything, "/repo").Return("abc", nil).Once()
	git.EXPECT().Dirty(mock.Anything, "/repo").Return(false, nil).Once()

	c := triggercontroller.New(git)

	first, err := c.Poll(context.Background(), map[string]any{"watch": []any{"/repo"}})
	require.NoError(t, err)

	git.EXPECT().HeadSHA(mock.Anything, "/repo").Return("def", nil).Once()
	git.EXPECT().Dirty(mock.Anything, "/repo").Return(false, nil).Once()

	second, err := c.Poll(context.Background(), map[string]any{
		"watch": []any{"/repo"}, "previous": first.Fingerprint,
	})
	require.NoError(t, err)
	require.True(t, second.Changed)
	require.Equal(t, "the watched repos moved", second.Reason)
}

func TestAnUncommittedEditIsAChange(t *testing.T) {
	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HeadSHA(mock.Anything, "/repo").Return("abc", nil).Twice()
	git.EXPECT().Dirty(mock.Anything, "/repo").Return(false, nil).Once()

	c := triggercontroller.New(git)

	first, err := c.Poll(context.Background(), map[string]any{"watch": []any{"/repo"}})
	require.NoError(t, err)

	git.EXPECT().Dirty(mock.Anything, "/repo").Return(true, nil).Once()

	second, err := c.Poll(context.Background(), map[string]any{
		"watch": []any{"/repo"}, "previous": first.Fingerprint,
	})
	require.NoError(t, err)
	require.True(t, second.Changed)
}

func TestTheFingerprintIsOrderIndependent(t *testing.T) {
	build := func(order []any) string {
		git := gitadaptermock.NewMockGit(t)
		git.EXPECT().HeadSHA(mock.Anything, mock.Anything).Return("abc", nil).Times(2)
		git.EXPECT().Dirty(mock.Anything, mock.Anything).Return(false, nil).Times(2)

		out, err := triggercontroller.New(git).Poll(context.Background(), map[string]any{"watch": order})
		require.NoError(t, err)

		return out.Fingerprint
	}

	require.Equal(t, build([]any{"/a", "/b"}), build([]any{"/b", "/a"}))
}

func TestWatchIsRequired(t *testing.T) {
	c := triggercontroller.New(gitadaptermock.NewMockGit(t))

	for _, spec := range []map[string]any{
		{},
		{"watch": []any{}},
		{"watch": []any{"  "}},
		{"watch": "not-a-list"},
	} {
		_, err := c.Poll(context.Background(), spec)
		require.ErrorIs(t, err, triggercontroller.ErrWatch)
	}
}

func TestATypedStringSliceIsAccepted(t *testing.T) {
	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HeadSHA(mock.Anything, "/repo").Return("abc", nil).Once()
	git.EXPECT().Dirty(mock.Anything, "/repo").Return(false, nil).Once()

	out, err := triggercontroller.New(git).Poll(context.Background(), map[string]any{
		"watch": []string{"/repo"},
	})
	require.NoError(t, err)
	require.True(t, out.Changed)
}

func TestGitFailuresNameTheDirectory(t *testing.T) {
	git := gitadaptermock.NewMockGit(t)
	git.EXPECT().HeadSHA(mock.Anything, "/repo").Return("", errBoom).Once()

	_, err := triggercontroller.New(git).Poll(context.Background(), map[string]any{"watch": []any{"/repo"}})
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), "watching /repo")

	git2 := gitadaptermock.NewMockGit(t)
	git2.EXPECT().HeadSHA(mock.Anything, "/repo").Return("abc", nil).Once()
	git2.EXPECT().Dirty(mock.Anything, "/repo").Return(false, errBoom).Once()

	_, err = triggercontroller.New(git2).Poll(context.Background(), map[string]any{"watch": []any{"/repo"}})
	require.ErrorIs(t, err, errBoom)
}

func TestTriggerDeclaresNoResources(t *testing.T) {
	out, err := triggercontroller.New(nil).Declare(nil)
	require.NoError(t, err)
	require.Empty(t, out.Resources)
}
