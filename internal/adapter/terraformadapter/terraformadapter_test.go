package terraformadapter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const versionAnswer = `{"terraform_version":"1.16.1","platform":"linux_amd64"}`

const planAnswer = `{"format_version":"1.2","resource_changes":[` +
	`{"address":"aws_route53_record.home","change":{"actions":["create"]}}]}`

const noChangePlanAnswer = `{"format_version":"1.2","resource_changes":[` +
	`{"address":"aws_route53_record.home","change":{"actions":["no-op"]}}]}`

var (
	answersEverything string
	answersNoChange   string
	failsEveryCommand string
	answersANonPlan   string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "terraformadapter")
	if err != nil {
		panic(err)
	}

	answersEverything = writeStub(dir, "everything",
		"plan) exit 2 ;;\nshow) echo '"+planAnswer+"' ;;\n*) exit 0 ;;")
	answersNoChange = writeStub(dir, "no-change",
		"show) echo '"+noChangePlanAnswer+"' ;;\n*) exit 0 ;;")
	failsEveryCommand = writeStub(dir, "failure",
		"*) echo 'the state is locked' >&2; exit 1 ;;")
	answersANonPlan = writeStub(dir, "non-plan",
		"show) echo 'not a plan' ;;\n*) exit 0 ;;")

	code := m.Run()

	_ = os.RemoveAll(dir)

	os.Exit(code)
}

func writeStub(dir, name, body string) string {
	path := filepath.Join(dir, name, "terraform")

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		panic(err)
	}

	script := "#!/bin/sh\ncase \"$1\" in\nversion) echo '" + versionAnswer + "' ;;\n" + body + "\nesac\n"

	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		panic(err)
	}

	return path
}

func TestNewFindsTheTerraformBinaryOnThePath(t *testing.T) {
	t.Setenv("PATH", filepath.Dir(answersEverything))

	root, err := New()
	require.NoError(t, err)
	assert.Equal(t, answersEverything, root.execPath)
}

func TestNewRefusesWhenNoTerraformIsOnThePath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := New()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "looking up the terraform binary on the path")
}

func TestInitReportsTheDirectoryItCouldNotOpen(t *testing.T) {
	t.Parallel()

	err := Root{execPath: "/nowhere/terraform"}.Init(t.Context(), "/nowhere/module")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "opening the root module at /nowhere/module")
	assert.Contains(t, err.Error(), "/nowhere/terraform")
}

func TestPlanReportsTheDirectoryItCouldNotOpen(t *testing.T) {
	t.Parallel()

	_, _, err := Root{execPath: "/nowhere/terraform"}.Plan(t.Context(), "/nowhere/module")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "opening the root module at /nowhere/module")
}

func TestApplyReportsTheDirectoryItCouldNotOpen(t *testing.T) {
	t.Parallel()

	err := Root{execPath: "/nowhere/terraform"}.Apply(t.Context(), "/nowhere/module")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "opening the root module at /nowhere/module")
}

func TestInitReportsTheDirectoryOfATerraformThatFailed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	err := Root{execPath: failsEveryCommand}.Init(t.Context(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initializing the root module at "+dir)
}

func TestPlanReportsTheDirectoryOfATerraformThatFailed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, _, err := Root{execPath: failsEveryCommand}.Plan(t.Context(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "planning the root module at "+dir)
}

func TestApplyReportsTheDirectoryOfATerraformThatFailed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	err := Root{execPath: failsEveryCommand}.Apply(t.Context(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "applying the root module at "+dir)
}

func TestInitRunsAgainstADirectoryThatHoldsARootModule(t *testing.T) {
	t.Parallel()

	require.NoError(t, Root{execPath: answersEverything}.Init(t.Context(), t.TempDir()))
}

func TestApplyRunsAgainstADirectoryThatHoldsARootModule(t *testing.T) {
	t.Parallel()

	require.NoError(t, Root{execPath: answersEverything}.Apply(t.Context(), t.TempDir()))
}

func TestPlanAnswersBothTheExitCodeAndTheDocumentWhenTerraformReportsChanges(t *testing.T) {
	t.Parallel()

	reportsChanges, document, err := Root{execPath: answersEverything}.Plan(t.Context(), t.TempDir())
	require.NoError(t, err)
	assert.True(t, reportsChanges)
	require.Len(t, document.ResourceChanges, 1)
	assert.Equal(t, "aws_route53_record.home", document.ResourceChanges[0].Address)
	assert.False(t, document.ResourceChanges[0].Change.Actions.NoOp())
}

func TestPlanAnswersBothTheExitCodeAndTheDocumentWhenTerraformReportsNoChange(t *testing.T) {
	t.Parallel()

	reportsChanges, document, err := Root{execPath: answersNoChange}.Plan(t.Context(), t.TempDir())
	require.NoError(t, err)
	assert.False(t, reportsChanges)
	require.Len(t, document.ResourceChanges, 1)
	assert.True(t, document.ResourceChanges[0].Change.Actions.NoOp())
}

func TestPlanReportsTheDirectoryOfAPlanItCouldNotRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, _, err := Root{execPath: answersANonPlan}.Plan(t.Context(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading the plan of the root module at "+dir)
}
