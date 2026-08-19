# ci-gate-manual

**Pass when an approval file exists, otherwise wait.**

A forge-ci `gate` engine.

## Why it exists

A gate judges an outcome. It runs after a substage, never before, because a
gate before a substage produces "why is my CI not progressing".

## Tools

| Tool | Does |
|---|---|
| `declare` | Report the resources this gate needs. It needs none. |
| `evaluate` | Judge a finished run. |

## Running it by hand

Every engine answers on the command line as well as over MCP, so you can debug
one without a pipeline.

```sh
echo '{"run":{"status":"passed"},"spec":{"approvalPath":"/tmp/approved"}}' \
  | ci-gate-manual evaluate
```

## Using it in a pipeline

```yaml
engines:
  - alias: my-gate
    type: gate
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-gate-manual@v0.1.3"
    manager: local
```

## What to know

A failed substage returns `failed`, because there is nothing to approve.

No `approvalPath` returns `pending` and says how to set it. That is deliberate:
a gate that silently passes is worse than one that blocks.
