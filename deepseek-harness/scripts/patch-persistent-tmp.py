#!/usr/bin/env python3
"""Pin the Lite-only DSH sandbox patch to its reviewed upstream artifact.

The outer slot sandbox exposes only this instance's PVC-backed /tmp. A nested
workspace-write command must retain that mount, rather than hide it with tmpfs.
Read-only commands and the Landlock allow-list are unchanged.
"""
import hashlib
from pathlib import Path
import sys

# Reviewed 0.2.1-rc.3 original, also recorded in native/source-lock.json.
# The workspace-write mount change is the same; read-only/Landlock stay intact.
UPSTREAM_SHA256 = 'be6e1107871cf8d94c040090b13c405bc833822e2f51ed5280e93eececf762be'
NEEDLE = 'args.push("--tmpfs", "/tmp");'
REPLACEMENT = 'args.push(...(process.env.CLAWMANAGER_DSH_PRIVATE_TMP === "1" ? ["--bind", "/tmp", "/tmp"] : ["--tmpfs", "/tmp"]));'


def patch_text(source):
    if source.count(NEEDLE) != 1 or REPLACEMENT in source:
        raise ValueError('DSH tmp mount patch requires exactly one reviewed workspace-write tmpfs')
    return source.replace(NEEDLE, REPLACEMENT, 1)


def main():
    target = Path(sys.argv[1])
    original = target.read_bytes()
    if hashlib.sha256(original).hexdigest() != UPSTREAM_SHA256:
        raise SystemExit('Unreviewed DSH sandbox artifact; refusing tmp patch')
    target.write_text(patch_text(original.decode()))


if __name__ == '__main__':
    main()
