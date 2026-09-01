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
		return renderRelease(spec, w), nil
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

	if w.ReportFailure {
		b.WriteString("  issues: write\n")
	}

	// A registry write needs its own permission. Nothing else grants it, and
	// without it the push fails at the last step of a run that already built
	// everything.
	if w.Packages {
		b.WriteString("  packages: write\n")
	}

	// One run of this workflow at a time, queued rather than cancelled: an
	// apply that is already writing state must finish, and the next one then
	// sees what it wrote and converges. This is the engine's own guarantee,
	// not a knob - one push wave dispatches once per member, so a workflow
	// without it races itself on the state repo, and forgetting to declare
	// it was exactly how forge-self's duplicate runs went red. The group is
	// the platform's own name for this workflow; nothing is hand-typed.
	// Cancelling mid-write is how a state repo ends up with a revision
	// recorded and no run beside it, so a superseded start waits instead.
	b.WriteString("\nconcurrency:\n  group: ${{ github.workflow }}\n  cancel-in-progress: false\n")

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
	// reaches the API, or pushes to a registry, had no credential at all.
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

// writeFailureReport renders the step that opens an issue when the run
// fails. It uses the token every job already has, so a workflow needs no new
// secret to be able to speak.
//
// The step is a step and not an engine because it is the only thing that can
// see a job which died before forge-ci was alive: a missing binary, a
// checkout that failed, a container without git. Whether it exists at all is
// declared, in reportFailure, rather than inferred from the on-block.
//
// curl, and not a CLI. This runs in whatever image the job named, and the
// toolchain image carries no gh; curl is the one client every runner and
// every image has, and it is what the fan-out and the notify workflow
// already use.
//
// It dedupes on the exact title, so a job that fails daily leaves one issue
// open rather than thirty. A closed one does not suppress a new issue,
// because a failure that comes back after a green run is news again.
func writeFailureReport(b *strings.Builder, w WorkflowSpec) {
	if !w.ReportFailure {
		return
	}

	// Listing open issues rather than asking the search API: the search
	// index is asynchronous, so two runs seconds apart can both find nothing
	// and both file. A list is consistent immediately.
	//
	// The grep tolerates either spacing GitHub emits after the key, and
	// TITLE goes through the environment rather than into the URL so a title
	// with a space cannot split the request.
	fmt.Fprintf(b, `
      - name: Say that the run failed
        if: failure()
        env:
          TOKEN: ${{ github.token }}
          TITLE: "%s is failing"
        run: |
          open=$(curl -fsS \
            -H "Authorization: Bearer $TOKEN" \
            -H "Accept: application/vnd.github+json" \
            "$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/issues?state=open&per_page=100")
          if printf '%%s' "$open" | grep -q "\"title\": *\"$TITLE\""; then
            echo "an issue is already open for this; not filing another"
            exit 0
          fi
          curl -fsS -X POST \
            -H "Authorization: Bearer $TOKEN" \
            -H "Accept: application/vnd.github+json" \
            "$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/issues" \
            -d "{\"title\":\"$TITLE\",\"body\":\"$GITHUB_SERVER_URL/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID failed. Nothing else reports this run, so it reports itself. Close this once a run goes green; a failure after that files a new one.\"}"
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
//
// It runs the toolchain rather than a CLI the runner happens to ship. Both
// halves are separately idempotent and a tag that already points elsewhere
// is refused, which is a rule with a test in releasecontroller rather than a
// shell `if` in generated YAML that nothing can reach.
//
// Its prelude is the command job's, verbatim through the same three
// helpers: the container when the factory names one, the toolchain install
// otherwise, and the workspace checkout either way.
//
// The workspace is not optional here even though this job only writes one
// tag. A toolchain script does `cd <member> && go install`, so it needs the
// members on disk; a lone actions/checkout of this repo makes that step die
// on a directory that does not exist, which is the failure this whole
// mechanism was built to delete. Sharing the prelude is what stops the two
// jobs drifting into one that works and one that does not.
func renderRelease(spec Spec, w WorkflowSpec) string {
	var b strings.Builder

	b.WriteString(w.Header)
	fmt.Fprintf(&b, `name: %s

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
`, w.Name)

	writeRunsOn(&b, spec, "release")
	writeSetup(&b, spec)
	writeWorkspaceCheckout(&b, spec, w.Secret)
	writeToolchain(&b, spec)

	// --dir is this repo's checkout inside the workspace, not the workspace
	// root, because the root is not a repo and the tag belongs on the repo
	// the workflow lives in.
	fmt.Fprintf(&b, `
      - name: Publish the release
        env:
          GITHUB_TOKEN: ${{ github.token }}
          TAG: ${{ inputs.tag }}
          SHA: ${{ inputs.sha }}
        run: |
          forge-ci release --repo "$GITHUB_REPOSITORY" --dir %s --tag "$TAG" --sha "$SHA"
`, spec.Dir)

	return b.String()
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
// writeWorkspaceCheckout stands the workspace up around this repo.
//
// The insteadOf line is written only when there is a secret to write into
// it. A workflow that named none used to render `${{ secrets. }}`, which
// Actions expands to nothing: the rewrite then maps every ssh remote to an
// unauthenticated https one and the clone of the first private member fails
// on a credential that was never there. ParseSpec refuses that config, and
// this refuses to render it either way, because a half-written credential
// line is worse than an absent one.
func writeWorkspaceCheckout(b *strings.Builder, spec Spec, secret string) {
	b.WriteString(`
      - name: Check out the workspace around this repo
        run: |
`)

	if secret != "" {
		fmt.Fprintf(b,
			`          git config --global url."https://x-access-token:${{ secrets.%s }}@github.com/".insteadOf "git@github.com:"
`, secret)
	}

	writeIndented(b, spec.Workspace.BootstrapCommand)
}

// writeRunsOn opens the jobs block and names where the job executes.
//
// A container image supplies the toolchain, so the install step disappears
// with it - but not the checkout: a container carries tools and not a
// workspace, and the members still have to be cloned. Only the two jobs that
// build a workspace take one; a fan-out only curls a dispatch, so it is
// content with whatever the runner ships.
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
