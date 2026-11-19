"""Explicit, offline 0.21 Dashboard -> locked 0.22.5 Serve migration.

Run as the instance UID only after the controller and native stop proofs agree.
No HTTP/model/tool entry point invokes this operator utility. It retains a full
private home snapshot, validates native DB migration on a copy, then migrates
the original DB and publishes its new version marker last. Never auto-restore
the snapshot after the instance has accepted new work.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import subprocess
import sys
import uuid

OLD = {"schema_version": 1, "hermes_version": "0.21.0", "hermes_git_ref": "v2026.8.31",
       "hermes_commit": "29112bef099274229cadff79cdff7bf7b99c4b77", "backend_mode": "dashboard"}
NEW = {"schema_version": 1, "hermes_version": "0.22.5", "hermes_git_ref": "v0.22.5",
       "hermes_commit": "bf0203a794a07aac3ff764972ab9087baa4e4f4e", "backend_mode": "serve"}
NATIVE = Path("/opt/hermes-agent")
MARKER = ".clawmanager-hermes-workspace.json"


def census(home):
    path = home / "state.db"
    if not path.exists():
        return {}
    if path.is_symlink():
        raise RuntimeError("state_db_symlink")
    with sqlite3.connect(path.resolve().as_uri() + "?mode=ro", uri=True) as db:
        if db.execute("PRAGMA integrity_check").fetchone() != ("ok",):
            raise RuntimeError("source_database_integrity")
        result = {}
        names = {r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table'")}
        for table, fields in {"sessions": ("id", "title", "source"),
                              "messages": ("id", "session_id", "role", "content")}.items():
            if table not in names:
                continue
            columns = {r[1] for r in db.execute(f'PRAGMA table_info("{table}")')}
            selected = [c for c in fields if c in columns]
            if "id" not in selected:
                raise RuntimeError("unsupported_history_schema")
            rows = db.execute('SELECT ' + ','.join('"' + c + '"' for c in selected)
                              + f' FROM "{table}" ORDER BY "id"').fetchall()
            result[table] = {"columns": selected, "rows": len(rows),
                             "sha256": hashlib.sha256(json.dumps(rows, ensure_ascii=False).encode()).hexdigest()}
        return result


def migrate(home):
    env = os.environ.copy()
    env["HERMES_HOME"] = str(home)
    # Native initialization reads/migrates this explicit database only; it
    # does not start serve, the gateway, a model or the Redis consumer.
    subprocess.run([str(NATIVE / ".venv/bin/python"), str(NATIVE / "hermes_cli/lite_startup.py")],
                   env=env, cwd=NATIVE, check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--workspace", type=Path, required=True)
    parser.add_argument("--confirmed-stopped", action="store_true", required=True)
    args = parser.parse_args()
    workspace = args.workspace
    home = workspace / "home"
    marker = home / ".hermes" / MARKER
    if os.geteuid() < 200001 or workspace.is_symlink() or home.is_symlink():
        raise RuntimeError("instance_identity_required")
    if workspace.stat().st_uid != os.geteuid() or home.stat().st_uid != os.geteuid():
        raise RuntimeError("workspace_owner_mismatch")
    if marker.is_symlink() or json.loads(marker.read_text()) != OLD:
        raise RuntimeError("unsupported_source_workspace_version")
    lock = json.loads((Path("/usr/local/share/hermes-lite/source-lock.json")).read_text())
    if lock["commit"] != NEW["hermes_commit"] or lock["tag"] != NEW["hermes_git_ref"]:
        raise RuntimeError("target_image_lock_mismatch")
    root = workspace / ".clawmanager-hermes-migrations"
    if root.is_symlink():
        raise RuntimeError("unsafe_backup_root")
    root.mkdir(mode=0o700, exist_ok=True)
    if root.stat().st_uid != os.geteuid() or root.stat().st_mode & 0o077:
        raise RuntimeError("unsafe_backup_permissions")
    backup = root / str(uuid.uuid4())
    backup.mkdir(mode=0o700)
    # Keep symlinks as links, never traverse Team/external workspace aliases.
    shutil.copytree(home, backup / "home", symlinks=True)
    targets = [Path(".hermes"), Path(".clawmanager-team-worker/.hermes")]
    before = {str(p): census(home / p) for p in targets if (home / p / "state.db").exists()}
    stage = backup / "validation"
    for relative in before:
        source = backup / "home" / relative
        if source.is_symlink():
            raise RuntimeError("unsafe_history_home")
        copied = stage / relative
        shutil.copytree(source, copied, symlinks=True)
        migrate(copied)
        if census(copied) != before[relative]:
            raise RuntimeError("isolated_history_changed")
    (backup / "evidence.json").write_text(json.dumps({"source": OLD, "target": NEW,
                                                    "history": before}, indent=2))
    for relative in before:
        if census(home / relative) != before[relative]:
            raise RuntimeError("source_changed_after_snapshot")
        migrate(home / relative)
        if census(home / relative) != before[relative]:
            raise RuntimeError("migrated_history_changed_keep_snapshot")
    temporary = marker.with_name(MARKER + ".migration-" + str(uuid.uuid4()))
    with temporary.open("x", encoding="utf-8") as stream:
        json.dump(NEW, stream, indent=2)
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, marker)
    print(json.dumps({"migration": "completed", "backup": str(backup), "history": before}))


if __name__ == "__main__":
    main()
