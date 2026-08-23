# forge-ci

Declarative multi repo CI, built on forge. It runs on your laptop first.

## Why

forge answers what the targets are and how to build them, in one repo, on one
machine. It does not answer which repos at which commits form a release, what
runs where, or what gets promoted to which environment.

forge-ci answers those and calls forge for the rest.

## Try it

```sh
forge-ci bootstrap --config forge-ci.yaml
forge-ci apply     --config forge-ci.yaml
forge-ci status    --config forge-ci.yaml
```

The first version needs no cloud, no credential and no account. If you own one
computer you can run your pipeline.

## The smallest pipeline that does something

```yaml
name: demo

repos:
  - name: my-repo
    url: git@github.com:me/my-repo.git

managers:
  - alias: local
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-local@v0.1.0"

engines:
  - alias: here
    type: compute
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-compute-local@v0.1.0"
    manager: local
  - alias: ci-state
    type: state
    engine: "forge://github.com/alexandremahdhaoui/forge-ci/cmd/ci-state-git@v0.1.0"
    manager: local
    spec:
      path: ../demo-state

state: ci-state

targets:
  - alias: build-all
    forge: test-all
    in: [my-repo]

stages:
  - name: build
    substages:
      - name: default
        engine: here
        manager: local
        targets: [build-all]
```

## Reading order

| Doc | Answers |
|---|---|
| [docs/concepts.md](docs/concepts.md) | What every word means |
| [docs/architecture.md](docs/architecture.md) | How the parts fit and how it bootstraps |
| [docs/decisions.md](docs/decisions.md) | Why it is shaped this way |

## Engines

| Engine | Port | Does |
|---|---|---|
| `ci-compute-local` | compute | Runs targets here and harvests forge's artifact store |
| `ci-state-git` | state | Reads and writes a state repo, committing each record |
| `ci-trigger-watch` | trigger | Fingerprints watched repos and says whether they moved |
| `ci-gate-manual` | gate | Waits for an approval file |
| `ci-promotion-all` | promotion | Advances on a pass threshold, 100 percent by default |
| `ci-manager-local` | manager | Creates directories and files, and records what it made |
| `ci-manager-dryrun` | manager | Reports what it would create, changes nothing |

Write your own. An engine is a binary named by a `forge://` URI. Nothing needs to
know it exists until a pipeline file names it.

## Gates

```sh
forge test-all
```

Six stages: `lint`, `no-hardcoding`, `unit`, `integration`, `e2e`, `coverage`.

`no-hardcoding` fails if forge-ci mentions any project or language toolchain by
name. It orchestrates forge. It must not know what forge builds.

`coverage` holds a floor of 90 percent across all three suites, with generated
mocks excluded.
