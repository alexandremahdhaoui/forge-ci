# ci-manager-dryrun configuration

The `spec` block on this engine's entry in `pipeline.yaml`.

## spec

It takes none.

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
    engine: "go://github.com/alexandremahdhaoui/forge-ci/cmd/ci-manager-dryrun@v0.1.3"
    manager: local
```
