# ci-trigger-watch

**Fingerprint the watched repos and say whether they moved.**

A forge-ci `trigger` engine.

## Why it exists

It is stateless. It reports a fingerprint and compares it to the one you hand
it, so forge-ci keeps the history and the engine keeps none.

An uncommitted edit counts as a move, which is what you want on a laptop.

## Tools

| Tool | Does |
|---|---|
| `declare` | Report the resources this trigger needs. It needs none. |
| `poll` | Fingerprint the watched repos and compare with spec.previous. |

## Running it by hand

Every engine answers on the command line as well as over MCP, so you can debug
one without a pipeline.

```sh
echo '{"spec":{"watch":["../my-repo"],"previous":""}}' \
  | ci-trigger-watch poll
```

## Using it in a pipeline

```yaml
engines:
  - alias: my-trigger
    type: trigger
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-trigger-watch@v0.1.3"
    manager: local
```

## What to know

The fingerprint covers each repo's HEAD and whether it is dirty. It is order
independent, so listing the same repos differently gives the same answer.
