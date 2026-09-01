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

    # Sync is a bool, so the string loop above cannot see it. Same defect
    # class: dropped at the boundary it is a zero value, and a substage that
    # declared sync: true builds against a workspace nothing converged.
    for field in Sync; do
        grep -qE "	$field +bool" "$spec" || continue

        if ! grep -qE "$field: *in\.$field" "$handlers"; then
            echo "$handlers drops $field: the wire carries it and the mapping does not copy it." >&2
            echo "  A missing field here is not a compile error, it is a zero value at runtime." >&2
            fail=1
        fi
    done

    # RepoCheckout is nested inside RunInput, so the loop above cannot see it:
    # its fields are on another type and not all of them are strings. The
    # checkout list is what carries the needs graph, and a dropped Needs makes
    # every repo build in list order - which looks exactly like a workspace
    # that declared no dependencies.
    grep -q 'citypes.RepoCheckout{' "$handlers" || continue

    for field in Name Path Sha Needs; do
        grep -qE "	$field \[?\]?string" "$spec" || continue

        # SHA is the internal spelling of the generated Sha. Only the
        # capitalisation differs; it is still one field to one field.
        internal="$field"
        if [ "$field" = "Sha" ]; then
            internal="SHA"
        fi

        if ! grep -qE "$internal: *r\.$field" "$handlers"; then
            echo "$handlers drops RepoCheckout.$field: the wire carries it and the mapping does not copy it." >&2
            echo "  A missing field here is not a compile error, it is a zero value at runtime." >&2
            fail=1
        fi
    done
done

# The manager engines answer outward, so the mapping runs the other way and
# the loops above cannot see it. Changed is one bool and it decides whether a
# run stops: dropped, the pipeline converges its own resources, measures the
# tree it just rewrote, and mints a dirty revision the release then refuses.
# That shipped, and it read as a broken release rather than a dropped field.
for dir in cmd/ci-*; do
    handlers="$dir/handlers.go"
    spec="$dir/zz_generated.spec.go"

    [ -f "$handlers" ] && [ -f "$spec" ] || continue

    grep -q 'ReconcileOutput{' "$handlers" || continue

    for field in Changed Published; do
        grep -q "	$field bool" "$spec" || continue

        if ! grep -qE "$field: *out\.$field" "$handlers"; then
            echo "$handlers drops ReconcileOutput.$field: the internal type carries it and the mapping does not copy it." >&2
            echo "  A missing field here is not a compile error, it is a zero value at runtime." >&2
            fail=1
        fi
    done

    # The inward half. DryRun dropped is a plan that writes, which is the
    # worst failure this whole surface has; Force dropped is a secret nobody
    # can rotate. Both are bools, so both are silent.
    for field in DryRun Force; do
        grep -q "	$field bool" "$spec" || continue

        if ! grep -qE "$field: *in\.$field" "$handlers"; then
            echo "$handlers drops ReconcileInput.$field: the wire carries it and the mapping does not copy it." >&2
            echo "  A missing field here is not a compile error, it is a zero value at runtime." >&2
            fail=1
        fi
    done
done

if [ "$fail" -eq 0 ]; then
    echo "every engine copies the fields its wire type carries into the internal one"
fi

exit "$fail"
