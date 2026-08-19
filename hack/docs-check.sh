#!/bin/sh
set -eu

SCHEMA=".forge/spec-cache/forge-ci.v1.yaml"
CONCEPTS="docs/concepts.md"

[ -f "$SCHEMA" ] || { echo "run the resolve-spec build stage first" >&2; exit 1; }

fail=0

TERMS="repos managers engines targets stages substages gates promotion params state triggers compute trigger gate artifact revision run"

for term in $TERMS; do
    if ! grep -qi "$term" "$CONCEPTS"; then
        echo "$CONCEPTS never explains \"$term\"" >&2
        fail=1
    fi
done

PORTS=$(awk '/^    enum: \[compute/ {print}' "$SCHEMA" | tr -d '[],' | sed 's/enum://')

for port in $PORTS; do
    if ! grep -qi "$port" "$CONCEPTS"; then
        echo "the schema has a port \"$port\" that $CONCEPTS never explains" >&2
        fail=1
    fi
done

KEYS=$(awk '/^properties:/{inprops=1; next} inprops && /^  [a-zA-Z]+:/{gsub(/[: ]/,""); print} /^\$defs:/{inprops=0}' "$SCHEMA")

for key in $KEYS; do
    case "$key" in
        name|artifactStorePath) continue ;;
    esac

    if ! grep -qi "$key" "$CONCEPTS"; then
        echo "the schema has a key \"$key\" that $CONCEPTS never explains" >&2
        fail=1
    fi
done

if [ "$fail" -eq 0 ]; then
    echo "every schema term is explained in $CONCEPTS"
fi

exit "$fail"
