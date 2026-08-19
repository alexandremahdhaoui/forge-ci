# ci-promotion-all

**Advance a stage when enough of its substages passed.**

A forge-ci `promotion` engine.

## Why it exists

A promotion is the decision to move on, taken once per stage over every
substage outcome. It is a port, so `all`, a threshold, and anything you write
yourself are all just engines.

## Tools

| Tool | Does |
|---|---|
| `declare` | Report the resources this promotion needs. It needs none. |
| `evaluate` | Decide whether the stage advances. |

## Running it by hand

Every engine answers on the command line as well as over MCP, so you can debug
one without a pipeline.

```sh
echo '{"stage":"prod","runs":[{"status":"passed"},{"status":"failed"}],"spec":{"threshold":90}}' \
  | ci-promotion-all evaluate
```

## Using it in a pipeline

```yaml
engines:
  - alias: my-promotion
    type: promotion
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-promotion-all@v0.1.3"
    manager: local
```

## What to know

A substage counts as passed only when its status is `passed` **and** every one
of its gates passed. A pending gate blocks the stage.

A stage with no runs never advances.

A threshold outside 0 to 100, or one that is not a number, is an error.
