#!/bin/sh
set -eu

# A generated wire type is not an internal type, and the mapping between them
# is written by hand. A field the mapping forgets does not fail to compile: it
# stays zero, the engine runs, and the value is simply absent.
#
# That shipped. RunInput.Version crossed the wire into both compute engines
# and neither handler copied it, so every binary in the v0.45.1 release was
# stamped by git describe instead of by the version the pipeline decided.
# The release said v0.45.1 and the binaries inside it said v0.44.5-6-gcb456c5.
#
# So: a field the generated type carries and the internal type also carries
# must appear in the handler that maps between them.

fail=0

for dir in cmd/ci-*; do
    handlers="$dir/handlers.go"
    spec="$dir/zz_generated.spec.go"

    [ -f "$handlers" ] && [ -f "$spec" ] || continue

    # Only engines that map a RunInput at all.
    grep -q 'citypes.RunInput{' "$handlers" || continue

    for field in Revision Version Stage Substage Root; do
        grep -q "	$field string" "$spec" || continue

        if ! grep -qE "$field: *in\.$field" "$handlers"; then
            echo "$handlers drops $field: the wire carries it and the mapping does not copy it." >&2
            echo "  A missing field here is not a compile error, it is a zero value at runtime." >&2
            fail=1
        fi
    done
done

if [ "$fail" -eq 0 ]; then
    echo "every engine copies the fields its wire type carries into the internal one"
fi

exit "$fail"
