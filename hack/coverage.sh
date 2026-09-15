#!/bin/sh
set -eu

FLOOR="${1:-90}"
OUT="${2:-tmp/coverage.out}"
PACKAGE_FLOOR="${3:-77}"
PACKAGE_FLOOR_EXCLUDED='internal/adapter/talosadapter'

mkdir -p "$(dirname "$OUT")"

PKGS=$(go list ./internal/... ./pkg/... | grep -v '/internal/mocks/' | paste -sd, -)

go test -tags "integration e2e" -coverpkg="$PKGS" -coverprofile="$OUT.raw" ./... >/dev/null

grep -v '/internal/mocks/' "$OUT.raw" > "$OUT"

TOTAL=$(go tool cover -func="$OUT" | awk '/^total:/ {gsub(/%/,"",$3); print $3}')

echo "coverage ${TOTAL}% against a floor of ${FLOOR}%"

awk -v total="$TOTAL" -v floor="$FLOOR" 'BEGIN { exit (total + 0 >= floor + 0) ? 0 : 1 }' || {
    echo "coverage ${TOTAL}% is below the floor of ${FLOOR}%" >&2
    go tool cover -func="$OUT" | awk '$3 != "100.0%"' >&2
    exit 1
}

echo "every package clears ${PACKAGE_FLOOR}%, and that floor ratchets up and never down"
echo "${PACKAGE_FLOOR_EXCLUDED} is out of it because a unit test never reaches a node, the adapter is proved against a VM, and the exclusion stands until its testenv-vm case lands"

PER_PACKAGE=$(awk 'NR > 1 {
    statements[$1] = $2
    if ($3 + 0 > 0) hit[$1] = 1
}
END {
    for (block in statements) {
        n = split(block, part, "/")
        pkg = part[1]
        for (i = 2; i < n; i++) pkg = pkg "/" part[i]
        total[pkg] += statements[block]
        if (block in hit) covered[pkg] += statements[block]
    }
    for (pkg in total) printf "%s %.1f\n", pkg, 100 * covered[pkg] / total[pkg]
}' "$OUT" | sort)

printf '%s\n' "$PER_PACKAGE"

BELOW=$(printf '%s\n' "$PER_PACKAGE" | awk -v floor="$PACKAGE_FLOOR" -v skip="$PACKAGE_FLOOR_EXCLUDED" '
    index($1, skip) { next }
    $2 + 0 < floor + 0 { print }')

if [ -n "$BELOW" ]; then
    echo "these packages are below the per package floor of ${PACKAGE_FLOOR}%:" >&2
    printf '%s\n' "$BELOW" >&2
    exit 1
fi
