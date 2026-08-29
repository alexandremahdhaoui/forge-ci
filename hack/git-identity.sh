#!/bin/sh
set -eu

# A git subcommand that writes an object needs a committer identity, and a CI
# runner has none. This shipped twice: gitadapter.Commit and
# releaseadapter.Tag, in different packages sharing no code, so fixing the
# first left the second dead one stage later.
#
# Every file that runs one of these subcommands must go through gitident.
# Reading git needs nothing.

MODULE="github.com/alexandremahdhaoui/forge-ci"

# The subcommands that write an object, as they appear in a shellout: a
# quoted argument. "tag" only writes when annotated, and every tag we make is.
WRITERS='"commit"|"tag"|"merge"|"cherry-pick"|"revert"|"am"|"notes"'

fail=0

# Adapters only. A controller never shells out - the architecture gate holds
# that line - so a git subcommand in one is text being rendered into a
# workflow, not a process being started.
FILES=$(find internal/adapter -name '*.go' ! -name '*_test.go' ! -path 'internal/mocks/*')

for file in $FILES; do
    grep -qE "$WRITERS" "$file" || continue

    # A file that only lists or reads tags writes nothing.
    grep -qE '"(commit|tag|merge|cherry-pick|revert|am|notes)"' "$file" || continue

    if grep -qE '"tag", "--list"' "$file" && ! grep -qE '"tag", "-m"|"commit", "-m"|"commit"\)' "$file"; then
        continue
    fi

    if ! grep -q "$MODULE/internal/gitident" "$file"; then
        echo "$file runs a git subcommand that writes an object and does not use gitident." >&2
        echo "  A CI runner has no committer identity and git exits 128. Prefix the" >&2
        echo "  subcommand with gitident.Args(ctx, runner, dir)." >&2
        fail=1
    fi
done

if [ "$fail" -eq 0 ]; then
    echo "every git write goes through gitident, so it works on a host with no identity"
fi

exit "$fail"
