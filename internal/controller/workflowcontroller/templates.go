package workflowcontroller

import (
	"fmt"
	"sort"
	"strings"
)

// File is one rendered workflow: its name (without extension) and its
// full content.
type File struct {
	Name    string
	Content string
}

// RenderAll renders every owned workflow plus the runner, when one is
// configured. Verbatim content wins over a kind's template.
func RenderAll(spec Spec) ([]File, error) {
	files := make([]File, 0, len(spec.Workflows)+1)

	for _, w := range spec.Workflows {
		content, err := render(spec, w)
		if err != nil {
			return nil, err
		}

		files = append(files, File{Name: w.Name, Content: content})
	}

	if spec.Runner.Name != "" {
		files = append(files, File{Name: spec.Runner.Name, Content: renderRunner(spec)})
	}

	return files, nil
}

func render(spec Spec, w WorkflowSpec) (string, error) {
	if w.Content != "" {
		return w.Content, nil
	}

	switch w.Kind {
	case KindCommand:
		return renderCommand(spec, w), nil
	case KindFanOut:
		return renderFanOut(spec, w), nil
	case KindRelease:
		return renderRelease(), nil
	default:
		return "", fmt.Errorf("workflow %q: nothing renders kind %q", w.Name, w.Kind)
	}
}

// renderCommand is the shape shared by every workflow that builds the
// workspace and runs a command in the repo checkout: the on-block, the
// setup-go step, the workspace checkout, the toolchain install, the
// command, and optionally the push of what the command wrote.
func renderCommand(spec Spec, w WorkflowSpec) string {
	var b strings.Builder

	b.WriteString(w.Header)
	fmt.Fprintf(&b, "name: %s\n\non:\n", w.Name)

	if w.Cron != "" {
		fmt.Fprintf(&b, "  schedule:\n    - cron: \"%s\"\n  workflow_dispatch: {}\n", w.Cron)
	}

	if len(w.Events) > 0 {
		// workflow_dispatch as well, always. A workflow only another repo
		// can fire cannot be tested by the person who just changed it, and
		// an untestable workflow is how one of these went eight days
		// without completing a single run.
		fmt.Fprintf(&b, "  repository_dispatch:\n    types: [%s]\n  workflow_dispatch: {}\n",
			strings.Join(w.Events, ", "))
	}

	// A push to the repo the workflow lives on. Nothing dispatches when a
	// factory's own workspace files change, so its pipeline asks for this or
	// an edit to the pipeline never runs the pipeline.
	if len(w.PushBranches) > 0 {
		fmt.Fprintf(&b, "  push:\n    branches: [%s]\n", strings.Join(w.PushBranches, ", "))
	}

	job := w.Job
	if job == "" {
		job = "run"
	}

	b.WriteString("\npermissions:\n  contents: write\n")

	if reportsFailure(w) {
		b.WriteString("  issues: write\n")
	}

	// A registry write needs its own permission. Nothing else grants it, and
	// without it the push fails at the last step of a run that already built
	// everything.
	if w.Packages {
		b.WriteString("  packages: write\n")
	}

	// One run of the group at a time, queued rather than cancelled: an apply
	// that is already writing state must finish, and the next one then sees
	// what it wrote. Cancelling mid-write is how a state repo ends up with a
	// revision recorded and no run beside it.
	if w.Concurrency != "" {
		fmt.Fprintf(&b, "\nconcurrency:\n  group: %s\n  cancel-in-progress: false\n", w.Concurrency)
	}

	writeRunsOn(&b, spec, job)
	writeSetup(&b, spec)
	writeWorkspaceCheckout(&b, spec, w.Secret)
	writeToolchain(&b, spec)

	stepName := w.StepName
	if stepName == "" {
		stepName = "Run the command"
	}

	fmt.Fprintf(&b, "\n      - name: %s\n", stepName)

	// The token. NOTHING else puts one into the environment the command runs
	// in: engines inherit forge-ci's environment and nothing else, and the
	// secrets a bootstrap seals into Actions are sealed on the operator's
	// laptop and put nothing into a running job. So a release engine that
	// shells out to gh, or pushes to a registry, had no credential at all.
	//
	// secrets.GITHUB_TOKEN is injected by Actions. There is no secret to
	// create, seal or rotate.
	if len(w.PayloadEnv) > 0 || w.Token || w.Secret != "" {
		b.WriteString("        env:\n")

		if w.Token {
			b.WriteString("          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}\n")
		}

		// The workflow's own secret, by its own name. A run reconciles the
		// resources it declares before it does anything else, and a sealed
		// Actions secret is realized by PUTTING it: the API is write-only,
		// so a put is the only convergence there is, and a put needs the
		// value. Without this the secret reaches the checkout step and
		// nothing else, and every apply died on "environment variable
		// FORGE_CI_GITHUB_TOKEN is empty" - advice written for an operator's
		// laptop, printed inside a runner where there is no .envrc to fix.
		if w.Secret != "" {
			fmt.Fprintf(&b, "          %s: ${{ secrets.%s }}\n", w.Secret, w.Secret)
		}

		for _, field := range w.PayloadEnv {
			fmt.Fprintf(&b, "          %s: ${{ github.event.client_payload.%s }}\n",
				strings.ToUpper(field), field)
		}
	}

	b.WriteString("        run: |\n")
	writeIndented(&b, w.Command)

	if w.Push {
		fmt.Fprintf(&b, "\n      - name: Push what the pipeline wrote\n        run: |\n          cd %s\n          git push origin HEAD:%s\n",
			spec.Dir, spec.Ref)
	}

	writeFailureReport(&b, w)

	return b.String()
}

// reportsFailure answers whether a workflow needs to raise its own alarm.
//
// A scheduled run has no audience: nobody typed it and nobody is waiting on
// it, so a red run is a red icon on a page nobody opens. One instance failed
// every morning for eight days that way, and the first person to look found
// it by listing runs on a hunch.
//
// A repository_dispatch run has no audience either, and that is easy to get
// wrong. Somebody caused it, so it looks attended - but they caused it from
// another repo. A member push that fans out to a workspace pipeline is read
// by a person watching the member's own checks, not the workspace's, and a
// consumer filing an admission request never sees the register's run list at
// all.
//
// What is left is a push to this repo and a workflow_dispatch somebody
// typed. Both have a person already looking at this repo's checks, so they
// file nothing.
func reportsFailure(w WorkflowSpec) bool {
	return w.Cron != "" || len(w.Events) > 0
}

// writeFailureReport renders the step that opens an issue when the run
// fails. It uses the token every job already has, so a workflow needs no new
// secret to be able to speak.
//
// It dedupes on the title: a job that fails daily should leave one issue
// open, not thirty. Reopening after a green run is deliberate too, because a
// failure that comes back is news again.
func writeFailureReport(b *strings.Builder, w WorkflowSpec) {
	if !reportsFailure(w) {
		return
	}

	fmt.Fprintf(b, `
      - name: Say that the run failed
        if: failure()
        env:
          GH_TOKEN: ${{ github.token }}
          TITLE: "%s is failing"
        run: |
          open=$(gh issue list --repo "$GITHUB_REPOSITORY" --state open --search "$TITLE in:title" --json number --jq length)
          if [ "$open" -gt 0 ]; then
            echo "an issue is already open for this; not filing another"
            exit 0
          fi
          gh issue create --repo "$GITHUB_REPOSITORY" --title "$TITLE" --body "$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID failed. Nothing else reports this run, so it reports itself. Close this once a run goes green; a failure after that files a new one."
`, w.Name)
}

// renderFanOut tells every consumer about a new tag through a
// repository_dispatch.
func renderFanOut(_ Spec, w WorkflowSpec) string {
	var b strings.Builder

	b.WriteString(w.Header)

	tag := w.TagPattern
	if tag == "" {
		tag = "v*"
	}

	apiBase := w.APIBaseURL
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}

	fmt.Fprintf(&b, `name: %s

on:
  push:
    tags: ["%s"]

jobs:
  dispatch:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        consumer: [%s]
    steps:
      - name: Tell ${{ matrix.consumer }}
        run: |
          curl -sS -X POST \
            -H "Authorization: Bearer ${{ secrets.%s }}" \
            -H "Accept: application/vnd.github+json" \
            "%s/repos/${{ github.repository_owner }}/${{ matrix.consumer }}/dispatches" \
            -d '{"event_type":"%s","client_payload":{"tag":"${{ github.ref_name }}"}}'
`, w.Name, tag, strings.Join(w.Consumers, ", "), w.Secret, apiBase, w.EventType)

	return b.String()
}

// renderRelease is fully generic: tag one commit and publish a release,
// idempotently, with the workflow's own token.
func renderRelease() string {
	return `name: release

on:
  workflow_dispatch:
    inputs:
      tag:
        description: semver tag to publish
        required: true
      sha:
        description: commit the tag points at
        required: true

permissions:
  contents: write

jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - env:
          GH_TOKEN: ${{ github.token }}
          TAG: ${{ inputs.tag }}
          SHA: ${{ inputs.sha }}
        run: |
          git config user.name "forge-release"
          git config user.email "forge-release@users.noreply.github.com"
          if ! git rev-parse "refs/tags/$TAG" >/dev/null 2>&1; then
            git tag -a "$TAG" -m "release $TAG" "$SHA"
            git push origin "$TAG"
          fi
          if ! gh release view "$TAG" >/dev/null 2>&1; then
            gh release create "$TAG" --verify-tag --generate-notes
          fi
`
}

// renderRunner is the run tool's dispatch target: it builds the same
// workspace as every command workflow and executes the dispatched target
// script at the workspace root. The run name echoes the marker, which is
// what the run tool correlates on.
func renderRunner(spec Spec) string {
	var b strings.Builder

	fmt.Fprintf(&b, `# The remote compute runner: the github compute engine dispatches this
# workflow with a unique marker and the target script, then correlates
# the run by the marker in its name and polls it to conclusion.
name: %s
run-name: run ${{ inputs.marker }}

on:
  workflow_dispatch:
    inputs:
      marker:
        description: correlation marker echoed in the run name
        required: true
      script:
        description: target commands executed at the workspace root
        required: true

permissions:
  contents: write
`, spec.Runner.Name)
	writeRunsOn(&b, spec, "run")
	writeSetup(&b, spec)
	writeWorkspaceCheckout(&b, spec, spec.Runner.Secret)
	writeToolchain(&b, spec)
	b.WriteString(`
      - name: Run the targets
        run: |
`)

	if spec.Runner.SetupScript != "" {
		writeIndented(&b, spec.Runner.SetupScript)
	}

	b.WriteString("          ${{ inputs.script }}\n")

	return b.String()
}

// writeSetup renders the spec's setup actions. The instance names its
// own toolchain actions here; forge-ci renders whatever it is handed.
func writeSetup(b *strings.Builder, spec Spec) {
	for i, step := range spec.Setup {
		if i > 0 {
			b.WriteString("\n")
		}

		fmt.Fprintf(b, "      - uses: %s\n", step.Uses)

		if len(step.With) == 0 {
			continue
		}

		b.WriteString("        with:\n")

		keys := make([]string, 0, len(step.With))
		for k := range step.With {
			keys = append(keys, k)
		}

		sort.Strings(keys)

		for _, k := range keys {
			fmt.Fprintf(b, "          %s: \"%s\"\n", k, step.With[k])
		}
	}
}

// writeWorkspaceCheckout renders the two things a runner needs before it has
// a toolchain: credentials for private members, then the instance's own seed
// command. The seed is verbatim, because forge-ci names no toolchain and no
// bootstrap verb.
//
// This rendered five hand-written lines once. They cloned the factory, ran
// its place script from the directory above it, cloned the repo a second
// time, and passed relative paths across two directory levels. Two of those
// lines carried bugs and every scheduled run failed for a week. The seed
// command already stands a workspace up in one call, so the reimplementation
// is gone rather than repaired.
func writeWorkspaceCheckout(b *strings.Builder, spec Spec, secret string) {
	fmt.Fprintf(b, `
      - name: Check out the workspace around this repo
        run: |
          git config --global url."https://x-access-token:${{ secrets.%s }}@github.com/".insteadOf "git@github.com:"
`, secret)
	writeIndented(b, spec.Workspace.BootstrapCommand)
}

// writeRunsOn opens the jobs block and names where the job executes.
//
// A container image supplies the toolchain, so the install step disappears
// with it - but not the checkout: a container carries tools and not a
// workspace, and the members still have to be cloned. Only the two jobs that
// build a workspace take one; a fan-out curls a dispatch and a release runs
// gh, and both are content with whatever the runner ships.
func writeRunsOn(b *strings.Builder, spec Spec, job string) {
	fmt.Fprintf(b, "\njobs:\n  %s:\n    runs-on: ubuntu-latest\n", job)

	if spec.Container != "" {
		fmt.Fprintf(b, "    container:\n      image: %s\n", spec.Container)
	}

	b.WriteString("    steps:\n")
}

// writeToolchain renders the instance's verbatim toolchain script.
// forge-ci names no toolchain; the words are the spec's.
//
// No script means the job's container already carries the toolchain, and the
// step goes rather than being emitted empty: a `run: |` with nothing under it
// is not valid YAML.
func writeToolchain(b *strings.Builder, spec Spec) {
	if spec.Workspace.ToolchainScript == "" {
		return
	}

	b.WriteString(`
      - name: Install the toolchain from the workspace
        run: |
`)
	writeIndented(b, spec.Workspace.ToolchainScript)
}

// writeIndented writes a command block at run-block indentation, one
// line at a time, keeping internal blank lines.
func writeIndented(b *strings.Builder, command string) {
	for _, line := range strings.Split(strings.TrimRight(command, "\n"), "\n") {
		if line == "" {
			b.WriteString("\n")

			continue
		}

		b.WriteString("          " + line + "\n")
	}
}
