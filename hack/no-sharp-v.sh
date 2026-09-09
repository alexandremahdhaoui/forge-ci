#!/bin/sh
set -eu

fail=0

FILES=$(find cmd internal pkg -name '*.go' ! -path 'internal/mocks/*' ! -name 'zz_generated*')

hits=$(grep -rn -- '%#v' $FILES || true)

if [ -n "$hits" ]; then
    printf '%s\n' "$hits" >&2
    echo "  ^ %#v prints a struct field by field and never calls String()." >&2
    echo "  citypes.Resource.String() hides spec so no formatter can put a declared secret in a" >&2
    echo "  pipeline log, and %#v is the one verb that walks around it. Use %v, %+v or %s." >&2
    fail=1
fi

if [ "$fail" -eq 0 ]; then
    echo "no formatter walks around String()"
fi

exit "$fail"
