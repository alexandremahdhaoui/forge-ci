# ci-manager-dryrun

**Report what would be created, and change nothing.**

A forge-ci `manager` engine.

## Why it exists

It exists to prove the manager seam is real. Swap `local` for `dryrun`, apply,
and the ownership record makes forge-ci refuse. That refusal is the test.

## Tools

| Tool | Does |
|---|---|
| `reconcile` | Report what would be realized. Touch nothing. |

## Running it by hand

Every engine answers on the command line as well as over MCP, so you can debug
one without a pipeline.

```sh
echo '{"manager":"dryrun","resources":[{"kind":"directory","name":"/tmp/state"}]}' \
  | ci-manager-dryrun reconcile
```

## Using it in a pipeline

```yaml
engines:
  - alias: my-manager
    type: manager
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-dryrun@v0.1.3"
    manager: local
```

## What to know

It realizes nothing, so it accepts any kind. It still refuses when a resource
is recorded as owned by a different manager, because that check belongs to the
controller, not the realizer.
