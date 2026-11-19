"""Preserve GitHub's external-skill ownership policy in the v0.22.5 sync state."""
import hashlib
from pathlib import Path
import sys

EXPECTED = "12eb3a592603314aa15f433d5ac55abde477a9ba83657c4bf99a50985e123387"
ANCHOR = '    if dest.exists() and dir_hash(dest) == bundled_hash:\n        _rmtree_writable(dest)\n        st.say(f"  ✓ removed stale shadow of {skill_name}")\n        st.manifest.pop(skill_name, None)\n'
REPLACEMENT = '    # clawmanager-profile-external-skill-dedupe-v1\n    if dest.exists():\n        local_hash = dir_hash(dest)\n        origin_hash = st.manifest.get(skill_name, "")\n        hub_owned = dest.relative_to(_skills_dir()).as_posix() in _read_hub_install_paths()\n        managed_unmodified = local_hash == bundled_hash or (bool(origin_hash) and local_hash == origin_hash)\n        if managed_unmodified and not hub_owned and not dest.is_symlink():\n            _rmtree_writable(dest)\n            st.manifest.pop(skill_name, None)\n            st.say(f"  ✓ removed stale managed shadow of {skill_name}")\n        else:\n            st.external_conflicts.append(skill_name)\n            st.say(f"  ⚠ {skill_name}: preserved local/external skill conflict")\n'

def apply(path):
    raw = path.read_bytes()
    if hashlib.sha256(raw).hexdigest() != EXPECTED:
        raise ValueError("Skill dedupe patch requires locked post-startup sources")
    source = raw.decode("utf-8")
    edits = [
        (ANCHOR, REPLACEMENT),
        ("    shadowed_by_external: list[str] = field(default_factory=list)\n", "    shadowed_by_external: list[str] = field(default_factory=list)\n    external_conflicts: list[str] = field(default_factory=list)\n"),
        ('        "shadowed_by_external": st.shadowed_by_external,\n', '        "shadowed_by_external": st.shadowed_by_external,\n        "external_conflicts": st.external_conflicts,\n'),
    ]
    for before, after in edits:
        if source.count(before) != 1:
            raise ValueError("Skill dedupe anchor changed")
        source = source.replace(before, after, 1)
    compile(source, str(path), "exec")
    path.write_text(source, encoding="utf-8", newline="\n")

if __name__ == "__main__":
    apply(Path(sys.argv[1]))
