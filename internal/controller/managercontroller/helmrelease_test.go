package managercontroller_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/alexandremahdhaoui/forge-ci/internal/controller/managercontroller"
	"github.com/alexandremahdhaoui/forge-ci/internal/mocks/managercontrollermock"
	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
)

const (
	theReleaseName = "flux"
	theReleaseID   = theNamespace + "/" + theReleaseName

	theChart      = "flux2"
	theRepository = "oci://ghcr.io/fluxcd-community/charts"
	theVersion    = "2.13.0"

	theAPIServer      = "https://a-cluster.example:6443"
	anotherAPIServer  = "https://another-cluster.example:6443"
	theOlderVersion   = "2.12.0"
	thePendingInstall = "pending-install"
)

func helmRealizer(t *testing.T) (
	managercontroller.KubernetesRealizer,
	*managercontrollermock.MockKubernetes,
	*managercontrollermock.MockHelm,
) {
	t.Helper()

	cluster := managercontrollermock.NewMockKubernetes(t)
	helm := managercontrollermock.NewMockHelm(t)

	return managercontroller.NewKubernetesRealizer(t.Context(), cluster, helm), cluster, helm
}

func declaredReleaseWith(overrides map[string]any) citypes.Resource {
	spec := map[string]any{
		"namespace":  theNamespace,
		"name":       theReleaseName,
		"chart":      theChart,
		"repository": theRepository,
		"version":    theVersion,
		"apiServer":  theAPIServer,
	}

	for key, value := range overrides {
		if value == nil {
			delete(spec, key)

			continue
		}

		spec[key] = value
	}

	return citypes.Resource{
		Kind: managercontroller.KindHelmRelease,
		Name: theReleaseName,
		Spec: spec,
	}
}

func declaredRelease() citypes.Resource {
	return declaredReleaseWith(nil)
}

func liveRelease(chart, version, status string) citypes.HelmRelease {
	return citypes.HelmRelease{
		Namespace: theNamespace,
		Name:      theReleaseName,
		Chart:     chart,
		Version:   version,
		Status:    status,
	}
}

func TestTheKubernetesRealizerNamesBothKindsItKnowsWhenItRefusesAnother(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil, nil)

	_, err := r.Realize(citypes.Resource{Kind: "config-map", Name: "settings"}, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), managercontroller.KindSecret)
	assert.Contains(t, err.Error(), managercontroller.KindHelmRelease)
}

func TestAHealthyReleaseThatMatchesTheDeclarationIsKeptAndNothingIsInstalled(t *testing.T) {
	t.Parallel()

	r, cluster, helm := helmRealizer(t)

	cluster.EXPECT().APIServer().Return(theAPIServer)
	helm.EXPECT().Release(mock.Anything, theNamespace, theReleaseName).
		Return(liveRelease(theChart, theVersion, managercontroller.StatusDeployed), found, nil)

	action, err := r.Realize(declaredRelease(), plain)
	require.NoError(t, err)
	assert.False(t, action.Changed)
	assert.Equal(t, "kept release "+theReleaseID, action.Text)
}

func TestAHealthyReleaseOnAnotherChartVersionIsKeptAndNamesTheDriftAndNeverUpgrades(t *testing.T) {
	t.Parallel()

	r, cluster, helm := helmRealizer(t)

	cluster.EXPECT().APIServer().Return(theAPIServer)
	helm.EXPECT().Release(mock.Anything, theNamespace, theReleaseName).
		Return(liveRelease(theChart, theOlderVersion, managercontroller.StatusDeployed), found, nil)

	action, err := r.Realize(declaredRelease(), plain)
	require.NoError(t, err)
	assert.False(t, action.Changed)
	assert.Contains(t, action.Text, "kept release "+theReleaseID)
	assert.Contains(t, action.Text, theOlderVersion)
	assert.Contains(t, action.Text, theVersion)
}

func TestAMissingReleaseIsInstalledOnAnOrdinaryApplyAndAnswersDid(t *testing.T) {
	t.Parallel()

	r, cluster, helm := helmRealizer(t)

	var written citypes.HelmRelease

	cluster.EXPECT().APIServer().Return(theAPIServer)
	helm.EXPECT().Release(mock.Anything, theNamespace, theReleaseName).
		Return(citypes.HelmRelease{}, notFound, nil)
	helm.EXPECT().InstallRelease(mock.Anything, mock.Anything).
		Run(func(_ context.Context, release citypes.HelmRelease) { written = release }).
		Return(nil)

	action, err := r.Realize(declaredRelease(), plain)
	require.NoError(t, err)
	assert.True(t, action.Changed)
	assert.Contains(t, action.Text, "installed release "+theReleaseID)
	assert.Equal(t, theChart, written.Chart)
	assert.Equal(t, theVersion, written.Version)
	assert.Equal(t, theRepository, written.Repository)
}

func TestAMissingReleaseCarriesTheDeclaredValuesAndNamespaceFlagToTheHelmClient(t *testing.T) {
	t.Parallel()

	r, cluster, helm := helmRealizer(t)

	var written citypes.HelmRelease

	cluster.EXPECT().APIServer().Return(theAPIServer)
	helm.EXPECT().Release(mock.Anything, theNamespace, theReleaseName).
		Return(citypes.HelmRelease{}, notFound, nil)
	helm.EXPECT().InstallRelease(mock.Anything, mock.Anything).
		Run(func(_ context.Context, release citypes.HelmRelease) { written = release }).
		Return(nil)

	res := declaredReleaseWith(map[string]any{
		"values":          map[string]any{"replicas": 2},
		"createNamespace": true,
	})

	_, err := r.Realize(res, plain)
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"replicas": 2}, written.Values)
	assert.True(t, written.CreateNamespace)
}

func TestADryRunOverAMissingReleaseAnswersKeptAndInstallsNothing(t *testing.T) {
	t.Parallel()

	r, cluster, helm := helmRealizer(t)

	cluster.EXPECT().APIServer().Return(theAPIServer)
	helm.EXPECT().Release(mock.Anything, theNamespace, theReleaseName).
		Return(citypes.HelmRelease{}, notFound, nil)

	action, err := r.Realize(declaredRelease(), managercontroller.Options{DryRun: true})
	require.NoError(t, err)
	assert.False(t, action.Changed)
	assert.True(t, strings.HasPrefix(action.Text, "would install"), action.Text)
}

func TestAReleaseHeldInAPendingStatusIsRefusedByNameAndNeverRetriedBlind(t *testing.T) {
	t.Parallel()

	r, cluster, helm := helmRealizer(t)

	cluster.EXPECT().APIServer().Return(theAPIServer)
	helm.EXPECT().Release(mock.Anything, theNamespace, theReleaseName).
		Return(liveRelease(theChart, theVersion, thePendingInstall), found, nil)

	_, err := r.Realize(declaredRelease(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), theReleaseID)
	assert.Contains(t, err.Error(), thePendingInstall)
	assert.Contains(t, err.Error(), managercontroller.StatusDeployed)
}

func TestAReleaseHeldInAFailedStatusIsRefusedByName(t *testing.T) {
	t.Parallel()

	r, cluster, helm := helmRealizer(t)

	cluster.EXPECT().APIServer().Return(theAPIServer)
	helm.EXPECT().Release(mock.Anything, theNamespace, theReleaseName).
		Return(liveRelease(theChart, theVersion, "failed"), found, nil)

	_, err := r.Realize(declaredRelease(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `status "failed"`)
}

func TestAReleaseCarryingAnotherChartIsRefusedNamingBothCharts(t *testing.T) {
	t.Parallel()

	r, cluster, helm := helmRealizer(t)

	cluster.EXPECT().APIServer().Return(theAPIServer)
	helm.EXPECT().Release(mock.Anything, theNamespace, theReleaseName).
		Return(liveRelease("cert-manager", theVersion, managercontroller.StatusDeployed), found, nil)

	_, err := r.Realize(declaredRelease(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cert-manager")
	assert.Contains(t, err.Error(), theChart)
}

func TestAnAPIServerThatIsNotTheOneTheDeclarationNamesIsRefusedByName(t *testing.T) {
	t.Parallel()

	r, cluster, _ := helmRealizer(t)

	cluster.EXPECT().APIServer().Return(anotherAPIServer)

	_, err := r.Realize(declaredRelease(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), theAPIServer)
	assert.Contains(t, err.Error(), anotherAPIServer)
}

func TestAClusterClientThatNamesNoAPIServerIsRefusedByName(t *testing.T) {
	t.Parallel()

	r, cluster, _ := helmRealizer(t)

	cluster.EXPECT().APIServer().Return("")

	_, err := r.Realize(declaredRelease(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "names no api server")
}

func TestAManagerCarryingNoClusterClientRefusesAReleaseByName(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil, nil)

	_, err := r.Realize(declaredRelease(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "this manager carries no cluster client yet")
}

func TestAManagerCarryingNoHelmClientRefusesAReleaseByName(t *testing.T) {
	t.Parallel()

	cluster := managercontrollermock.NewMockKubernetes(t)
	cluster.EXPECT().APIServer().Return(theAPIServer)

	r := managercontroller.NewKubernetesRealizer(t.Context(), cluster, nil)

	_, err := r.Realize(declaredRelease(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "this manager carries no helm client yet")
}

func TestAHelmClientThatCannotBeReadIsReportedWithTheReleaseItWasReading(t *testing.T) {
	t.Parallel()

	r, cluster, helm := helmRealizer(t)

	cluster.EXPECT().APIServer().Return(theAPIServer)
	helm.EXPECT().Release(mock.Anything, theNamespace, theReleaseName).
		Return(citypes.HelmRelease{}, notFound, errors.New("the registry is unreachable"))

	_, err := r.Realize(declaredRelease(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading release "+theReleaseID)
	assert.Contains(t, err.Error(), "the registry is unreachable")
}

func TestAnInstallThatFailsIsReportedWithTheReleaseItWasInstalling(t *testing.T) {
	t.Parallel()

	r, cluster, helm := helmRealizer(t)

	cluster.EXPECT().APIServer().Return(theAPIServer)
	helm.EXPECT().Release(mock.Anything, theNamespace, theReleaseName).
		Return(citypes.HelmRelease{}, notFound, nil)
	helm.EXPECT().InstallRelease(mock.Anything, mock.Anything).
		Return(errors.New("the chart was not pulled"))

	_, err := r.Realize(declaredRelease(), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "installing release "+theReleaseID)
	assert.Contains(t, err.Error(), "the chart was not pulled")
}

func TestARepositoryOnAnotherSchemeIsRefusedByName(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil, nil)

	res := declaredReleaseWith(map[string]any{"repository": "http://charts.example"})

	_, err := r.Realize(res, plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `spec.repository is "http://charts.example"`)
	assert.Contains(t, err.Error(), "oci:// or https://")
}

func TestARepositoryOverHTTPSIsAccepted(t *testing.T) {
	t.Parallel()

	r, cluster, helm := helmRealizer(t)

	cluster.EXPECT().APIServer().Return(theAPIServer)
	helm.EXPECT().Release(mock.Anything, theNamespace, theReleaseName).
		Return(liveRelease(theChart, theVersion, managercontroller.StatusDeployed), found, nil)

	res := declaredReleaseWith(map[string]any{"repository": "https://charts.example"})

	action, err := r.Realize(res, plain)
	require.NoError(t, err)
	assert.Equal(t, "kept release "+theReleaseID, action.Text)
}

func TestADeclarationCarryingNoRepositoryIsRefusedByName(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil, nil)

	_, err := r.Realize(declaredReleaseWith(map[string]any{"repository": nil}), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.repository is required")
}

func TestAVersionThatIsNotMajorMinorPatchIsRefusedByName(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil, nil)

	for _, version := range []string{"^2.13.0", "~2.13.0", "2.13.x", ">=2.13.0", "2.13"} {
		_, err := r.Realize(declaredReleaseWith(map[string]any{"version": version}), plain)
		require.Error(t, err)
		assert.Contains(t, err.Error(),
			"one exact chart version written as major.minor.patch with an optional leading v "+
				"is required, such as 2.13.0 or v2.13.0")
	}
}

func TestAVPrefixedVersionIsExactAndIsAcceptedAgainstALiveReleaseThatDropsThePrefix(t *testing.T) {
	t.Parallel()

	r, cluster, helm := helmRealizer(t)

	cluster.EXPECT().APIServer().Return(theAPIServer)
	helm.EXPECT().Release(mock.Anything, theNamespace, theReleaseName).
		Return(liveRelease(theChart, theVersion, managercontroller.StatusDeployed), found, nil)

	action, err := r.Realize(declaredReleaseWith(map[string]any{"version": "v" + theVersion}), plain)
	require.NoError(t, err)
	assert.False(t, action.Changed)
	assert.Equal(t, "kept release "+theReleaseID, action.Text)
}

func TestADriftTextPrintsTheVersionTheDeclarationWroteAndKeepsItsLeadingV(t *testing.T) {
	t.Parallel()

	r, cluster, helm := helmRealizer(t)

	cluster.EXPECT().APIServer().Return(theAPIServer)
	helm.EXPECT().Release(mock.Anything, theNamespace, theReleaseName).
		Return(liveRelease(theChart, theOlderVersion, managercontroller.StatusDeployed), found, nil)

	action, err := r.Realize(declaredReleaseWith(map[string]any{"version": "v" + theVersion}), plain)
	require.NoError(t, err)
	assert.False(t, action.Changed)
	assert.Equal(t, "kept release "+theReleaseID+", the cluster holds chart "+theChart+" "+
		theOlderVersion+" and the declaration names v"+theVersion, action.Text)
}

func TestAVPrefixedVersionReachesTheHelmClientWithoutItsPrefix(t *testing.T) {
	t.Parallel()

	r, cluster, helm := helmRealizer(t)

	var written citypes.HelmRelease

	cluster.EXPECT().APIServer().Return(theAPIServer)
	helm.EXPECT().Release(mock.Anything, theNamespace, theReleaseName).
		Return(citypes.HelmRelease{}, notFound, nil)
	helm.EXPECT().InstallRelease(mock.Anything, mock.Anything).
		Run(func(_ context.Context, release citypes.HelmRelease) { written = release }).
		Return(nil)

	action, err := r.Realize(declaredReleaseWith(map[string]any{"version": "v" + theVersion}), plain)
	require.NoError(t, err)
	assert.Equal(t, theVersion, written.Version)
	assert.Equal(t, "installed release "+theReleaseID+" from chart "+theChart+" "+theVersion, action.Text)
}

func TestAPrereleaseChartVersionIsExactAndIsAccepted(t *testing.T) {
	t.Parallel()

	r, cluster, helm := helmRealizer(t)

	cluster.EXPECT().APIServer().Return(theAPIServer)
	helm.EXPECT().Release(mock.Anything, theNamespace, theReleaseName).
		Return(liveRelease(theChart, "2.13.0-rc.1", managercontroller.StatusDeployed), found, nil)

	res := declaredReleaseWith(map[string]any{"version": "2.13.0-rc.1"})

	action, err := r.Realize(res, plain)
	require.NoError(t, err)
	assert.Equal(t, "kept release "+theReleaseID, action.Text)
}

func TestADeclarationCarryingNoVersionIsRefusedByName(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil, nil)

	_, err := r.Realize(declaredReleaseWith(map[string]any{"version": nil}), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.version is required")
}

func TestADeclarationMissingTheNamespaceTheReleaseOrTheChartIsRefusedByName(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil, nil)

	for _, key := range []string{"namespace", "name", "chart"} {
		_, err := r.Realize(declaredReleaseWith(map[string]any{key: nil}), plain)
		require.Error(t, err)
		assert.Contains(t, err.Error(),
			"spec.namespace, spec.name and spec.chart are required")
	}
}

func TestAReleaseNameThatIsNotAStringIsRefusedByName(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil, nil)

	_, err := r.Realize(declaredReleaseWith(map[string]any{"name": 7}), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading spec.name: a string is required, the spec holds a int")
}

func TestAValuesBlockThatIsNotAMapIsRefusedByName(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil, nil)

	_, err := r.Realize(declaredReleaseWith(map[string]any{"values": "replicas=2"}), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading spec.values: a map is required, the spec holds a string")
}

func TestACreateNamespaceThatIsNotABoolIsRefusedByName(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil, nil)

	_, err := r.Realize(declaredReleaseWith(map[string]any{"createNamespace": "yes"}), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading spec.createNamespace: a bool is required, the spec holds a string")
}

func TestADeclarationCarryingNoAPIServerIsRefusedByName(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil, nil)

	_, err := r.Realize(declaredReleaseWith(map[string]any{"apiServer": nil}), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.apiServer is required")
}

func TestAnAPIServerKeyThatIsNotAStringIsRefusedByName(t *testing.T) {
	t.Parallel()

	r := managercontroller.NewKubernetesRealizer(t.Context(), nil, nil)

	_, err := r.Realize(declaredReleaseWith(map[string]any{"apiServer": 7}), plain)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading spec.apiServer: a string is required, the spec holds a int")
}
