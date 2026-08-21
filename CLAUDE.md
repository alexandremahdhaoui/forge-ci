# CLAUDE.md

forge-ci orchestrates forge across many repos. It never learns what forge
builds.

Read ~/.claude/CLAUDE.md first. Those rules apply here.

## The one rule that shapes everything

**forge-ci names no project and no language toolchain.** Not in a string, not
in a comment, not in a variable name.

`hack/no-hardcoding.sh` is a test stage and it will fail the build. If you need
per language behaviour, that belongs in a forge engine or in a target, never
here.

## Conventions come from forge, not from us

forge parses yaml through `sigs.k8s.io/yaml`, so `json:` tags decide key names.

| Thing | Style | Example |
|---|---|---|
| YAML keys | lowerCamelCase | `artifactStorePath` |
| Enum values | kebab-case | `type: compute` |
| Binaries | kebab-case, domain first | `ci-state-git` |
| MCP tools | prefixed by domain | ours are `ci-*` |
| Engine URIs | `go://` or `alias://` only | anything else is a hard error |

## A failing test is not an error

Stolen from forge's test runner framework and it matters more here than there.

Return a run with status `failed`. Return an error only when the machinery
broke. Get this wrong and a red build looks identical to a broken runner, which
defeats the point of a CI tool.

## Traps that cost real time

**A `[]byte` field breaks over MCP.** It marshals to a base64 string, and the
generated JSON schema expects an array. Every state write failed on the wire
while passing in process. Payloads are strings. Unit tests cannot catch this,
which is why the integration suite exists.

**A nil map serializes to `null` and fails schema validation.** Every spec
handed to an engine goes through `orEmpty`. Miss one call site and only that
engine breaks, at runtime, in the field.

**`go test -run` matches the parent test name first.** A subtest filter like
`-run 'put-then-get'` matches nothing and exits 0, which reads exactly like a
pass. Use the full path, `-run 'TestParent/subtest'`. This hid a broken
conformance assertion for one commit.

**Substages run at the same time, so anything they share needs a lock.** They
were sequential for a while and both the schema and the generated docs claimed
otherwise, which is the kind of lie that only shows up when someone relies on
it. State writes are serialized behind one mutex, because the git state engine
commits and two concurrent commits in one repo race. An injected clock must be
safe too. Run `go test -race`.

**A revision covers uncommitted work, so a repo must gitignore its own output.**
The revision id hashes each repo's HEAD plus a hash of its uncommitted changes.
Without that, an edit that breaks the build is invisible: the trigger fires, the
substage is already recorded as passed, and the pipeline reports green on code
that does not compile. That happened live on golden-rust.

The consequence is that any build output not covered by `.gitignore` makes the
tree dirty, so every apply is a new revision and the loop never settles.
`TestUntrackedBuildOutputNeverSettles` pins that, and it is a real footgun for a
repo that does not ignore its own `.forge/`.

**A go.work hides an incomplete go.sum until CI.** This repo shipped four tags
that did not build. `go.sum` held 13 of the 36 lines it needed, and the
workspace let it borrow the rest from sibling modules. A pristine clone failed
outright.

The `standalone` stage builds with `GOWORK=off` and fails when `go mod tidy`
would change anything. Never trust a build that ran inside the workspace, and
never trust one where you passed `-mod=mod`, which silently repairs `go.sum`
and then looks like proof.

**`go-build` refuses a repo with no commits.** It stamps binaries with a git
SHA. Commit before the first `forge build`.

**Mockery needs `unroll-variadic: true`.** Without it a variadic argument
arrives as one slice and every expectation misses in a way that reads like the
code never ran.

**Nothing catches an unused engine.** A port with no engine, or an engine no
pipeline names, compiles and tests clean. The e2e suite is what proves the
wiring, so a new engine needs an e2e case or it is decoration.

## Testing

- Controllers hold the logic and are tested with generated mocks. Nothing under
  90 percent.
- `test/integration` speaks real MCP to real engine binaries. Anything crossing
  the wire needs a case here, because the wire is where the types are really
  checked.
- `test/e2e` builds real git repos, runs real forge, and reads the real state
  repo. It is hermetic. No network beyond the module cache, no game, no cloud.
- Generated mocks are excluded from coverage. `hack/coverage.sh` does that.

## The engine contract

Every engine is a `cmd/<name>` directory holding three hand-written files -
`forge-dev.yaml`, `spec.yaml`, `handlers.go` - and everything else generated
by `go://forge-dev` (`zz_generated.*`, plus `docs/list.yaml`, `usage.md` and
`schema.md`, gated by `hack/engine-docs.sh`). Engines are MCP-only; a
`config-validate` tool is generated for every engine, and the wire types come
from the OpenAPI spec in `.forge/spec-cache/`, so the contract two tools
share is the one the engine is built from.

An engine declares resources and never names a manager kind. Break that and
the manager layer stops being swappable, which is the whole reason it exists.

ci-state-git stores any record family: the built-in kinds are `revision`,
`run` and `owned`, and a caller can name extras in `spec.kinds`, each stored
under a directory of its own name - the register stores `index`, `request`
and `verdict` this way, and this engine never learns what they mean. `list`
walks nested keys recursively and returns relative slash paths.
