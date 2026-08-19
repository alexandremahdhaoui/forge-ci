# Architecture

forge-ci has three layers. Read them as want, do, and is.

| Layer | Holds | Where it lives |
|---|---|---|
| Definition | what you want | `pipeline.yaml` |
| Engines | what makes it happen | binaries named by URI |
| State | what is true now | a state repo |

forge answers what the targets are and how to build them, in one repo, on one
machine. It does not answer which repos at which commits form a release, what
runs where, or what gets promoted. forge-ci answers those and calls forge for
the rest.

## Two verbs

| Verb | Does |
|---|---|
| `forge-ci bootstrap` | Creates the minimum that lets the loop run. Once, by hand. |
| `forge-ci apply` | Makes everything match the definition. This is the reconcile. |

`apply` is idempotent. Run it twice and the second run does nothing, because
nothing is outstanding. That is why triggers are dumb. A webhook, a cron and
your keyboard all call the same thing, and a duplicate call costs nothing.

`status` reads without changing anything. `poll` asks the triggers whether
anything moved. `validate` checks the file and stops.

## The reconcile loop

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

A substage that already passed is never run again. A substage that failed is
retried on the next apply. That is what makes the loop safe to call on a timer.

## The ports

Everything pluggable is a binary named by a `go://` or `alias://` URI, spoken
to over MCP. That is forge's own mechanism, so there is no registry, no plugin
loader and no server to run.

```
forge-ci
   |
   +-- compute    ci-compute-local      runs forge, harvests the artifact store
   +-- state      ci-state-git          reads and writes a state repo
   +-- trigger    ci-trigger-watch      fingerprints the watched repos
   +-- gate       ci-gate-manual        waits for an approval file
   +-- promotion  ci-promotion-all      advances on a pass threshold
   |
   +-- manager    ci-manager-local      creates directories and files
                  ci-manager-dryrun     records intent, changes nothing
```

Engines declare, managers realize. An engine says "I need this directory". It
never says how, and it never names a manager kind. That is what lets you swap
Terraform for CDK without touching a single engine.

## Ownership, and why apply can refuse

forge-ci records which manager owns which resource. On the next apply it hands
that record to the manager.

If a resource is recorded as owned by `local` and the pipeline now says
`dryrun`, the manager refuses and says so. It does not create a second copy
beside the first.

That is the only honest answer. No IaC tool can adopt another one's state, so
the choice is between stopping loudly and silently paying for two of
everything.

## Bootstrap

Something has to run `apply`, and that something cannot create itself. So one
command is manual, once.

```sh
forge-ci bootstrap
```

After that the pipeline updates itself, because `apply` is a target like any
other and a stage can run it.

```yaml
targets:
  - alias: self-apply
    forgeCI: apply

stages:
  - name: self
    substages:
      - { name: default, engine: here, manager: local, targets: [self-apply] }
```

Put that stage after the one that proves the revision. Then a broken pipeline
file breaks the next reconcile, not the running one, and you can still fix it.

**apply must never delete the trigger or the seed.** Everything else it may
replace. Break that and a bad pipeline file leaves you back at the laptop.

## Layout

```
cmd/                       one binary per engine, plus forge-ci
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
