package forgeadapter_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/forgeadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/adapter/fsadapter"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/fsadaptermock"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

var errBoom = errors.New("boom")

const store = `
artifacts:
  - name: forge-ci
    type: binary
    location: build/bin/forge-ci
    version: abc123
testReports:
  unit:
    id: u1
    stage: unit
    status: passed
    testStats:
      total: 37
    coverage:
      percentage: 96.4
  lint:
    id: l1
    stage: lint
    status: passed
`

func writeStore(t *testing.T, body string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, forgeadapter.DefaultStorePath)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o750))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

	return dir
}

func TestArtifactsAndReportsAreHarvested(t *testing.T) {
	got, err := forgeadapter.New(fsadapter.New()).Harvest(writeStore(t, store), time.Time{})
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Len(t, got.Artifacts, 1)
	require.Equal(t, "forge-ci", got.Artifacts[0].Name)
	require.Len(t, got.TestReports, 2)
	require.Equal(t, "lint", got.TestReports[0].Stage)
	require.Equal(t, "unit", got.TestReports[1].Stage)
	require.InDelta(t, 96.4, got.TestReports[1].Coverage.Percentage, 0.01)
}

func TestNoStoreIsNotAnError(t *testing.T) {
	got, err := forgeadapter.New(fsadapter.New()).Harvest(t.TempDir(), time.Time{})
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestABrokenStoreIsReported(t *testing.T) {
	_, err := forgeadapter.New(fsadapter.New()).Harvest(writeStore(t, "\tnot: [valid"), time.Time{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "parsing the artifact store")
}

func TestANilReportIsSkipped(t *testing.T) {
	got, err := forgeadapter.New(fsadapter.New()).Harvest(writeStore(t, "testReports:\n  unit: null\n"), time.Time{})
	require.NoError(t, err)
	require.Empty(t, got.TestReports)
}

func TestFilesystemFailuresAreReported(t *testing.T) {
	fs := fsadaptermock.NewMockFS(t)
	fs.EXPECT().Exists(mock.Anything).Return(false, errBoom).Once()

	_, err := forgeadapter.New(fs).Harvest("/x", time.Time{})
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), "looking for the artifact store")

	fs2 := fsadaptermock.NewMockFS(t)
	fs2.EXPECT().Exists(mock.Anything).Return(true, nil).Once()
	fs2.EXPECT().ReadFile(mock.Anything).Return(nil, errBoom).Once()

	_, err = forgeadapter.New(fs2).Harvest("/x", time.Time{})
	require.ErrorIs(t, err, errBoom)
	require.Contains(t, err.Error(), "reading the artifact store")
}

const withHistory = `
testReports:
  old-run:
    id: o1
    stage: unit
    status: passed
    startTime: 2020-01-01T00:00:00Z
  this-run:
    id: n1
    stage: unit
    status: passed
    startTime: 2030-01-01T00:00:00Z
`

func TestReportsFromEarlierRunsAreDropped(t *testing.T) {
	since := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	got, err := forgeadapter.New(fsadapter.New()).Harvest(writeStore(t, withHistory), since)
	require.NoError(t, err)
	require.Len(t, got.TestReports, 1,
		"the artifact store keeps every past run, so a harvest must take only this one")
	require.Equal(t, "n1", got.TestReports[0].ID)
}

func TestAZeroSinceTakesEverything(t *testing.T) {
	got, err := forgeadapter.New(fsadapter.New()).Harvest(writeStore(t, withHistory), time.Time{})
	require.NoError(t, err)
	require.Len(t, got.TestReports, 2)
}
