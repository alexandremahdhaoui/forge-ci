# Concepts

Eleven words. Six describe what you write. Five describe what happens.

## What you write

### Repo

A git repo that takes part in the pipeline.

Repos are optional. A pipeline with one repo is fine. A pipeline with none is
fine too, and then its revision is the pipeline file itself.

```yaml
repos:
  - name: golden-rust
    url: git@github.com:alexandremahdhaoui/golden-rust.git
```

### Port

The question an engine answers. There are six.

| Port | Question |
|---|---|
| `compute` | Where does a job run |
| `state` | What happened, and when |
| `trigger` | What starts a run |
| `gate` | Did this substage's result pass |
| `promotion` | Should this stage advance |
| `artifact` | Where do outputs go |

The list is closed. A typo cannot invent a new port.

### Engine

One implementation of one port. You name it with an alias and a URI.

```yaml
engines:
  - alias: here
    type: compute
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-compute-local@v0.1.0"
    manager: local
```

An engine declares the resources it needs. It never names a manager kind.

### Manager

How a declared resource is made to exist.

The local manager creates directories and files. A terraform manager would
write HCL and apply it. An API manager would call a cloud API. forge-ci cannot
tell the difference.

```yaml
managers:
  - alias: local
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-local@v0.1.0"
```

### Target

A unit of work. It names a forge target or a forge-ci verb.

This is the only place a forge target ever appears.

```yaml
targets:
  - alias: build-all
    forge: test-all
    in: [golden-rust]
```

`in` names the repos it runs in. Leave it out and it runs at the root.

### Wiring

Two keys say which engines are in charge.

`state` names the one engine that records what happened. `triggers` lists the
engines allowed to say something moved.

```yaml
state: ci-state
triggers: [on-change]
```

Every other engine is reached from a substage, so it needs no wiring key.

### Stage and substage

A stage is one phase of the pipeline. Stages run in order. The order is the
order you wrote them in. There is no dependency graph.

A stage holds substages. The substages run at the same time.

A substage says where it runs, what it runs, and what must pass afterwards.

```yaml
stages:
  - name: prod
    promotion: all-pass
    substages:
      - name: eu-west-1-a
        engine: here
        manager: local
        targets: [deploy]
        gates: [approve]
        params: { region: eu-west-1, cell: a }
```

`params` are your own key names. forge-ci never reads them. It templates them
into the target as `{{.Params.region}}` and hands them over.

That is why your cells and regions can never collide with a forge-ci word. It
does not have one.

## What happens

### Revision

A tuple of commit SHAs, one per repo, proven to build together.

The id is a hash of that tuple, so the same commits always give the same
revision and a new commit always gives a new one.

### Run

One execution of one substage, for one revision.

A run records its status, how long it took, the gates that judged it, and the
forge artifacts and test reports harvested from wherever forge ran.

### Gate

A check that runs **after** a substage, never before.

A gate that has not passed blocks the stage. The manual gate waits for a file
to appear. Write your own to check a metric or a soak period.

### Promotion

The decision to advance, taken once per stage, over every substage outcome.

The default advances when every substage passed. Give it `spec.threshold: 90`
and it advances when nine in ten passed.

### Status

`pending`, `running`, `passed`, `failed`.

A failed test is a run with status `failed`. It is not an error. An error means
the machinery broke, not that the code did.
