# ci-state-git

**Read and write CI state in a git repo, committing each record.**

A forge-ci `state` engine.

## Why it exists

Git gives history, atomic writes, auth you already have and an audit trail, for
nothing. A database would cost money and give less, for a few kilobytes.

It is a normal repo. Read it with `git log`.

## Tools

| Tool | Does |
|---|---|
| `declare` | Report the directories the state repo needs. |
| `get` | Read one record. |
| `put` | Write one record and commit it. |
| `list` | List the keys under one kind. |

## Running it by hand

Every engine answers on the command line as well as over MCP, so you can debug
one without a pipeline.

```sh
echo '{"kind":"revision","key":"abc123","spec":{"path":"../my-state"}}' \
  | ci-state-git get
```

## Using it in a pipeline

```yaml
engines:
  - alias: my-state
    type: state
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-state-git@v0.1.3"
    manager: local
```

## What to know

Kinds are `revision`, `run` and `owned`. A run key nests, as
`<revision>/<stage>/<substage>`.

A key cannot escape the state root. `../../escape` lands inside it.

A missing record is `found: false`, never an error. Nothing to commit is not an
error either.
