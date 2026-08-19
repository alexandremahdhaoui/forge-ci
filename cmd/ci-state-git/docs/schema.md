# ci-state-git configuration

The `spec` block on this engine's entry in `pipeline.yaml`.

## spec

| Key | Type | Required | Means |
|---|---|---|---|
| `path` | string | yes | The state repo. Created and initialised if it is not a repo yet. |

## Where it comes from

The pipeline schema lives in
[forge-ci-spec](https://github.com/alexandremahdhaoui/forge-ci-spec). This
document covers only the `spec` this engine reads.

forge-ci never validates a `spec`. The engine does, because only the engine
knows what its keys mean.

## Example

```yaml
engines:
  - alias: my-state
    type: state
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-state-git@v0.1.3"
    manager: local
    spec:
      path: <string>
```
