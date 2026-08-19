# ci-compute-local

**Run a substage's targets on this machine and harvest what forge produced.**

A forge-ci `compute` engine.

## Why it exists

This is what makes the tenet true. What runs in CI runs locally, because
locally is the only place it runs.

After a forge target it reads `.forge/artifact-store.yaml` from the directory
forge ran in, so every test report and artifact lands in CI state for free.

## Tools

| Tool | Does |
|---|---|
| `declare` | Report the resources this compute target needs. It needs none. |
| `run` | Run the targets and return the outcome. |

## Running it by hand

Every engine answers on the command line as well as over MCP, so you can debug
one without a pipeline.

```sh
echo '{"revision":"abc","stage":"build","substage":"default","root":"/work","targets":[{"alias":"build-all","forge":"test-all","in":["my-repo"]}]}' \
  | ci-compute-local run
```

## Using it in a pipeline

```yaml
engines:
  - alias: my-compute
    type: compute
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-compute-local@v0.1.3"
    manager: local
```

## What to know

A failing target is **not** an error. It returns `status: failed`. An error
means the machinery broke, not that the code did.

It harvests only test reports that started after the run did. forge's artifact
store keeps every past run, so without that filter a run of three stages
reports seventy.

`params` are templated into a target as `{{.Params.name}}`. A missing key is an
error, never an empty string.
