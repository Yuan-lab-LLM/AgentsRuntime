#!/usr/bin/env python3
"""Extend pinned Hermes 0.21 external skill deduplication safely.

Upstream already indexes external skills and suppresses new local shadows.
Only add recovery of an unchanged older managed copy and explicit conflicts.
Keep upstream profile scoping, opt-out, rename recovery and hub ownership.
All anchors fail closed on source drift.
"""
from pathlib import Path
import sys

MARKER = "clawmanager-profile-external-skill-dedupe-v1"
ANCHOR = '''            if dest.exists() and _dir_hash(dest) == bundled_hash:
                _rmtree_writable(dest)
                if not quiet:
                    print(f"  ✓ removed stale shadow of {skill_name}")
                manifest.pop(skill_name, None)
'''
REPLACEMENT = '''            # clawmanager-profile-external-skill-dedupe-v1
            if dest.exists():
                local_hash = _dir_hash(dest)
                origin_hash = manifest.get(skill_name, "")
                if hub_paths is None:
                    hub_paths = _read_hub_install_paths()
                hub_owned = dest.relative_to(_skills_dir()).as_posix() in hub_paths
                managed_unmodified = (
                    local_hash == bundled_hash
                    or (bool(origin_hash) and local_hash == origin_hash)
                )
                if managed_unmodified and not hub_owned and not dest.is_symlink():
                    _rmtree_writable(dest)
                    manifest.pop(skill_name, None)
                    if not quiet:
                        print(f"  ✓ removed stale managed shadow of {skill_name}")
                else:
                    external_conflicts.append(skill_name)
                    if not quiet:
                        print(f"  ⚠ {skill_name}: preserved local/external skill conflict")
'''

def replace_once(source, anchor, replacement):
    if source.count(anchor) != 1:
        raise RuntimeError("Hermes skill sync source drift: anchor count is not one")
    return source.replace(anchor, replacement, 1)

def main():
    if len(sys.argv) != 2:
        raise SystemExit("usage: apply_profile_external_skill_dedupe.py <skills_sync.py>")
    target = Path(sys.argv[1])
    source = target.read_text(encoding="utf-8")
    if MARKER in source:
        return 0
    source = replace_once(source,
        "    shadowed_by_external: List[str] = []\n",
        "    shadowed_by_external: List[str] = []\n    external_conflicts: List[str] = []\n")
    source = replace_once(source, ANCHOR, REPLACEMENT)
    source = replace_once(source,
        '        "shadowed_by_external": shadowed_by_external,\n',
        '        "shadowed_by_external": shadowed_by_external,\n        "external_conflicts": external_conflicts,\n')
    target.write_text(source, encoding="utf-8")
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
