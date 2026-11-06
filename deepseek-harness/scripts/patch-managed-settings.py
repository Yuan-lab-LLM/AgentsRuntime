#!/usr/bin/env python3
"""Enable host settings reads only in the authenticated managed Lite image.

The browser URL names ClawManager rather than localhost. Keep DSH's generic
host-ownership decision intact; only its settings mirror uses the existing
instance-authenticated, secret-redacted host RPC instead of process memory.
"""
import hashlib
from pathlib import Path
import sys

UPSTREAM_SHA256 = '479002d654490d19cbc89ed4582198603eddf2a62e3d233bbd30748f1bc0d862'
NEEDLE = 'const persistence = ctx.remote.$host.isLoopback ? "host" : "memory";'
REPLACEMENT = 'const persistence = "host";'


def patch_text(source):
    if source.count(NEEDLE) != 1 or REPLACEMENT in source:
        raise ValueError('DSH managed settings patch requires exactly one reviewed persistence selection')
    return source.replace(NEEDLE, REPLACEMENT, 1)


if __name__ == '__main__':
    target = Path(sys.argv[1])
    original = target.read_bytes()
    if hashlib.sha256(original).hexdigest() != UPSTREAM_SHA256:
        raise SystemExit('Unreviewed DSH settings artifact; refusing managed patch')
    target.write_text(patch_text(original.decode()))
