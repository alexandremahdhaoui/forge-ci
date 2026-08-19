#!/bin/sh
set -eu

BANNED="golden-rust golden-go golden-python golden-typescript poe-wayfinder opends gamesync cargo rustc pnpm npm uv pytest clippy oapi-codegen mockery"

fail=0

FILES=$(find internal pkg -name '*.go' ! -name '*_test.go' ! -path 'internal/mocks/*')

for word in $BANNED; do
    hits=$(grep -rniF "$word" $FILES || true)

    if [ -n "$hits" ]; then
        echo "forge-ci must not know about \"$word\". It orchestrates forge, it does not know what forge builds." >&2
        echo "$hits" >&2
        fail=1
    fi
done

if [ "$fail" -eq 0 ]; then
    echo "forge-ci names no project and no language toolchain"
fi

exit "$fail"
