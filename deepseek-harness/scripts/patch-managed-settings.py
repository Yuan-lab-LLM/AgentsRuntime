#!/usr/bin/env python3
"""Enable the host settings mirror in the authenticated managed Lite image.

The browser URL names ClawManager rather than localhost. Keep DSH's generic
host-ownership decision intact; only its settings mirror uses the existing
instance-authenticated host RPC instead of process memory. Reads stay secret-
redacted; writes retain the host provider policy and revision checks.
"""
import hashlib
from pathlib import Path
import sys

UPSTREAM_SHA256 = '479002d654490d19cbc89ed4582198603eddf2a62e3d233bbd30748f1bc0d862'
# The locked 0.2.1-rc.3 client already uses authenticated Host settings for
# remote browsers. Keep its newer forms API; do not transplant the old client.
NATIVE_HOST_SETTINGS_SHA256 = '0555eec2df1ca392a1167e637f287ac9963dbd2b0a230089c2a41be079c490e3'
NEEDLE = 'const persistence = ctx.remote.$host.isLoopback ? "host" : "memory";'
REPLACEMENT = 'const persistence = "host";'


def patch_text(source):
    if source.count(NEEDLE) != 1 or REPLACEMENT in source:
        raise ValueError('DSH managed settings patch requires exactly one reviewed persistence selection')
    return source.replace(NEEDLE, REPLACEMENT, 1)


if __name__ == '__main__':
    target = Path(sys.argv[1])
    original = target.read_bytes()
    digest = hashlib.sha256(original).hexdigest()
    if digest == NATIVE_HOST_SETTINGS_SHA256:
        print('Pinned native settings already use authenticated Host RPC; no patch required')
        sys.exit(0)
    if digest != UPSTREAM_SHA256:
        raise SystemExit('Unreviewed DSH settings artifact; refusing managed patch')
    target.write_text(patch_text(original.decode()))
