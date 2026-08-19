# ci-trigger-watch configuration

The `spec` block on this engine's entry in `pipeline.yaml`.

## spec

| Key | Type | Required | Means |
|---|---|---|---|
| `watch` | list of string | yes | Directories to watch. Each must be a git repo. |
| `previous` | string | no | The last fingerprint. Omit it and the first look counts as changed. |

## Where it comes from

The pipeline schema lives in
[forge-ci-spec](https://github.com/alexandremahdhaoui/forge-ci-spec). This
document covers only the `spec` this engine reads.

forge-ci never validates a `spec`. The engine does, because only the engine
knows what its keys mean.

## Example

```yaml
engines:
  - alias: my-trigger
    type: trigger
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-trigger-watch@v0.1.3"
    manager: local
    spec:
      watch: <list of string>
      previous: <string>
```
