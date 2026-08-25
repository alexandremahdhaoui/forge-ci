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
		fmt.Fprintf(&b, "  repository_dispatch:\n    types: [%s]\n", strings.Join(w.Events, ", "))
	}

	job := w.Job
	if job == "" {
		job = "run"
	}

	fmt.Fprintf(&b, "\npermissions:\n  contents: write\n\njobs:\n  %s:\n    runs-on: ubuntu-latest\n    steps:\n", job)
	writeSetup(&b, spec)
	writeWorkspaceCheckout(&b, spec, w.Secret)
	writeToolchain(&b, spec)

	stepName := w.StepName
	if stepName == "" {
		stepName = "Run the command"
	}

	fmt.Fprintf(&b, "\n      - name: %s\n", stepName)

	if len(w.PayloadEnv) > 0 {
		b.WriteString("        env:\n")

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

	return b.String()
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

jobs:
  run:
    runs-on: ubuntu-latest
    steps:
`, spec.Runner.Name)
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

func writeWorkspaceCheckout(b *strings.Builder, spec Spec, secret string) {
	ws := spec.Workspace

	fmt.Fprintf(b, `
      - name: Check out the workspace around this repo
        run: |
          git config --global url."https://x-access-token:${{ secrets.%s }}@github.com/".insteadOf "git@github.com:"
          git clone "git@github.com:${{ github.repository_owner }}/%s.git"
          sh %s/%s
          git clone "git@github.com:${{ github.repository }}.git" %s
          (cd %s && %s)
`, secret, ws.FactoryRepo, ws.FactoryRepo, ws.PlaceScript, spec.Dir, spec.Dir, ws.BootstrapCommand)
}

// writeToolchain renders the instance's verbatim toolchain script.
// forge-ci names no toolchain; the words are the spec's.
func writeToolchain(b *strings.Builder, spec Spec) {
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
