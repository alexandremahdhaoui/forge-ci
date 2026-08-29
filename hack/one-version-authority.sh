#!/bin/sh
set -eu

# The version is decided in the core and carried to the engines. An engine
# that derives one of its own is a second authority, and two authorities is
# how every member of a workspace drifted onto a version line of its own:
# the core computed v0.2.0, the release engine ignored it and computed a
# different number per repo off each repo's own last tag.
#
# This gate is here because that class of defect came back three times in one
# day in other forms. It fails when an artifact engine reads a tag line or
# does version arithmetic.

python3 - "$@" <<'PY'
import re
import sys
from pathlib import Path

# What an engine must never do: read the released tag line, or move a number
# along it. Reading in.Version and writing it out is the whole job.
FORBIDDEN = {
    'LatestTag': 'reads the tag line to decide a version',
    'NextVersion': 'does version arithmetic',
    'Bump(': 'does version arithmetic',
    'HighestLevel': 'decides how far a release moves',
}

engines = sorted(
    p for d in Path('cmd').glob('*artifact*') if d.is_dir()
    for p in d.rglob('*.go')
    if not p.name.endswith('_test.go')
)

if not engines:
    print('no artifact engine found, so nothing to check', file=sys.stderr)
    sys.exit(1)

fail = False

for path in engines:
    src = path.read_text()

    # Comments say what NOT to do here, and saying so is the point.
    code = re.sub(r'//[^\n]*', '', src)

    for token, why in FORBIDDEN.items():
        if token in code:
            print('%s: %s (%s)' % (path, token, why), file=sys.stderr)
            fail = True

if fail:
    print('', file=sys.stderr)
    print('An artifact engine publishes the version it is handed. The core '
          'decides it, in reconcilecontroller.releaseVersion, and nowhere else.',
          file=sys.stderr)
    sys.exit(1)

print('%d artifact engine files, none of them a second version authority' % len(engines))
PY
