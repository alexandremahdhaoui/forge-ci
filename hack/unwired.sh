#!/bin/sh
set -eu

# Every exported function must have a caller in production code. A ported
# function nobody calls still counts as ported, which is how half a panel goes
# missing while every gate reads green.
#
# String literals and comments are stripped first. A repo that emits code holds
# whole functions inside template literals, and reading those as definitions
# reported seven dead functions in golden-configgen that the compiler does not
# even see. Reading them as call sites is worse: it marks dead code as wired.

python3 - "$@" <<'PY'
import re
import subprocess
import sys
from pathlib import Path


def strip(src):
    """Remove comments and string literals, keeping newlines so lines still line up."""
    out = []
    i = 0
    n = len(src)

    while i < n:
        c = src[i]

        if c == '/' and i + 1 < n and src[i + 1] == '/':
            j = src.find('\n', i)
            i = n if j < 0 else j
        elif c == '/' and i + 1 < n and src[i + 1] == '*':
            j = src.find('*/', i + 2)
            j = n if j < 0 else j + 2
            out.append('\n' * src.count('\n', i, j))
            i = j
        elif c in '"`\'':
            quote = c
            j = i + 1

            while j < n:
                if src[j] == '\\' and quote != '`':
                    j += 2
                    continue

                if src[j] == quote:
                    j += 1
                    break

                j += 1

            out.append('""' + '\n' * src.count('\n', i, j))
            i = j
        else:
            out.append(c)
            i += 1

    return ''.join(out)


standard_interface_methods = {
    ('String', (), ('string',)),
    ('GoString', (), ('string',)),
    ('Error', (), ('string',)),
    ('Unwrap', (), ('error',)),
    ('Close', (), ('error',)),
    ('Read', ('[]byte',), ('int', 'error')),
    ('Write', ('[]byte',), ('int', 'error')),
    ('Len', (), ('int',)),
    ('Less', ('int', 'int'), ('bool',)),
    ('Swap', ('int', 'int'), ()),
    ('MarshalJSON', (), ('[]byte', 'error')),
    ('UnmarshalJSON', ('[]byte',), ('error',)),
    ('MarshalText', (), ('[]byte', 'error')),
    ('UnmarshalText', ('[]byte',), ('error',)),
    ('MarshalBinary', (), ('[]byte', 'error')),
    ('UnmarshalBinary', ('[]byte',), ('error',)),
    ('Format', ('fmt.State', 'rune'), ()),
    ('Is', ('error',), ('bool',)),
    ('As', ('any',), ('bool',)),
}


def past_balanced(src, start):
    depth = 0
    i = start

    while i < len(src):
        if src[i] in '([{':
            depth += 1
        elif src[i] in ')]}':
            depth -= 1
            if depth == 0:
                return i + 1

        i += 1

    return len(src)


def split_top_level(text):
    text = text.strip()

    if not text:
        return []

    groups = []
    depth = 0
    current = ''

    for c in text:
        if c in '([{':
            depth += 1
        elif c in ')]}':
            depth -= 1

        if c == ',' and depth == 0:
            groups.append(current.strip())
            current = ''
            continue

        current += c

    groups.append(current.strip())

    return [g for g in groups if g]


def parameter_types(text):
    groups = split_top_level(text)

    if not groups:
        return ()

    any_named = any(' ' in g for g in groups)
    types = []
    carried = ''

    for group in reversed(groups):
        if not any_named:
            types.append(group)
            continue

        if ' ' in group:
            carried = group.rsplit(' ', 1)[1]

        types.append(carried)

    return tuple(reversed(types))


def result_types(text):
    text = text.strip()

    if not text:
        return ()

    if text.startswith('('):
        return parameter_types(text[1:-1])

    return (text,)


def satisfies_a_standard_interface(src):
    satisfied = set()

    for match in re.finditer(r'^func\s+', src, re.M):
        i = match.end()

        if i >= len(src) or src[i] != '(':
            continue

        i = past_balanced(src, i)

        while i < len(src) and src[i].isspace():
            i += 1

        named = re.match(r'([A-Z]\w*)', src[i:])

        if not named:
            continue

        i += named.end()

        while i < len(src) and src[i].isspace():
            i += 1

        if i >= len(src) or src[i] != '(':
            continue

        closed = past_balanced(src, i)
        params = parameter_types(src[i + 1:closed - 1])
        rest = src[closed:]
        brace = rest.find('{')
        results = result_types(rest[:brace] if brace >= 0 else '')

        if (named.group(1), params, results) in standard_interface_methods:
            satisfied.add(named.group(1))

    return satisfied


roots = [d for d in ('internal', 'pkg', 'cmd') if Path(d).is_dir()]

files = [
    p for r in roots for p in Path(r).rglob('*.go')
    if not p.name.endswith('_test.go') and 'mocks' not in p.parts
]

owned = [p for p in files if not p.name.startswith('zz_generated')]

bodies = {p: strip(p.read_text()) for p in files}
haystack = '\n'.join(bodies.values())

define = re.compile(r'^func\s+(?:\([^)]*\)\s*)?([A-Z]\w*)\s*\(', re.M)

fail = False

for path in owned:
    implicit = satisfies_a_standard_interface(bodies[path])

    for name in sorted(set(define.findall(bodies[path]))):
        if name in ('Main', 'TestMain'):
            continue

        if name in implicit:
            continue

        hits = len(re.findall(r'[^\w.]%s\s*\(' % name, haystack))
        hits += len(re.findall(r'\.%s\s*\(' % name, haystack))
        defs = len(re.findall(r'^func\s+(?:\([^)]*\)\s*)?%s\s*\(' % name, haystack, re.M))

        if hits - defs <= 0:
            print('%s: %s has no caller in production code' % (path, name), file=sys.stderr)
            fail = True

if fail:
    sys.exit(1)

print('every exported function has a production caller')
PY
