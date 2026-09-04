package workflowcontroller

import (
	"fmt"
	"sort"
	"strings"

	"github.com/alexandremahdhaoui/forge-ci/pkg/citypes"
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
		return renderCommand(spec, w)
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
func renderCommand(spec Spec, w WorkflowSpec) (string, error) {
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

		// A push that touched only these paths never starts the run. This
		// is the platform's own filter and it is blind to what a file is
		// for, so the instance lists only what nothing embeds.
		if len(w.PushPathsIgnore) > 0 {
			fmt.Fprintf(&b, "    paths-ignore: [%s]\n", quoteList(w.PushPathsIgnore))
		}
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

	if spec.Phases {
		jobs, err := phasedJobs(spec, w)
		if err != nil {
			return "", err
		}

		writePhasedJobs(&b, spec, w, jobs)

		return b.String(), nil
	}

	writeRunsOn(&b, spec, job)
	writeSetup(&b, spec)
	writeStoreRestore(&b)
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

	writeStoreSave(&b)
	writeFailureReport(&b, w)

	return b.String(), nil
}

// The fixed jobs of one phased apply. Every job stands the workspace up on
// its own and runs `<command> --phase <name>`; the jobs hand each other what
// they decided through the state repo, and the files the stages built cross
// between them as Actions artifacts under the directory the compute engine's
// put keeps them in.
//
// There are two fixed jobs and no more. A release is a substage, so the job
// that publishes is a stage job like any other, named by the pipeline. Every
// job after the evaluate one is named by the pipeline: one per stage, or one
// per substage with a promotion job per stage, as the spec's jobs key says.
// A stage may not take one of these two names.
const (
	jobSelfReconcile = "self-reconcile"
	jobEvaluate      = "evaluate"
	jobStages        = "stages"
)

// The two granularities of the stage jobs.
const (
	JobsPerStage    = "stage"
	JobsPerSubstage = "substage"
)

// artifactDir is where a compute engine's put keeps what a run built, under
// the factory root. Every job that runs targets uploads it under a name of
// its own and downloads every name this run has written so far, so a stage
// reads what the stages before it built - the stage that publishes included -
// on a runner that built none of it.
const artifactDir = ".forge-ci/artifacts"

// carriedDir holds the tarballs that cross between jobs, kept apart from
// the artifacts themselves so that unpacking one never packs it again.
const carriedDir = ".forge-ci/carried"

// packMark is the file whose modification time separates what a job
// inherited from what it built, and packList is where the second of those
// is written for tar to read.
const (
	packMark = ".forge-ci/pack-mark"
	packList = ".forge-ci/pack-list"
)

// job is one rendered job of a phased workflow: its id, the step name a
// human reads, the flags after `--phase`, what it needs, and whether it
// carries built files out.
type job struct {
	id string
	// name is the job's title: what a person reads in a list of jobs, where
	// the platform would otherwise show the identifier. Derived from the
	// stage and substage names unless the pipeline declares a display name,
	// which is then used verbatim.
	name  string
	step  string
	flags string
	needs []string
	// download says the job brings back what the jobs before it built. A job
	// that does work in any stage after the first needs it: a stage reads
	// what the stage before it made, and the substage that publishes reads
	// all of it. Nothing has been built yet in front of the first stage, so
	// its jobs skip a step that could only match nothing.
	download bool
	upload   bool
	// gated says the job runs only on proceed: everything after evaluate.
	gated bool
	// push says the workflow's push step follows the command, for a
	// pipeline whose stages write into its own checkout.
	push bool
}

// phasedJobs lays the jobs out: the two fixed ones in front, the stage
// jobs the pipeline names, and the release at the end. With no stage names
// known - an older core that declares without them - the stages are one
// job, which is what every phased workflow rendered before names crossed
// the wire.
func phasedJobs(spec Spec, w WorkflowSpec) ([]job, error) {
	jobs := []job{
		{
			id: jobSelfReconcile, name: "Reconcile CI resources",
			step: "Reconcile CI resources", flags: "--phase " + jobSelfReconcile,
		},
		{
			id: jobEvaluate, name: "Evaluate next steps",
			step: "Evaluate next steps", flags: "--phase " + jobEvaluate,
			needs: []string{jobSelfReconcile},
		},
	}

	// What a stage waits on: every job of the stage in front of it. Empty
	// while nothing has run, which is what tells the first stage's jobs
	// they have nothing to bring back.
	var prev []string

	if len(spec.Stages) == 0 {
		jobs = append(jobs, job{
			id: jobStages, name: "Run the stages", step: "Run the stages",
			flags: "--phase stages",
			needs: []string{jobEvaluate}, gated: true, download: true, upload: true, push: w.Push,
		})
		prev = []string{jobStages}
	}

	for _, stage := range spec.Stages {
		if stage.Name == jobSelfReconcile || stage.Name == jobEvaluate {
			return nil, fmt.Errorf("stage %q takes the name of a fixed job; %s and %s are reserved",
				stage.Name, jobSelfReconcile, jobEvaluate)
		}

		if spec.Jobs != JobsPerSubstage {
			jobs = append(jobs, job{
				id: stage.Name, name: stageTitle(stage),
				step:  "Run stage " + stage.Name,
				flags: "--phase stages --stage " + stage.Name,
				needs: append([]string{jobEvaluate}, prev...), gated: true,
				download: len(prev) > 0, upload: true, push: w.Push,
			})
			prev = []string{stage.Name}

			continue
		}

		// One job per substage, running beside each other, and the stage
		// after them waiting on all of them.
		//
		// There is no job between the two. A stage's promotion used to have
		// one, and it spent a checkout, a container and a toolchain to write
		// down an answer the next stage's jobs work out for themselves from
		// the same run records in seconds. On a pipeline of four stages that
		// is four whole jobs deciding nothing.
		subs := make([]string, 0, len(stage.Substages))

		for _, sub := range stage.Substages {
			id := stage.Name + "-" + sub.Name
			subs = append(subs, id)

			// What the substage declared it needs, within its own stage,
			// becomes an edge between the two jobs - and a job that waits
			// on a sibling downloads too, because the sibling may have
			// built what it is about to read.
			needs := append([]string{jobEvaluate}, prev...)
			for _, n := range sub.Needs {
				needs = append(needs, stage.Name+"-"+n)
			}

			jobs = append(jobs, job{
				id: id, name: substageTitle(stage, sub),
				step:  "Run " + stage.Name + " " + sub.Name,
				flags: "--phase stages --stage " + stage.Name + " --substage " + sub.Name,
				needs: needs, gated: true,
				download: len(prev) > 0 || len(sub.Needs) > 0, upload: true, push: w.Push,
			})
		}

		prev = subs
	}

	seen := map[string]bool{}

	for i, j := range jobs {
		// The first stage needs the evaluation twice over: once for its
		// word and once as the job before it. Once is enough.
		jobs[i].needs = dedupe(j.needs)

		if seen[j.id] {
			return nil, fmt.Errorf("two jobs would be named %q; a stage and a substage of another stage collide", j.id)
		}

		seen[j.id] = true
	}

	return jobs, nil
}

// stageTitle and substageTitle are what a person reads. A pipeline that
// declares a display name gets it verbatim; one that declares none gets a
// title derived from the names it already has, so nothing has to be typed
// twice to read as words.
func stageTitle(stage citypes.DeclaredStage) string {
	if stage.DisplayName != "" {
		return stage.DisplayName
	}

	return stage.Name
}

func substageTitle(stage citypes.DeclaredStage, sub citypes.DeclaredSubstage) string {
	if sub.DisplayName != "" {
		return sub.DisplayName
	}

	return stageTitle(stage) + " › " + sub.Name
}

func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}

	for _, s := range in {
		if !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}

	return out
}

// writePhasedJobs renders one apply as jobs. The evaluate job's last line
// is one word, skip or proceed, captured as its output; every job after it
// runs only on proceed, so a run with nothing to release shows them skipped
// rather than green. A self reconcile that superseded itself exits 0 with
// no revision, and the evaluate job then runs on the superseded state and
// skips on its own - the run that push fired carries the work.
func writePhasedJobs(b *strings.Builder, spec Spec, w WorkflowSpec, jobs []job) {
	b.WriteString("\njobs:\n")

	for _, j := range jobs {
		fmt.Fprintf(b, "  %s:\n", j.id)

		if j.name != "" {
			fmt.Fprintf(b, "    name: %s\n", j.name)
		}

		if len(j.needs) > 0 {
			fmt.Fprintf(b, "    needs: [%s]\n", strings.Join(j.needs, ", "))
		}

		if j.gated {
			fmt.Fprintf(b, "    if: needs.%s.outputs.outcome == 'proceed'\n", jobEvaluate)
		}

		if j.id == jobEvaluate {
			b.WriteString("    outputs:\n      outcome: ${{ steps.phase.outputs.outcome }}\n" +
				"      revision: ${{ steps.phase.outputs.revision }}\n")
		}

		b.WriteString("    runs-on: ubuntu-latest\n")

		if spec.Container != "" {
			fmt.Fprintf(b, "    container:\n      image: %s\n", spec.Container)
		}

		b.WriteString("    steps:\n")

		writeSetup(b, spec)
		writeStoreRestore(b)
		writeWorkspaceCheckout(b, spec, w.Secret)
		writeToolchain(b, spec)

		if j.download {
			// Every name this run has written so far, merged. A job early in
			// the run matches none of them and the step is a no-op; one after
			// a build gets what the build made. Nothing here knows which jobs
			// wrote which files, and it does not need to: the record says
			// where each artifact belongs and get puts it there.
			//
			// Each name holds one tarball, and unpacking is what restores
			// the modes. The platform's own artifact format is a zip, which
			// does not carry a unix mode, so a binary uploaded as loose
			// files comes back unexecutable however carefully it was
			// written. tar carries the bits, so a carried binary is still a
			// binary a later stage can run.
			fmt.Fprintf(b, "\n      - name: Bring back what the jobs before this one built\n        uses: actions/download-artifact@v8\n        with:\n          pattern: built-${{ github.run_id }}-*\n          merge-multiple: true\n          path: %s\n", carriedDir)
			fmt.Fprintf(b, "\n      - name: Unpack it\n        run: |\n          mkdir -p %s\n          for f in %s/*.tar.gz; do\n            [ -e \"$f\" ] || continue\n            tar -xzf \"$f\" -C %s\n          done\n", artifactDir, carriedDir, artifactDir)
		}

		if j.upload {
			// Everything already on disk is somebody else's. Written after
			// the unpack and before the run, so the line it draws is exact.
			fmt.Fprintf(b, "\n      - name: Mark what was here before this job ran\n        run: |\n          mkdir -p %s\n          touch %s\n", artifactDir, packMark)
		}

		fmt.Fprintf(b, "\n      - name: %s\n        id: phase\n", j.step)
		writeCommandEnv(b, w)
		b.WriteString("        run: |\n")

		command := strings.TrimSpace(w.Command) + " " + j.flags
		if j.gated {
			command += " --revision ${{ needs." + jobEvaluate + ".outputs.revision }}"
		}

		if j.id == jobEvaluate {
			// Two lines of the report are contracts. The last is the
			// word, captured rather than parsed from the middle because a
			// report can say anything before it. The first is the revision
			// this run proves, which every job after this one is told, so
			// that a job whose own checkout has moved on - a stage of this
			// run publishes into one of the pipeline's own repos - still
			// answers for the commits the run started with.
			writeIndented(b, "out=$("+command+")\n"+
				"printf '%s\\n' \"$out\"\n"+
				"echo \"outcome=$(printf '%s\\n' \"$out\" | sed -n 's/^"+jobEvaluate+": //p' | tail -n 1)\" >> \"$GITHUB_OUTPUT\"\n"+
				"echo \"revision=$(printf '%s\\n' \"$out\" | sed -n 's/^revision //p' | head -n 1)\" >> \"$GITHUB_OUTPUT\"")
		} else {
			writeIndented(b, command)
		}

		if j.upload {
			// One tarball per job, holding what THIS job made and nothing
			// else. The mark is what draws that line: a carried file keeps
			// the modification time it was built with, because tar restores
			// it, so anything newer than the mark was written here.
			//
			// Packing the whole directory instead is the trap. Every job
			// unpacks what every job before it kept, so a job that repacked
			// the lot published its inheritance again under its own name,
			// and the job after that inherited every copy. The traffic grows
			// with the square of the jobs, and on a run of thirteen the
			// packing and shipping came to more than the work.
			//
			// It is a tarball rather than loose files because the platform's
			// artifact format is a zip, and a zip does not carry a unix
			// mode: a binary uploaded loose comes back unexecutable and the
			// stage that has to run it dies on "permission denied". It is
			// gzipped because these are binaries and the upload is billed by
			// the byte on a private repo.
			fmt.Fprintf(b, "\n      - name: Pack what this job built\n        id: pack\n        run: |\n          if [ -d %s ]; then\n            find %s -type f -newer %s -printf '%%P\\n' > %s\n          else\n            : > %s\n          fi\n          if [ -s %s ]; then\n            mkdir -p %s\n            tar -czf %s/built-%s.tar.gz -C %s -T %s\n            echo packed=true >> \"$GITHUB_OUTPUT\"\n          else\n            echo packed=false >> \"$GITHUB_OUTPUT\"\n          fi\n",
				artifactDir, artifactDir, packMark, packList, packList, packList, carriedDir, carriedDir, j.id, artifactDir, packList)
			// A job that built nothing skips the upload outright. Most jobs
			// build nothing - a gate, a publish, a lint - and the step costs
			// the same whether or not it finds a file.
			fmt.Fprintf(b, "\n      - name: Keep what this job built for the jobs after it\n        if: steps.pack.outputs.packed == 'true'\n        uses: actions/upload-artifact@v7\n        with:\n          name: built-${{ github.run_id }}-%s\n          path: %s/built-%s.tar.gz\n          retention-days: 7\n", j.id, carriedDir, j.id)
		}

		if j.push {
			fmt.Fprintf(b, "\n      - name: Push what the pipeline wrote\n        run: |\n          cd %s\n          git push origin HEAD:%s\n",
				spec.Dir, spec.Ref)
		}

		writeStoreSave(b)
		writeFailureReport(b, w)
	}
}

// writeCommandEnv renders the env block the command step needs: the
// token, the workflow's own secret, and the dispatch payload fields.
func writeCommandEnv(b *strings.Builder, w WorkflowSpec) {
	if len(w.PayloadEnv) == 0 && !w.Token && w.Secret == "" {
		return
	}

	b.WriteString("        env:\n")

	if w.Token {
		b.WriteString("          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}\n")
	}

	if w.Secret != "" {
		fmt.Fprintf(b, "          %s: ${{ secrets.%s }}\n", w.Secret, w.Secret)
	}

	for _, field := range w.PayloadEnv {
		fmt.Fprintf(b, "          %s: ${{ github.event.client_payload.%s }}\n",
			strings.ToUpper(field), field)
	}
}

// quoteList renders a YAML flow list of quoted strings, so a glob like
// "*.md" survives the parser.
func quoteList(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, item := range items {
		quoted = append(quoted, `"`+strings.ReplaceAll(item, `"`, `\"`)+`"`)
	}

	return strings.Join(quoted, ", ")
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
	writeStoreRestore(&b)
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

	writeStoreSave(&b)

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
	writeStoreRestore(&b)
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
	writeStoreSave(&b)

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
// writeStoreRestore brings back the tool store a previous run saved, so
// the bootstrap reuses installed runtimes and toolchain binaries instead
// of downloading them again. The restore key is a prefix: the newest saved
// store for this OS wins whatever it holds, because the store converges on
// its own - an entry that no longer matches its description is rebuilt and
// a missing one installs - so a stale cache costs one rebuild, never a lie.
// This is the engine's own behavior, not a knob, for the same reason the
// concurrency group is: every workspace-building job benefits identically
// and a forgotten declaration would silently cost minutes per run.
func writeStoreRestore(b *strings.Builder) {
	b.WriteString(`
      - name: Restore the tool store
        id: tool-store
        uses: actions/cache/restore@v6
        with:
          path: ~/.cache/forge
          key: tool-store-${{ runner.os }}-${{ github.run_id }}
          restore-keys: |
            tool-store-${{ runner.os }}-
`)
}

// writeStoreSave saves the tool store under a key derived from its own
// content, and only when this job's store is not the one the restore already
// found. It runs whatever the pipeline concluded: the store a red run
// installed is just as reusable as a green one's.
//
// The condition is the whole point. A save whose key already exists is
// rejected by the cache service, but only AFTER the action has archived the
// path to find that out - half a gigabyte of tar and zstd, measured at 60 to
// 70 seconds, in every job of every run, to upload nothing. Comparing the
// content key with the key the restore matched skips the archive as well as
// the upload, and a job that did install something still saves, because its
// key no longer matches what it started from.
func writeStoreSave(b *strings.Builder) {
	b.WriteString(`
      - name: Name the tool store by its content
        id: tool-store-key
        if: always()
        run: |
          echo "key=$(find ~/.cache/forge -mindepth 1 -maxdepth 4 2>/dev/null | sort | sha256sum | cut -c1-16)" >> "$GITHUB_OUTPUT"
          echo "present=$([ -d ~/.cache/forge ] && echo true || echo false)" >> "$GITHUB_OUTPUT"

      - name: Save the tool store
        if: always() && steps.tool-store-key.outputs.present == 'true' && steps.tool-store.outputs.cache-matched-key != format('tool-store-{0}-{1}', runner.os, steps.tool-store-key.outputs.key)
        uses: actions/cache/save@v6
        with:
          path: ~/.cache/forge
          key: tool-store-${{ runner.os }}-${{ steps.tool-store-key.outputs.key }}
`)
}

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
