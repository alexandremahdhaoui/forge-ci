# ci-promotion-all configuration

The `spec` block on this engine's entry in `pipeline.yaml`.

## spec

| Key | Type | Required | Means |
|---|---|---|---|
| `threshold` | number | no | Percentage of substages that must pass. Defaults to 100. |

## Where it comes from

The pipeline schema lives in
[forge-ci-spec](https://github.com/alexandremahdhaoui/forge-ci-spec). This
document covers only the `spec` this engine reads.

forge-ci never validates a `spec`. The engine does, because only the engine
knows what its keys mean.

## Example

```yaml
engines:
  - alias: my-promotion
    type: promotion
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-promotion-all@v0.1.3"
    manager: local
    spec:
      threshold: <number>
```
