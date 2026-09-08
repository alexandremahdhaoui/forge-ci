#!/bin/sh
# forge-ci names no project, no language toolchain, no floating version, and
# - outside the engines that adapt one platform - no platform word either.
#
# The GitHub engines may say GitHub: ci-compute-github renders Actions
# workflows, ci-manager-github converges an Actions repository, the release
# and container engines publish to GitHub and ghcr, the trigger engine
# reads GitHub's API, and the github adapter is the HTTP client they share.
# Those words belong to the platform they adapt, and each engine's spec is
# where a customer changes them. Everywhere else a runner label, an action
# pin, an API host, a token name, a state path, a second semver expression
# or a second spelling of the dirty suffix is a literal that should have
# been a spec key or a shared constant, and this gate fails on it by name.
set -eu

fail=0

# The scope: production Go under cmd, internal and pkg; generated code and
# mocks are what forge-dev and mockery wrote and are checked at their source.
FILES=$(find cmd internal pkg -name '*.go' ! -name '*_test.go' ! -path 'internal/mocks/*' ! -name 'zz_generated*')

# The words this tool must never know, anywhere.
BANNED="golden-rust golden-go golden-python golden-typescript poe-wayfinder opends gamesync cargo rustc pnpm npm uv pytest clippy oapi-codegen mockery"

for word in $BANNED; do
    hits=$(grep -rniF "$word" $FILES cmd/*/spec.yaml cmd/*/docs/*.md 2>/dev/null || true)

    if [ -n "$hits" ]; then
        echo "forge-ci must not know about \"$word\". It orchestrates forge, it does not know what forge builds." >&2
        echo "$hits" >&2
        fail=1
    fi
done

# Latest is never a fallback: every go-run carries a pin, so a floating
# version in production code is a regression.
latest_hits=$(grep -rn "@latest" $FILES || true)
if [ -n "$latest_hits" ]; then
    echo "forge-ci must never float to @latest; a go run carries a pinned version." >&2
    echo "$latest_hits" >&2
    fail=1
fi

# A literal class, its pattern, and the files allowed to carry it. Anything
# outside the allowlist that matches fails naming the file, the line and
# the class.
#
#   class            pattern                        allowed prefixes
platform_classes='
runner-label     ubuntu-latest                  cmd/ci-compute-github/ cmd/ci-manager-github/ internal/controller/workflowcontroller/workflowcontroller.go internal/controller/triggercontroller/notify.go
action-pin       actions/[a-z-]*@v[0-9]         cmd/ci-compute-github/ internal/controller/workflowcontroller/workflowcontroller.go
github-api-host  api\.github\.com               cmd/ci-compute-github/ cmd/ci-manager-github/ cmd/ci-artifact-release/ cmd/ci-artifact-container/ cmd/ci-trigger-watch/ internal/adapter/githubadapter/ internal/controller/triggercontroller/notify.go internal/controller/workflowcontroller/workflowcontroller.go
upload-host      uploads\.github\.com           cmd/ci-artifact-release/ internal/adapter/githubadapter/
token-name       GITHUB_TOKEN                   cmd/ci-compute-github/ cmd/ci-manager-github/ cmd/ci-artifact-release/ cmd/ci-artifact-container/ cmd/ci-trigger-watch/ internal/adapter/githubadapter/ internal/adapter/containeradapter/ internal/controller/managercontroller/githubrealizer.go internal/controller/workflowcontroller/ internal/controller/triggercontroller/notify.go internal/driver/clidriver/github.go
state-path       \.forge-ci/                    pkg/citypes/citypes.go
semver-regexp    \^v\(0\|\[1-9\]\\d\*\)\\\.\(0\|   pkg/citypes/citypes.go
dirty-suffix     "-dirty"                       pkg/citypes/citypes.go
'

printf '%s\n' "$platform_classes" | while read -r class pattern allowed; do
    [ -n "$class" ] || continue

    hits=$(grep -rnE -- "$pattern" $FILES || true)
    [ -n "$hits" ] || continue

    printf '%s\n' "$hits" | while IFS= read -r hit; do
        file=${hit%%:*}
        ok=0

        for prefix in $allowed; do
            case "$file" in
                "$prefix"*) ok=1 ;;
            esac
        done

        if [ "$ok" -eq 0 ]; then
            echo "$hit" >&2
            echo "  ^ a $class literal outside the engines that adapt it; declare it on a spec or read the shared constant" >&2
            echo "class-violation" >> "${TMPDIR:-/tmp}/no-hardcoding.$$"
        fi
    done
done

if [ -s "${TMPDIR:-/tmp}/no-hardcoding.$$" ]; then
    fail=1
fi

rm -f "${TMPDIR:-/tmp}/no-hardcoding.$$"

if [ "$fail" -eq 0 ]; then
    echo "forge-ci names no project, no language toolchain, no floating version, and no platform word outside the engines that adapt it"
fi

exit "$fail"
