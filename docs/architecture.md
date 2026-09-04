# Architecture

The hand written guide. Everything else under `docs/` is generated.

## Three layers

| Layer | Holds | Lives in |
|---|---|---|
| Definition | what you want | `forge-ci.yaml` |
| Engines | what makes it happen | binaries named by URI |
| State | what is true now | a state repo |

forge answers what the targets are and how to build them, in one repo, on one
machine. forge-ci answers which repos at which commits form a release, what
runs where, and what gets promoted. It calls forge for the rest.

## Verbs

| Verb | Does |
|---|---|
| `bootstrap` | Creates the minimum that lets the loop run. Once, by hand. |
| `apply` | Makes everything match the definition. This is the reconcile, and it drives the stages. |
| `status` | Reads without changing. |
| `graph` | Renders the pipeline and its live state as mermaid. |
| `poll` | Asks the triggers whether anything moved. |
| `validate` | Checks the file and stops. |
| `release` | Tags one commit somebody named and publishes its release. What the generated release workflow runs. |

`apply` is idempotent, so triggers are dumb. A webhook, a cron and your
keyboard all call the same thing and a duplicate call costs nothing.

`apply --phase <name>` runs one part of the loop: `self-reconcile`,
`evaluate` or `stages`. Each phase reads and writes state, so the next can
run in another process. A compute engine renders the phases as jobs, so a
run reads as what it is. The `evaluate` phase is where a revision with
nothing to release ends: already released, no new commit in the release
set, only commits of a kind that never releases, or the same code as the
last release. It answers one word, `skip` or `proceed`, and the phases
after it run only on `proceed`. It also mints the revision, so every job of
the run answers to one identity from the first stage to the last; whether
those commits were PROVEN is what the run records and the release say.

There is no release phase. A release is a substage that names an artifact
engine, so it runs where the pipeline puts it, under `stages` like anything
else. Substages of one stage run at the same time, so a release goes in a
stage of its own after the one that builds: stage order is what holds it
behind a green build, and the loop stops at the first stage that does not
advance.

The `stages` phase cuts further. `--stage <name>` runs one stage, and
refuses until the stage before it has advanced. `--stage <name> --substage
<name>` runs one substage and its gates and decides nothing. That is what
lets a compute engine render one job per stage, or one per substage.

Whether the stage in front advanced is the promotion's answer, and every
stage job asks it: a state read of that stage's substage records and one
engine call. Nothing between two stages has to run for the second to know
where it stands, and nothing is recorded for a stage as a whole.

Substages of one stage run at the same time unless one says otherwise.
`needs:` names the substages of the same stage that must advance before
this one runs; the stage runs its substages in the waves that graph gives
(`citypes.Waves`, the same walk that orders repos), a substage whose need
did not advance is not run, and the stage does not advance. That is the one
ordering a stage carries. Before it, two writes that must not race had to
live in stages of their own and the stage list stopped describing the
pipeline.

A run proves one set of commits, and only its first phase resolves them.
The `evaluate` phase records the revision beside its release decision and
reports it on its first line; every phase after it is handed that id with
`--revision`, and reads the revision from the record rather than measuring
its own checkout. Without that, each phase in its own process answers for
whatever the repos hold when it clones them - and a stage that publishes
into one of the pipeline's own repos moves one of them halfway through the
run, so the phases after it would look for records under an id nothing
wrote. A record for another revision is refused; a record from before the
revision travelled has none, and the phase resolves its own, so an apply
in flight over an upgrade finishes the way it started.

Every rendered job carries a title: derived from the stage and substage
names, unless the stage or substage declares a `displayName`, which is used
verbatim.

Under `unmatched: error` only the newest commit of each release repo has
to match a list. An older commit that matches none scores patch. History
is never rewritten, so a bad message is fixed by a good commit on top, and
the run that follows releases.

forge-ci's own commit, the one a manager writes when it reconciles the CI
resources, reads `forge-ci: self reconcile`. The prefix is the pipeline's
`versioning.selfReconcileCommitPrefix`, default `forge-ci:`, and the
release decision scores a commit starting with it as a patch unless a
semantic list names the same prefix.

## The loop

```
read the definition
ask every engine what resources it needs
hand those to the manager that owns them
resolve the revision from the repo SHAs
for each stage, in order:
    for each substage:
        skip it if it already passed
        run its targets on its compute engine
        evaluate its gates
        record the run
    ask the promotion whether to advance
    stop if it says no
```

A passed substage is never run again. A failed one is retried next apply. That
is what makes the loop safe on a timer.

## What a revision covers

A revision hashes each repo's HEAD **and** a hash of its uncommitted changes. So
an edit you have not committed is its own revision and it reruns.

That is deliberate. Keying only on the commit meant an uncommitted break was
invisible: the substage was already recorded as passed, so the pipeline reported
green on code that did not compile.

The price is that a repo must gitignore its own build output. If it does not, the
tree is dirty after every run, so every apply is a new revision and the loop
never settles.

## Ports

```
forge-ci
   +-- compute    ci-compute-local    runs forge, harvests the artifact store
   +-- state      ci-state-git        reads and writes a state repo
   +-- trigger    ci-trigger-watch    fingerprints the watched repos
   +-- gate       ci-gate-manual      waits for an approval file
   +-- promotion  ci-promotion-all    advances on a pass threshold
   +-- manager    ci-manager-local    creates directories and files
                  ci-manager-dryrun   records intent, changes nothing
```

Engines declare, managers realize. An engine says it needs a directory. It never
says how, and it never names a manager kind. That is what lets you swap
Terraform for CDK without touching an engine.

## Ownership, and why apply refuses

forge-ci records which manager owns which resource and hands that record back on
the next apply. If a resource is owned by `local` and the pipeline now says
`dryrun`, the manager refuses.

That is the only honest answer. No IaC tool can adopt another one's state, so
the choice is stopping loudly or paying for two of everything.

## Bootstrap

Something has to run `apply`, and it cannot create itself. So one command is
manual, once. After that every `apply` reconciles the pipeline's own
resources before its first stage, settles what it changed with a scoped
push, and stops superseded: the run that push fires starts from the
corrected state. There is no self stage. One ran `forge-ci bootstrap` for a
while, before the reconcile moved to the top of apply, and it only
duplicated what every run already does.

A stage that runs `forge-ci apply` is the loop calling itself, and forge-ci
refuses it by name.

**apply must never delete the trigger or the seed.** Break that and a bad
pipeline file leaves you back at the laptop.

## Layout

```
cmd/                       one binary per engine, plus forge-ci and docgen
pkg/config                 the pipeline schema and its validation
pkg/citypes                the types every engine agrees on
pkg/cienginekit            CLI and MCP from one tool definition
internal/adapter           git, exec, filesystem, MCP, the forge artifact store
internal/controller        the reconcile loop and one controller per port
internal/driver/clidriver  argument parsing and reporting
internal/mocks             generated, never edited
```

An adapter declares the interface it satisfies. A controller accepts it. A
driver holds no logic. main wires them and starts a driver.

`pkg/cienginekit` is a known divergence. forge generates this from an OpenAPI
spec via `forge://forge-dev`, and forge-ci hand-rolled it because forge-dev has no
engine type that fits. See the workspace `FOLLOWUP.md`.
