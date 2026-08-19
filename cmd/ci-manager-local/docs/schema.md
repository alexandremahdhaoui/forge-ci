# ci-manager-local configuration

The `spec` block on this engine's entry in `pipeline.yaml`.

## spec

| Key | Type | Required | Means |
|---|---|---|---|
| `statePath` | string | no | Where to record what was created. Omit it and nothing is recorded. |

## Where it comes from

The pipeline schema lives in
[forge-ci-spec](https://github.com/alexandremahdhaoui/forge-ci-spec). This
document covers only the `spec` this engine reads.

forge-ci never validates a `spec`. The engine does, because only the engine
knows what its keys mean.

## Example

```yaml
engines:
  - alias: my-manager
    type: manager
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-local@v0.1.3"
    manager: local
    spec:
      statePath: <string>
```
