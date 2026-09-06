package workflowcontroller

import (
	"errors"
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
	spec = spec.withDefaults()

	// A cache keyed by repos hashes the pipeline's repos, and a pipeline
	// that declares none has nothing to hash: refused rather than rendered
	// as a key over nothing that every run shares.
	if len(cachesKeyed(spec, CacheKeyRepos)) > 0 && len(spec.Repos) == 0 {
		return nil, errors.New("a cache keyed by repos needs the pipeline to declare repos, and it declares none")
	}

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

	writeOn(&b, w.On)

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
	fmt.Fprintf(&b, "\nconcurrency:\n  group: %s\n  cancel-in-progress: %t\n",
		spec.Concurrency.Group, spec.Concurrency.CancelInProgress)

	if spec.Jobs != JobsWhole {
		jobs, err := phasedJobs(spec, w)
		if err != nil {
			return "", err
		}

		writePhasedJobs(&b, spec, w, jobs)

		return b.String(), nil
	}

	writeRunsOn(&b, spec, job)
	writePrelude(&b, spec, w.Secret)

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

	writeContentCachesSave(&b, spec)
	writeFailureReport(&b, spec, w)

	return b.String(), nil
}

// writeOn renders what starts a command workflow. A cron and a dispatch
// event both take workflow_dispatch as well, always: a workflow only a
// schedule or another repo can fire cannot be tested by the person who just
// changed it, and an untestable workflow is how one of these went eight
// days without completing a single run.
func writeOn(b *strings.Builder, on OnSpec) {
	if on.Cron != "" {
		fmt.Fprintf(b, "  schedule:\n    - cron: \"%s\"\n  workflow_dispatch: {}\n", on.Cron)
	}

	if len(on.Events) > 0 {
		fmt.Fprintf(b, "  repository_dispatch:\n    types: [%s]\n  workflow_dispatch: {}\n",
			strings.Join(on.Events, ", "))
	}

	// A push to the repo the workflow lives on. Nothing dispatches when a
	// factory's own workspace files change, so its pipeline asks for this or
	// an edit to the pipeline never runs the pipeline. A push that touched
	// only the ignored paths never starts the run: the platform's own
	// filter, blind to what a file is for.
	if on.Push != nil {
		fmt.Fprintf(b, "  push:\n    branches: [%s]\n", strings.Join(on.Push.Branches, ", "))

		if len(on.Push.IgnorePaths) > 0 {
			fmt.Fprintf(b, "    paths-ignore: [%s]\n", quoteList(on.Push.IgnorePaths))
		}
	}
}

// writePrelude is what every job that builds a workspace does first: the
// setup actions, the content-keyed caches, the checkout, and the toolchain
// script with its repos-keyed caches around it. One function, so the whole
// apply, the phased jobs, the release and the runner cannot drift into one
// that works and one that does not.
func writePrelude(b *strings.Builder, spec Spec, secret string) {
	writeSetup(b, spec)
	writeContentCachesRestore(b, spec)
	writeWorkspaceCheckout(b, spec, secret)
	writeToolchain(b, spec)
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
	// uses names the jobs whose kept files this one brings back, when the
	// substages it runs declared what they read. Empty with download set
	// brings back everything the run has kept so far.
	uses   []string
	upload bool
	// gated says the job runs only on proceed: everything after evaluate.
	gated bool
	// push says the workflow's push step follows the command, for a
	// pipeline whose stages write into its own checkout.
	push bool
	// capture is the phase whose one-word answer this job publishes as its
	// `outcome` output: the last `<phase>: <word>` line of the report. The
	// two fixed jobs set it; a stage job answers nothing a later job reads.
	capture string
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
			capture: jobSelfReconcile,
		},
		{
			id: jobEvaluate, name: "Evaluate next steps",
			step: "Evaluate next steps", flags: "--phase " + jobEvaluate,
			needs: []string{jobSelfReconcile}, capture: jobEvaluate,
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
				download: len(prev) > 0, uses: usedJobs(spec, stageUses(stage)), upload: true, push: w.Push,
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
				download: len(prev) > 0 || len(sub.Needs) > 0, uses: usedJobs(spec, sub.Uses),
				upload: true, push: w.Push,
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

// stageUses is what a stage's substages declared they read: the union of
// their uses, or nil when any declared none - that one may read anything,
// so the stage brings back everything.
func stageUses(stage citypes.DeclaredStage) []string {
	out := []string{}

	for _, sub := range stage.Substages {
		if len(sub.Uses) == 0 {
			return nil
		}

		out = append(out, sub.Uses...)
	}

	return out
}

// usedJobs maps <stage>/<substage> pairs to the ids of the jobs that kept
// their files: the stage's job at stage granularity, the substage's own
// at substage granularity. The names are what the artifacts were kept
// under, so the download names exactly them.
func usedJobs(spec Spec, uses []string) []string {
	if len(uses) == 0 {
		return nil
	}

	ids := make([]string, 0, len(uses))

	for _, u := range uses {
		stage, sub, _ := strings.Cut(u, "/")

		if spec.Jobs == JobsPerSubstage {
			ids = append(ids, stage+"-"+sub)
		} else {
			ids = append(ids, stage)
		}
	}

	return dedupe(ids)
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

// writePhasedJobs renders one apply as jobs. Each of the two fixed jobs
// answers one word on its last line, captured as its output. The self
// reconcile answers superseded or converged: a whole apply that superseded
// itself stops in-process, and the evaluate job is what makes the phased
// run stop the same way - it runs only when the reconcile converged, so the
// run the settle's push fired is the one that carries the work. Without
// that gate the evaluate job ran on the superseded state, released, and
// the superseding run released again (forty-one times on one pipeline).
// The evaluate job answers skip or proceed; every job after it runs only on
// proceed, so a run with nothing to release shows them skipped rather than
// green, and a skipped evaluate has no outputs, so they skip then too.
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

		if j.id == jobEvaluate {
			fmt.Fprintf(b, "    if: needs.%s.outputs.outcome == '%s'\n", jobSelfReconcile, "converged")
		}

		if j.gated {
			fmt.Fprintf(b, "    if: needs.%s.outputs.outcome == '%s'\n", jobEvaluate, "proceed")
		}

		if j.capture != "" {
			b.WriteString("    outputs:\n      outcome: ${{ steps.phase.outputs.outcome }}\n")
		}

		if j.id == jobEvaluate {
			b.WriteString("      revision: ${{ steps.phase.outputs.revision }}\n")
		}

		fmt.Fprintf(b, "    runs-on: %s\n", spec.RunsOn)

		if spec.image != "" {
			fmt.Fprintf(b, "    container:\n      image: %s\n", spec.image)
		}

		b.WriteString("    steps:\n")

		writePrelude(b, spec, w.Secret)

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
			if len(j.uses) == 0 {
				fmt.Fprintf(b, "\n      - name: Bring back what the jobs before this one built\n        uses: %s\n        with:\n          pattern: built-${{ github.run_id }}-*\n          merge-multiple: true\n          path: %s\n", spec.Actions.DownloadArtifact, carriedDir)
			}

			// A substage that declared what it reads brings back exactly
			// that, by the name the job kept it under. Still a pattern, so
			// a job that built nothing and kept nothing is a no-op here
			// rather than a missing artifact.
			for _, used := range j.uses {
				fmt.Fprintf(b, "\n      - name: Bring back what %s built\n        uses: %s\n        with:\n          pattern: built-${{ github.run_id }}-%s\n          merge-multiple: true\n          path: %s\n", used, spec.Actions.DownloadArtifact, used, carriedDir)
			}

			fmt.Fprintf(b, "\n      - name: Unpack it\n        run: |\n          mkdir -p %s\n          for f in %s/*%s; do\n            [ -e \"$f\" ] || continue\n            tar -x%sf \"$f\" -C %s\n          done\n", artifactDir, carriedDir, tarExt(spec), tarZ(spec), artifactDir)
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

		switch {
		case j.id == jobEvaluate:
			// Two lines of the report are contracts. The last is the
			// word, captured rather than parsed from the middle because a
			// report can say anything before it. The first is the revision
			// this run proves, which every job after this one is told, so
			// that a job whose own checkout has moved on - a stage of this
			// run publishes into one of the pipeline's own repos - still
			// answers for the commits the run started with.
			writeIndented(b, "out=$("+command+")\n"+
				"printf '%s\\n' \"$out\"\n"+
				"echo \"outcome=$(printf '%s\\n' \"$out\" | sed -n 's/^"+j.capture+": //p' | tail -n 1)\" >> \"$GITHUB_OUTPUT\"\n"+
				"echo \"revision=$(printf '%s\\n' \"$out\" | sed -n 's/^revision //p' | head -n 1)\" >> \"$GITHUB_OUTPUT\"")
		case j.capture != "":
			// One line is the contract: the last `<phase>: <word>`.
			writeIndented(b, "out=$("+command+")\n"+
				"printf '%s\\n' \"$out\"\n"+
				"echo \"outcome=$(printf '%s\\n' \"$out\" | sed -n 's/^"+j.capture+": //p' | tail -n 1)\" >> \"$GITHUB_OUTPUT\"")
		default:
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
			// stage that has to run it dies on "permission denied".
			// Whether it is gzipped is the spec's carry.compression:
			// binaries compress and the upload is billed by the byte, while an
			// image layout is blobs that are already gzip and spends a minute
			// compressing nothing.
			fmt.Fprintf(b, "\n      - name: Pack what this job built\n        id: pack\n        run: |\n          if [ -d %s ]; then\n            find %s -type f -newer %s -printf '%%P\\n' > %s\n          else\n            : > %s\n          fi\n          if [ -s %s ]; then\n            mkdir -p %s\n            tar -c%sf %s/built-%s%s -C %s -T %s\n            echo packed=true >> \"$GITHUB_OUTPUT\"\n          else\n            echo packed=false >> \"$GITHUB_OUTPUT\"\n          fi\n",
				artifactDir, artifactDir, packMark, packList, packList, packList, carriedDir, tarZ(spec), carriedDir, j.id, tarExt(spec), artifactDir, packList)
			// A job that built nothing skips the upload outright. Most jobs
			// build nothing - a gate, a publish, a lint - and the step costs
			// the same whether or not it finds a file.
			fmt.Fprintf(b, "\n      - name: Keep what this job built for the jobs after it\n        if: steps.pack.outputs.packed == 'true'\n        uses: %s\n        with:\n          name: built-${{ github.run_id }}-%s\n          path: %s/built-%s%s\n          retention-days: %d\n", spec.Actions.UploadArtifact, j.id, carriedDir, j.id, tarExt(spec), spec.Carry.Retention)
		}

		if j.push {
			fmt.Fprintf(b, "\n      - name: Push what the pipeline wrote\n        run: |\n          cd %s\n          git push origin HEAD:%s\n",
				spec.Dir, spec.Ref)
		}

		writeContentCachesSave(b, spec)
		writeFailureReport(b, spec, w)
	}
}

// tarExt is the carried tarball's extension, and tarZ the tar flag that
// makes it: gzip or nothing, as carry.compression says.
func tarExt(spec Spec) string {
	if spec.Carry.Compression == CompressionNone {
		return ".tar"
	}

	return ".tar.gz"
}

func tarZ(spec Spec) string {
	if spec.Carry.Compression == CompressionNone {
		return ""
	}

	return "z"
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
func writeFailureReport(b *strings.Builder, spec Spec, w WorkflowSpec) {
	if !w.ReportFailure {
		return
	}

	title := strings.ReplaceAll(spec.FailureReport.Title, "{{.Workflow}}", w.Name)

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
          TITLE: "%s"
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
            -d "{\"title\":\"$TITLE\",\"body\":\"%s\"}"
`, title, spec.FailureReport.Body)
}

// renderFanOut tells every consumer about a new tag through a
// repository_dispatch.
func renderFanOut(spec Spec, w WorkflowSpec) string {
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
    runs-on: %s
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
`, w.Name, tag, spec.RunsOn, strings.Join(w.Consumers, ", "), w.Secret, apiBase, w.EventType)

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
	writePrelude(&b, spec, w.Secret)

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

	writeContentCachesSave(&b, spec)

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
	writePrelude(&b, spec, spec.Runner.Secret)
	b.WriteString(`
      - name: Run the targets
        run: |
`)

	if spec.Runner.SetupScript != "" {
		writeIndented(&b, spec.Runner.SetupScript)
	}

	b.WriteString("          ${{ inputs.script }}\n")
	writeContentCachesSave(&b, spec)

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
// actionAt is one of a cache action's entry points: actions/cache@v6 with
// "restore" is actions/cache/restore@v6.
func actionAt(action, entry string) string {
	if i := strings.LastIndex(action, "@"); i >= 0 {
		return action[:i] + "/" + entry + action[i:]
	}

	return action + "/" + entry
}

// display is a cache's name as a person reads it in a step title.
func display(name string) string {
	return strings.ReplaceAll(name, "-", " ")
}

// writeContentCachesRestore brings back every content-keyed cache a
// previous run saved, before the bootstrap, so it reuses what an earlier
// run installed instead of downloading it again. The restore key is a
// prefix: the newest saved cache for this OS wins whatever it holds, which
// is right for a store that converges on its own - an entry that no longer
// matches its description is rebuilt and a missing one installs - so a
// stale cache costs one rebuild, never a lie.
func writeContentCachesRestore(b *strings.Builder, spec Spec) {
	for _, cache := range cachesKeyed(spec, CacheKeyContent) {
		fmt.Fprintf(b, `
      - name: Restore the %s
        id: %s
        uses: %s
        with:
          path: %s
          key: %s-${{ runner.os }}-${{ github.run_id }}
          restore-keys: |
            %s-${{ runner.os }}-
`, display(cache.Name), cache.Name, actionAt(spec.Actions.Cache, "restore"),
			pathList(cache.Paths), cache.Name, cache.Name)
	}
}

// writeContentCachesSave saves every content-keyed cache under a key
// derived from its own content, and only when this job's copy is not the
// one the restore already found. It runs whatever the pipeline concluded:
// the store a red run installed is just as reusable as a green one's.
//
// The condition is the whole point. A save whose key already exists is
// rejected by the cache service, but only AFTER the action has archived the
// path to find that out - half a gigabyte of tar and zstd, measured at 60 to
// 70 seconds, in every job of every run, to upload nothing. Comparing the
// content key with the key the restore matched skips the archive as well as
// the upload, and a job that did install something still saves, because its
// key no longer matches what it started from.
func writeContentCachesSave(b *strings.Builder, spec Spec) {
	for _, cache := range cachesKeyed(spec, CacheKeyContent) {
		first := cache.Paths[0]

		fmt.Fprintf(b, `
      - name: Name the %s by its content
        id: %s-key
        if: always()
        run: |
          echo "key=$(find %s -mindepth 1 -maxdepth 4 2>/dev/null | sort | sha256sum | cut -c1-16)" >> "$GITHUB_OUTPUT"
          echo "present=$([ -d %s ] && echo true || echo false)" >> "$GITHUB_OUTPUT"

      - name: Save the %s
        if: always() && steps.%s-key.outputs.present == 'true' && steps.%s.outputs.cache-matched-key != format('%s-{0}-{1}', runner.os, steps.%s-key.outputs.key)
        uses: %s
        with:
          path: %s
          key: %s-${{ runner.os }}-${{ steps.%s-key.outputs.key }}
`, display(cache.Name), cache.Name, strings.Join(cache.Paths, " "), first,
			display(cache.Name), cache.Name, cache.Name, cache.Name, cache.Name,
			actionAt(spec.Actions.Cache, "save"), pathList(cache.Paths), cache.Name, cache.Name)
	}
}

// pathList renders a cache's paths as the action's path input: one path
// inline, several as a block.
func pathList(paths []string) string {
	if len(paths) == 1 {
		return paths[0]
	}

	var b strings.Builder

	b.WriteString("|")

	for _, p := range paths {
		b.WriteString("\n            " + p)
	}

	return b.String()
}

// cachesKeyed is the spec's caches with one key kind, in the order
// declared.
func cachesKeyed(spec Spec, key string) []CacheSpec {
	if spec.Caches == nil {
		return nil
	}

	out := []CacheSpec{}

	for _, cache := range *spec.Caches {
		if cache.Key == key {
			out = append(out, cache)
		}
	}

	return out
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
	fmt.Fprintf(b, "\njobs:\n  %s:\n    runs-on: %s\n", job, spec.RunsOn)

	if spec.image != "" {
		fmt.Fprintf(b, "    container:\n      image: %s\n", spec.image)
	}

	b.WriteString("    steps:\n")
}

// writeToolchain renders the instance's verbatim toolchain script, with the
// repos-keyed caches restored before it and saved after it.
// forge-ci names no toolchain; the words are the spec's.
//
// No script means the job's container already carries the toolchain, and the
// step goes rather than being emitted empty: a `run: |` with nothing under it
// is not valid YAML. A repos-keyed cache still renders around the place the
// script would be.
func writeToolchain(b *strings.Builder, spec Spec) {
	caches := cachesKeyed(spec, CacheKeyRepos)

	for _, cache := range caches {
		writeReposCacheRestore(b, spec, cache)
	}

	if spec.Workspace.ToolchainScript != "" {
		b.WriteString(`
      - name: Install the toolchain from the workspace
        run: |
`)
		writeIndented(b, spec.Workspace.ToolchainScript)
	}

	for _, cache := range caches {
		writeReposCacheSave(b, spec, cache)
	}
}

// writeReposCacheRestore brings back what an earlier job of the same
// checkout saved. The key is the HEAD of every repo the pipeline declares,
// hashed after the checkout, and it is exact: a toolchain built from the
// members is a function of their shas, so a different set rebuilds and a
// prefix match would hand a job the toolchain of some other commit. The
// script still runs on a hit - it is the instance's own words and this
// engine cannot split it - and finds its work already done.
//
// The repos are the ones the pipeline declares, handed in at declare time,
// so every job of a run computes one key: a hash over every directory under
// the root would move whenever a stage wrote into a repo the revision does
// not cover, and the jobs after it would each save a copy.
func writeReposCacheRestore(b *strings.Builder, spec Spec, cache CacheSpec) {
	names := make([]string, 0, len(spec.Repos))
	for _, r := range spec.Repos {
		names = append(names, r.Name)
	}

	fmt.Fprintf(b, `
      - name: Name the %s by the members it is built from
        id: %s-key
        run: |
          echo "key=$(for d in %s; do git -C "$d" rev-parse HEAD; done | sha256sum | cut -c1-16)" >> "$GITHUB_OUTPUT"

      - name: Restore the %s
        id: %s-cache
        uses: %s
        with:
          path: |
`, display(cache.Name), cache.Name, strings.Join(names, " "), display(cache.Name), cache.Name,
		actionAt(spec.Actions.Cache, "restore"))

	for _, p := range cache.Paths {
		b.WriteString("            " + p + "\n")
	}

	fmt.Fprintf(b, "          key: %s-${{ runner.os }}-${{ steps.%s-key.outputs.key }}\n", cache.Name, cache.Name)
}

// writeReposCacheSave keeps what the script installed, only on a miss: a
// hit already holds these bytes under this key, and archiving them again
// costs the tar for nothing.
func writeReposCacheSave(b *strings.Builder, spec Spec, cache CacheSpec) {
	fmt.Fprintf(b, `
      - name: Save the %s
        if: steps.%s-cache.outputs.cache-hit != 'true'
        uses: %s
        with:
          path: |
`, display(cache.Name), cache.Name, actionAt(spec.Actions.Cache, "save"))

	for _, p := range cache.Paths {
		b.WriteString("            " + p + "\n")
	}

	fmt.Fprintf(b, "          key: %s-${{ runner.os }}-${{ steps.%s-key.outputs.key }}\n", cache.Name, cache.Name)
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
