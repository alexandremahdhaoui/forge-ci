# Decisions

Why forge-ci is shaped this way. Each entry says what was decided, why, and
what it costs.

Three of these reversed a position argued at length. Those are first, because
the argument is the content.

## The three reversals

### Webhooks, not polling

**Decided.** A push webhook hits an endpoint you own. That endpoint dispatches
only when there is real work. No cron poll.

**Why.** Polling was argued twice, on the grounds that it debounces for free. A
poll that finds three pushes at once builds one tuple, so simultaneous commits
stop being a problem you solve.

The numbers killed it. GitHub bills each job rounded up to a minute. A 15
minute poll is 2880 minutes a month against a 2000 minute free tier, so
polling costs more than the whole budget before a single build runs. A webhook
is not an Actions job at all. It is an HTTP POST. Idle costs zero, it fires in
seconds, and it works on private repos.

**The debounce argument did not survive on merit. It became moot.** Once
`apply` was idempotent, a duplicate trigger cost nothing, so the one thing
polling was good at stopped mattering.

**Costs.** An endpoint to own and a signing secret to hold. Exactly what
polling avoided.

### Engines declare, managers realize

**Decided.** An engine declares the resources it needs. A manager makes them
exist. An engine never names a manager kind.

**Why.** The argument against a manager layer was that it forces forge-ci to
know the word Terraform, which breaks the rule that nothing is hardcoded.

That argument was wrong, and the test is orthogonality. `ci-compute-lambda`
needs a Lambda. `ci-state-dynamodb` needs a table. Both want Terraform and
neither is about Terraform. Two engines wanting the same realizer is what earns
a layer.

The neutral resource list is what keeps forge-ci ignorant. It says "a
directory named X". It never says how.

**Costs.** A neutral resource description has to be designed and versioned.
Keep it tiny or you are rebuilding Crossplane. Two layouts of the same IaC tool
become two managers.

### Stage and substage, after two overshoots

**Decided.** A stage holds substages. Exactly one inner level.

**Why.** First came a four level taxonomy of stage, wave, cell and step. Too
complicated and on the wrong layer. Then the inner level was deleted entirely,
which would have forced stage names like `prod-eu-west-1-cell-a`. That is
unmaintainable, and it was the right thing to reject.

**The most transferable lesson is why `cell` lost as a name.** A CI word that
also means something at the deployment layer will collide. If the pipeline has
cells and your dataplane has cells inside regions inside a stage, the word
means two things one layer apart.

`placement` and `site` read badly. `target` is forge's already.

**Costs.** Two levels to learn. State and logs are keyed by stage and substage.

## The pipeline

### An ordered list, not a graph

**Decided.** Stages run in the order you wrote them. There is no `dependsOn`
and no `needs`.

**Why.** A pipeline runs left to right, from the trigger through the build and
the self reconcile to the last production stage. A dependency graph is
complexity nobody asked for. forge's own target list has no inter stage
dependency either, so a graph would be a concept forge does not have.

**Costs.** No fan-in across stages. Anything concurrent is substages inside one
stage.

**This is also why `cmd/ci-orchestrator` in forge is retired rather than
extended.** Its sketch committed to `dependsOn`. Its four tenets survive:
accessible on one computer, reproducible locally, vendor agnostic, no defaults
and no side effects.

### Gates run after, never before

**Decided.** A gate judges an outcome. There is no `when: before`.

**Why.** Operability, not purity. A gate before a substage produces "why is my
CI not progressing", answered by "the gate is before the substage, not after".
The promotion is what holds the stage anyway.

**Costs.** Approval immediately before a risky deploy has to be expressed one
substage earlier, which reads awkwardly.

### Gates and promotion are ports

**Decided.** `gates` is a list on the substage and each gate is an engine. The
stage has one `promotion` engine that aggregates every substage outcome.

**Why.** Auto and manual is not enough. A gate may check a metric or a soak
period. And the aggregate needs a policy, because "one substage fails and the
stage fails" and "nine in ten passing is fine" are both reasonable.

Promotion is the right word because it names the decision being made.

**Costs.** Two more ports. Built in policies would have been far less surface.

### No defaults, no inheritance

**Decided.** `targets`, `engine`, `manager`, `gates` and `params` all live on
the substage. Nothing is declared on the stage and inherited.

**Why.** Inheritance plus override is two rules where one will do. Repetition
is solved by aliases.

**Costs.** A uniform stage restates its engine and manager on every substage.

### `params` uses your key names

**Decided.** A substage carries arbitrary user defined keys, templated into the
targets. forge-ci never learns the words `region`, `cell` or `env`.

**Why.** A matrix is confusing and it makes the CI own a vocabulary. Because
forge-ci never parses the keys, the collision that killed the name `cell` is
structurally impossible.

**Costs.** No validation of key names and no cross product expansion.

## The loop

### Two verbs, and apply is the reconcile

**Decided.** `bootstrap` and `apply`. There is no `run` and no separate
`reconcile`.

**Why.** Apply and reconcile are the same act, so naming them separately is a
lie. `bootstrap` exists only because the loop cannot create itself.

**The payoff is that triggers become dumb.** A webhook, a cron and a keyboard
all call one idempotent thing, so debounce and queuing stop being design
problems.

**Costs.** No way to force one stage for debugging without going through the
loop.

### A failing test is not an error

**Decided.** A red test is a run with status `failed`. An error means the
machinery broke.

**Why.** Taken from forge's test runner framework. Get it wrong and a red build
looks identical to a broken runner, which is the distinction a CI tool exists
to make.

**Costs.** Every engine boundary carries two channels, outcome and error. More
plumbing than returning an error.

### A tool is never a member of the repo list it builds

**Decided.** `repos:` names the product. forge-ci is not in it, exactly as
forge is not a member of the Cargo workspace it builds.

**Why.** It dissolves the circularity worry with no special case and no
exclusion list. Bootstrap circularity is fine on its own, because every
compiler self hosts and the previous release builds the next one.

**Costs.** forge-ci does not dogfood the golden pipeline on itself. It needs
its own CI.

## State and history

### A separate state repo, not an orphan branch

**Decided.** State lives in its own repo.

**Why.** An orphan branch was recommended twice and rejected in one line as an
antipattern. Machine commits must never touch a repo a human reads. At roughly
600 commits a month the cost is not size, it is signal.

**Costs.** One more repo, which is exactly what the orphan branch avoided.

### Commits pin the parts, semver names only the whole

**Decided.** A release is a tuple of commit SHAs plus one version on the
workspace. Per crate versions are deleted.

**Why.** Semver tells an external consumer whether an upgrade breaks them. A
closed workspace has no external consumer, so per crate semver is ceremony that
drifts across three files. A SHA also pins harder than a tag, because a tag can
be moved and a SHA cannot.

**Costs.** Nothing can consume the sub repos as versioned libraries. A single
repo checkout cannot tell you which release it belongs to.

### A run embeds forge's own types

**Decided.** A run record carries `forge.TestReport` and `forge.Artifact`
verbatim, in an optional `forge` section.

**Why.** Zero mapping code and full fidelity. Coverage and dependency graphs
flow into CI state for free, which is what makes it possible to build
everything on top of forge.

**Two consequences.** The compute engine's return type must carry the harvested
report from day one, because forge engines communicate through structured
content and retrofitting means changing every engine. And the section is
optional, because a compute engine may run a raw shell command with no artifact
store at all.

**Costs.** The state schema is coupled to forge's. A forge type change ripples
into recorded history.

## Shipping

### Local first is a tenet, not a shortcut

**Decided.** The first version is `ci-compute-local`, `ci-state-git` and
`ci-trigger-watch`, all on manager `local`. No cloud, no credential, no cost.

**Why.** Two reasons that happen to agree. It is the differentiator, because a
CI you can reproduce on your own machine is rare. And an abstraction with one
implementation is a guess, so a second local backend is what stops the ports
being shaped like GitHub Actions.

It started as frugality about free tier minutes. It ended as the tenet: what
runs in CI must run locally.

**Costs.** The remote path stays unvalidated. The trigger port is exercised
only by a file watcher.

### Engines ship as binaries resolved by URI

**Decided.** An engine is `go://github.com/.../cmd/ci-compute-local@v0.1.0`,
installed on demand. Not a Go interface compiled in.

**Why.** Adding a backend must not mean releasing forge-ci. It reuses the exact
mechanism forge already has, a third party engine needs no changes to forge,
and `enginecli.Bootstrap` gives CLI and MCP from one controller layer, so the
MCP framing costs nothing extra.

**Costs.** Every engine is a separate binary, separately versioned. Debugging
crosses a process boundary.

### The full schema, a partial implementation

**Decided.** The spec covers repos, managers, engines, targets, stages,
substages, gates, promotion, artifacts and triggers. The implementation covers
what the first workspace needs.

**Why.** Specifying only what the POC implements lets the first use case set
the shape, which is the outcome this whole design was avoiding.

**Costs.** Slower to a first green build, and a body of specified but
unimplemented surface that may age badly.
