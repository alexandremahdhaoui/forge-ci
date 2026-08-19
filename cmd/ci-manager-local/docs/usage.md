# ci-manager-local

**Make declared resources exist on this machine, and record what it made.**

A forge-ci `manager` engine.

## Why it exists

It creates directories and files. Nothing else. That is the whole point: a
manager you can run with no cloud, no credential and no cost, so the seam
between engines and managers is testable on a laptop.

## Tools

| Tool | Does |
|---|---|
| `reconcile` | Realize every declared resource and return who owns it. |

## Running it by hand

Every engine answers on the command line as well as over MCP, so you can debug
one without a pipeline.

```sh
echo '{"manager":"local","resources":[{"kind":"directory","name":"/tmp/state"}]}' \
  | ci-manager-local reconcile
```

## Using it in a pipeline

```yaml
engines:
  - alias: my-manager
    type: manager
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-local@v0.1.3"
    manager: local
```

## What to know

It knows two kinds, `directory` and `file`. A `file` that already exists is
kept, never overwritten. Anything else is refused by name.

It refuses outright when a resource is recorded as owned by a different
manager. It does not create a second copy beside the first.
