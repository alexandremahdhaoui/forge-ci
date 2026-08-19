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
| `apply` | Makes everything match the definition. This is the reconcile. |
| `status` | Reads without changing. |
| `graph` | Renders the pipeline and its live state as mermaid. |
| `poll` | Asks the triggers whether anything moved. |
| `validate` | Checks the file and stops. |

`apply` is idempotent, so triggers are dumb. A webhook, a cron and your
keyboard all call the same thing and a duplicate call costs nothing.

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
manual, once. After that a stage can run `forge-ci bootstrap` to reconcile the
pipeline from its own definition.

That stage must use `bootstrap`, never `apply`. Apply inside apply recurses, and
forge-ci refuses it by name.

Put it after the stage that proves the revision. Then a broken pipeline file
breaks the next reconcile, not the running one.

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
spec via `go://forge-dev`, and forge-ci hand-rolled it because forge-dev has no
engine type that fits. See the workspace `FOLLOWUP.md`.
