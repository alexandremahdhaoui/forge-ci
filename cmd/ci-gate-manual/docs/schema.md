# ci-gate-manual configuration

The `spec` block on this engine's entry in `pipeline.yaml`.

## spec

| Key | Type | Required | Means |
|---|---|---|---|
| `approvalPath` | string | no | The file whose existence means approved. Omit it and the gate waits forever. |

## Where it comes from

The pipeline schema lives in
[forge-ci-spec](https://github.com/alexandremahdhaoui/forge-ci-spec). This
document covers only the `spec` this engine reads.

forge-ci never validates a `spec`. The engine does, because only the engine
knows what its keys mean.

## Example

```yaml
engines:
  - alias: my-gate
    type: gate
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-gate-manual@v0.1.3"
    manager: local
    spec:
      approvalPath: <string>
```
