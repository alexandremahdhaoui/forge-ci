package helmadapter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"helm.sh/helm/v4/pkg/action"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/release/common"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage"
	"helm.sh/helm/v4/pkg/storage/driver"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	theNamespace = "flux-system"
	theName      = "cilium"
	theChart     = "cilium"
	theVersion   = "1.18.2"

	memoryStorage = "memory"
)

func hermetic(t *testing.T, storage string) Releases {
	t.Helper()

	kubeconfig := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeconfig, []byte(`apiVersion: v1
kind: Config
clusters:
  - name: here
    cluster:
      server: https://127.0.0.1:1
contexts:
  - name: here
    context:
      cluster: here
current-context: here
`), 0o600))

	t.Setenv("KUBECONFIG", kubeconfig)
	t.Setenv(storageVariable, storage)

	releases, err := New()
	require.NoError(t, err)

	return releases
}

func liveRelease(chart *chartv2.Chart, status common.Status) *releasev1.Release {
	return &releasev1.Release{
		Name:      theName,
		Namespace: theNamespace,
		Version:   1,
		Chart:     chart,
		Info:      &releasev1.Info{Status: status},
	}
}

func holding(t *testing.T, stored ...*releasev1.Release) Releases {
	t.Helper()

	memory := driver.NewMemory()
	memory.SetNamespace(theNamespace)
	store := storage.Init(memory)

	for _, one := range stored {
		require.NoError(t, store.Create(one))
	}

	return Releases{open: func(string) (*action.Configuration, error) {
		return &action.Configuration{Releases: store}, nil
	}}
}

func TestAReleaseHelmStorageHoldsComesBackFoundAndDescribed(t *testing.T) {
	stored := liveRelease(
		&chartv2.Chart{Metadata: &chartv2.Metadata{Name: theChart, Version: theVersion}},
		common.StatusDeployed)

	described, found, err := holding(t, stored).Release(context.Background(), theNamespace, theName)

	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, citypes.HelmRelease{
		Namespace: theNamespace,
		Name:      theName,
		Chart:     theChart,
		Version:   theVersion,
		Status:    common.StatusDeployed.String(),
	}, described)
}

func TestAReleaseHelmStorageHoldsWithoutAChartRefusesInsteadOfComingBackFound(t *testing.T) {
	_, found, err := holding(t, liveRelease(nil, common.StatusDeployed)).
		Release(context.Background(), theNamespace, theName)

	require.Error(t, err)
	require.False(t, found)
	require.Contains(t, err.Error(), "the stored release carries no chart")
}

func TestAReleaseHelmStorageDoesNotHoldIsReportedAsNotFoundAndNeverAsAnError(t *testing.T) {
	_, found, err := hermetic(t, memoryStorage).Release(context.Background(), theNamespace, theName)

	require.NoError(t, err)
	require.False(t, found)
}

func TestAStorageDriverHelmDoesNotKnowRefusesAndNamesTheNamespace(t *testing.T) {
	_, _, err := hermetic(t, "papyrus").Release(context.Background(), theNamespace, theName)

	require.Error(t, err)
	require.Contains(t, err.Error(), "opening helm storage in namespace "+theNamespace)
}

func TestAnInstallIntoAStorageDriverHelmDoesNotKnowRefusesBeforeAnyChartIsFetched(t *testing.T) {
	err := hermetic(t, "papyrus").InstallRelease(context.Background(), citypes.HelmRelease{
		Namespace: theNamespace, Name: theName, Chart: theChart, Version: theVersion,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(), "opening helm storage in namespace "+theNamespace)
}

func TestAChartThatCannotBeLocatedRefusesNamingTheChartTheVersionAndTheRelease(t *testing.T) {
	repository := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no index here", http.StatusNotFound)
	}))
	defer repository.Close()

	err := hermetic(t, memoryStorage).InstallRelease(context.Background(), citypes.HelmRelease{
		Namespace:  theNamespace,
		Name:       theName,
		Chart:      theChart,
		Version:    theVersion,
		Repository: repository.URL,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(),
		"locating chart "+theChart+" "+theVersion+" for release "+theNamespace+"/"+theName)
}

func TestAnOCIRepositoryBecomesOneChartReferenceAndCarriesNoRepositoryURL(t *testing.T) {
	reference, repositoryURL := chartReference(citypes.HelmRelease{
		Chart: theChart, Repository: "oci://ghcr.io/owner/charts/",
	})

	require.Equal(t, "oci://ghcr.io/owner/charts/"+theChart, reference)
	require.Empty(t, repositoryURL)
}

func TestAnHTTPRepositoryStaysTheRepositoryURLAndTheChartStaysItsOwnName(t *testing.T) {
	reference, repositoryURL := chartReference(citypes.HelmRelease{
		Chart: theChart, Repository: "https://charts.example.com",
	})

	require.Equal(t, theChart, reference)
	require.Equal(t, "https://charts.example.com", repositoryURL)
}

func TestAStoredReleaseIsDescribedByItsNamespaceNameChartVersionAndStatus(t *testing.T) {
	described, err := describe(liveRelease(
		&chartv2.Chart{Metadata: &chartv2.Metadata{Name: theChart, Version: theVersion}},
		common.StatusDeployed), theNamespace, theName)

	require.NoError(t, err)
	require.Equal(t, citypes.HelmRelease{
		Namespace: theNamespace,
		Name:      theName,
		Chart:     theChart,
		Version:   theVersion,
		Status:    common.StatusDeployed.String(),
	}, described)
}

func TestAStoredReleaseHeldInAFailedStatusIsDescribedWithThatStatusAndNeverCorrected(t *testing.T) {
	described, err := describe(liveRelease(
		&chartv2.Chart{Metadata: &chartv2.Metadata{Name: theChart, Version: theVersion}},
		common.StatusFailed), theNamespace, theName)

	require.NoError(t, err)
	require.Equal(t, common.StatusFailed.String(), described.Status)
}

func TestAStoredReleaseWhoseChartMetadataNamesNoVersionRefusesByName(t *testing.T) {
	_, err := describe(liveRelease(
		&chartv2.Chart{Metadata: &chartv2.Metadata{Name: theChart}},
		common.StatusDeployed), theNamespace, theName)

	require.Error(t, err)
	require.Contains(t, err.Error(), "chart "+theChart+" carries no version in its metadata")
}

func TestAStoredRecordThatIsNoReleaseRefusesNamingTheRelease(t *testing.T) {
	_, err := describe("not a release", theNamespace, theName)

	require.Error(t, err)
	require.Contains(t, err.Error(),
		"reading release "+theNamespace+"/"+theName+" from helm storage")
}

func TestAStoredReleaseCarryingNoChartRefusesNamingTheRelease(t *testing.T) {
	_, err := describe(liveRelease(nil, common.StatusDeployed), theNamespace, theName)

	require.Error(t, err)
	require.Contains(t, err.Error(),
		"reading the chart of release "+theNamespace+"/"+theName+
			": the stored release carries no chart")
}

func TestAStoredChartCarryingNoMetadataRefusesByNameInsteadOfPanicking(t *testing.T) {
	_, err := describe(liveRelease(&chartv2.Chart{}, common.StatusDeployed), theNamespace, theName)

	require.Error(t, err)
	require.Contains(t, err.Error(),
		"reading the chart of release "+theNamespace+"/"+theName+
			": the stored chart carries no name in its metadata")
}

func TestAStoredReleaseCarryingNoInfoRefusesByNameInsteadOfPanicking(t *testing.T) {
	_, err := describe(&releasev1.Release{
		Name:      theName,
		Namespace: theNamespace,
		Version:   1,
		Chart:     &chartv2.Chart{Metadata: &chartv2.Metadata{Name: theChart, Version: theVersion}},
	}, theNamespace, theName)

	require.Error(t, err)
	require.Contains(t, err.Error(),
		"reading the status of release "+theNamespace+"/"+theName+
			": the stored release carries no status")
}

func TestAReleasesClientNewNeverBuiltRefusesToReadByNameInsteadOfPanicking(t *testing.T) {
	_, found, err := Releases{}.Release(context.Background(), theNamespace, theName)

	require.Error(t, err)
	require.False(t, found)
	require.Contains(t, err.Error(),
		"opening helm storage in namespace "+theNamespace+
			": this helm client was never built by New")
}

func TestAReleasesClientNewNeverBuiltRefusesToInstallByNameInsteadOfPanicking(t *testing.T) {
	err := Releases{}.InstallRelease(context.Background(), citypes.HelmRelease{
		Namespace: theNamespace, Name: theName, Chart: theChart, Version: theVersion,
	})

	require.Error(t, err)
	require.Contains(t, err.Error(),
		"opening helm storage in namespace "+theNamespace+
			": this helm client was never built by New")
}
