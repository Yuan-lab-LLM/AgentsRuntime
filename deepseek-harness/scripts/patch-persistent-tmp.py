#!/usr/bin/env python3
"""Pin the Lite-only DSH sandbox patch to its reviewed upstream artifact.

The outer slot sandbox exposes only this instance's PVC-backed /tmp. A nested
workspace-write command must retain that mount, rather than hide it with tmpfs.
Read-only commands and the Landlock allow-list are unchanged.
"""
import hashlib
from pathlib import Path
import sys

UPSTREAM_SHA256 = 'f3533a38fca8e95755c69ab2e09c78e60c094e26105a0feb323e91daecb4ad1a'
NEEDLE = 'args.push("--tmpfs", "/tmp");'
REPLACEMENT = 'args.push("--bind", "/tmp", "/tmp");'


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
