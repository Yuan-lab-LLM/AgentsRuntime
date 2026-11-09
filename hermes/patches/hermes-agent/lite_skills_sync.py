"""Same discovery exclusions as upstream, applied before directory descent.

Path.rglob followed by filtering still traverses entire virtualenvs, repositories
and skill support trees on NFS. Prune those trees without caching user contents or
changing bundled-skill update, deletion, ownership or hash checks.
"""
import os
from pathlib import Path
from concurrent.futures import ThreadPoolExecutor


def iter_skill_mds(root, sort=False):
    from agent.skill_utils import EXCLUDED_SKILL_DIRS, SKILL_SUPPORT_DIRS, is_excluded_skill_path
    root = Path(root)
    if is_excluded_skill_path(root / 'SKILL.md'):
        return

    def walk():
        for directory, dirs, files in os.walk(root, followlinks=False):
            has_skill = 'SKILL.md' in files or 'SKILL.md' in dirs
            dirs[:] = [name for name in dirs if name not in EXCLUDED_SKILL_DIRS
                       and not (has_skill and name in SKILL_SUPPORT_DIRS)]
            if has_skill:
                candidate = Path(directory) / 'SKILL.md'
                if not is_excluded_skill_path(candidate):
                    yield candidate

    found = walk()
    yield from sorted(found) if sort else found


def matching_candidates(candidates, hash_directory):
    """Bounded read-only hashing; results keep upstream order for serial writes.

    No cross-request cache: edits, removals and newly installed skills are seen
    on each sync. Only file reads run in workers; provenance commits stay serial.
    """
    def matches(candidate):
        _name, source, destination, _relative = candidate
        return hash_directory(destination) == hash_directory(source)
    with ThreadPoolExecutor(max_workers=4, thread_name_prefix='lite-skill-hash') as pool:
        # Four queued/active packages at a time, including on Python < 3.14.
        iterator = iter(candidates)
        from itertools import islice
        while batch := list(islice(iterator, 4)):
            for candidate, matches_origin in zip(batch, pool.map(matches, batch)):
                if matches_origin:
                    yield candidate
