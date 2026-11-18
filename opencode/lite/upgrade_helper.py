"""Fixed OpenCode 1.18.13 preservation actions, run by the scoped UID helper.

No project discovery, native server startup, schema migration, or live checkpoint.
The trusted parent owns writer isolation and the maintenance container lifecycle.
"""
import base64
import contextlib
import datetime
import hashlib
import json
import os
from pathlib import Path
import shutil
import sqlite3
import stat
import sys
import tempfile
import uuid

ROOTS = ("home/.opencode", "home/.local/share/opencode", "home/.local/state/opencode",
         "home/.config/opencode")
DB = "home/.local/share/opencode/opencode.db"
HISTORY = ("session", "message", "part", "session_message", "session_input", "session_context_epoch")
PROTOCOL = "opencode-preserve-v1"


def require(ok, code):
    if not ok:
        raise ValueError(code)


def encode(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode()


def digest(value):
    return hashlib.sha256(encode(value)).hexdigest()


def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")


def safe_parts(relative):
    parts = relative.split("/")
    require(parts and all(p not in ("", ".", "..") for p in parts), "unsafe_path")
    return parts


class Tree:
    """Descriptor relative traversal: never follow a workspace-controlled link."""
    def __init__(self, path, uid, capsule=False):
        self.uid = uid
        self.path = Path(path)
        self.fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        st = os.fstat(self.fd)
        require(st.st_uid == uid or capsule and st.st_uid == 10001 and st.st_gid == uid,
                "unsafe_tree_owner")

    def close(self):
        os.close(self.fd)

    def parent(self, name):
        parts = safe_parts(name)
        fd = os.dup(self.fd)
        try:
            for p in parts[:-1]:
                child = os.open(p, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
                os.close(fd)
                fd = child
                require(os.fstat(fd).st_uid == self.uid, "unsafe_directory_owner")
            return fd, parts[-1]
        except BaseException:
            os.close(fd)
            raise

    @contextlib.contextmanager
    def file(self, name):
        fd, leaf = self.parent(name)
        try:
            file = os.open(leaf, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=fd)
            try:
                st = os.fstat(file)
                require(stat.S_ISREG(st.st_mode) and st.st_nlink == 1 and st.st_uid == self.uid,
                        "unsafe_native_file")
                with os.fdopen(file, "rb", closefd=False) as stream:
                    yield stream, st
                after = os.fstat(file)
                require((st.st_ino, st.st_size, st.st_mtime_ns) ==
                        (after.st_ino, after.st_size, after.st_mtime_ns), "native_file_changed")
            finally:
                os.close(file)
        finally:
            os.close(fd)

    def read(self, name):
        with self.file(name) as (stream, st):
            data = stream.read((32 << 20) + 1)
            require(len(data) <= 32 << 20, "native_metadata_too_large")
            return data, stat.S_IMODE(st.st_mode)

    def scan(self, name, destination=None):
        result, count = hashlib.sha256(), 0
        output = None
        try:
            if destination is not None:
                fd = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
                output = os.fdopen(fd, "wb")
            with self.file(name) as (stream, st):
                while True:
                    chunk = stream.read(1 << 16)
                    if not chunk:
                        break
                    result.update(chunk)
                    count += len(chunk)
                    if output is not None:
                        output.write(chunk)
            if output is not None:
                output.flush()
                os.fsync(output.fileno())
            return {"sha256": result.hexdigest(), "bytes": count, "mode": stat.S_IMODE(st.st_mode)}
        finally:
            if output is not None:
                output.close()

    def files(self, roots):
        facts = {}

        def visit(fd, relative, depth):
            require(depth < 64, "native_tree_too_deep")
            for name in sorted(os.listdir(fd)):
                child = relative + "/" + name
                st = os.stat(name, dir_fd=fd, follow_symlinks=False)
                require(st.st_uid == self.uid, "unsafe_native_owner")
                if stat.S_ISDIR(st.st_mode):
                    sub = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
                    try:
                        visit(sub, child, depth + 1)
                    finally:
                        os.close(sub)
                else:
                    require(stat.S_ISREG(st.st_mode) and st.st_nlink == 1, "unsafe_native_file")
                    facts[child] = self.scan(child)
                    require(len(facts) <= 100000, "native_file_count_exceeded")

        present = []
        for root in roots:
            try:
                fd, name = self.parent(root)
            except FileNotFoundError:
                continue
            try:
                try:
                    sub = os.open(name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
                except FileNotFoundError:
                    continue
                try:
                    require(os.fstat(sub).st_uid == self.uid, "unsafe_native_owner")
                    present.append(root)
                    visit(sub, root, 0)
                finally:
                    os.close(sub)
            finally:
                os.close(fd)
        return facts, present


def write_new(path, data, mode=0o600):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, mode)
    with os.fdopen(fd, "wb") as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())


def sync_dir(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def publish(path, value):
    raw = encode(value)
    if path.exists():
        require(path.read_bytes() == raw, "receipt_already_exists")
        return
    temp = path.with_name(path.name + ".tmp-" + uuid.uuid4().hex)
    write_new(temp, raw)
    # Link is an atomic no-replace publication. The temporary second link is
    # removed before a reader is allowed to validate the result.
    os.link(temp, path, follow_symlinks=False)
    temp.unlink()
    sync_dir(path.parent)


def read_receipt(tree, name):
    # A crash after link(no-replace), before unlink(temp), leaves two names for
    # the same receipt. Accept only those exact private temporary names. Status
    # mounts are read-only: retain the names instead of attempting cleanup.
    # Arbitrary hardlinks in user data remain prohibited.
    fd = os.open(name, os.O_RDONLY | os.O_NOFOLLOW, dir_fd=tree.fd)
    try:
        st = os.fstat(fd)
        require(stat.S_ISREG(st.st_mode) and st.st_uid == tree.uid, "unsafe_receipt")
        if st.st_nlink > 1:
            pending = []
            for candidate in os.listdir(tree.fd):
                prefix = name + ".tmp-"
                suffix = candidate[len(prefix):] if candidate.startswith(prefix) else ""
                if len(suffix) != 32 or any(c not in "0123456789abcdef" for c in suffix):
                    continue
                other = os.stat(candidate, dir_fd=tree.fd, follow_symlinks=False)
                if (other.st_dev, other.st_ino) == (st.st_dev, st.st_ino):
                    pending.append(candidate)
            require(len(pending) == st.st_nlink - 1, "unsafe_receipt_links")
        with os.fdopen(fd, "rb", closefd=False) as stream:
            raw = stream.read((32 << 20) + 1)
        require(len(raw) <= 32 << 20, "native_metadata_too_large")
        after = os.fstat(fd)
        require((st.st_ino, st.st_size, st.st_mtime_ns, st.st_nlink) ==
                (after.st_ino, after.st_size, after.st_mtime_ns, after.st_nlink), "receipt_changed")
        return raw
    finally:
        os.close(fd)


def quote(name):
    return '"' + name.replace('"', '""') + '"'


def typed(value):
    if value is None:
        return ["null"]
    if isinstance(value, bytes):
        return ["blob", base64.b64encode(value).decode()]
    if isinstance(value, int):
        return ["integer", str(value)]
    if isinstance(value, float):
        return ["real", value.hex()]
    return ["text", value]


def db_open(path):
    # mode=ro still observes WAL. immutable=1 would silently lose WAL commits.
    db = sqlite3.connect(path.as_uri() + "?mode=ro", uri=True, timeout=5)
    db.execute("PRAGMA query_only=ON")
    return db


def catalog(path, schema):
    db = db_open(path)
    try:
        db.execute("BEGIN")
        require(db.execute("PRAGMA integrity_check").fetchall() == [("ok",)], "database_integrity_failed")
        require(not db.execute("PRAGMA foreign_key_check").fetchall(), "database_foreign_keys_failed")
        require([r[0] for r in db.execute("SELECT id FROM migration ORDER BY id")] ==
                sorted(schema["migrations"]), "unsupported_migration_journal")
        reference = sqlite3.connect(":memory:")
        try:
            reference.executescript("\n".join(schema["sql"]))
            reference.execute("CREATE TABLE migration(id TEXT PRIMARY KEY,time_completed INTEGER NOT NULL)")
            expected = [r[0] for r in reference.execute("SELECT name FROM sqlite_master WHERE type='table'")]
            actual = [r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")]
            require(set(actual) in (set(expected), set(expected) | {"__drizzle_migrations"}),
                    "unsupported_database_tables")
            require(not db.execute("SELECT 1 FROM sqlite_master WHERE type IN ('view','trigger')").fetchall(),
                    "unsupported_database_objects")
            for table in expected:
                for pragma in ("table_info", "foreign_key_list"):
                    require(db.execute("PRAGMA " + pragma + "(" + quote(table) + ")").fetchall() ==
                            reference.execute("PRAGMA " + pragma + "(" + quote(table) + ")").fetchall(),
                            "unsupported_database_schema")
        finally:
            reference.close()
        tables = {}
        for table in sorted(actual):
            rows = sorted(digest([typed(v) for v in row]) for row in db.execute("SELECT * FROM " + quote(table)))
            tables[table] = {"count": len(rows), "sha256": digest(rows)}
        return {"tables": tables, "sha256": digest(tables),
                "session_sha256": digest({t: tables[t] for t in HISTORY}),
                "session_count": tables["session"]["count"]}
    finally:
        db.close()


def identity(req):
    return {k: req[k] for k in ("rollout_id", "instance_id", "user_id", "generation", "uid", "gid")}


def checked_capsule(tree, req):
    raw, _ = tree.read("opencode-preserve/manifest.json")
    manifest = json.loads(raw)
    require(manifest["identity"] == identity(req) and manifest["protocol"] == PROTOCOL,
            "capsule_identity_mismatch")
    actual, _ = tree.files(["opencode-preserve"])
    actual.pop("opencode-preserve/manifest.json")
    require(actual == manifest["files"], "capsule_content_changed")
    return manifest, hashlib.sha256(raw).hexdigest()


@contextlib.contextmanager
def private_database(workspace, files):
    # SQLite read-only WAL access may update SHM read marks. Never open live
    # SQLite, even for preflight: inspect a stable private DB/sidecar copy.
    with tempfile.TemporaryDirectory(prefix="opencode-db-check-") as scratch:
        for suffix in ("", "-wal", "-shm", "-journal"):
            name = DB + suffix
            if name in files:
                fact = workspace.scan(name, Path(scratch) / ("opencode.db" + suffix))
                require(fact == files[name],
                        "source_changed_during_snapshot")
        yield Path(scratch) / "opencode.db"
        for suffix in ("", "-wal", "-shm", "-journal"):
            name = DB + suffix
            if name in files:
                require(workspace.scan(name) == files[name],
                        "source_changed_during_snapshot")


def current_inventory(workspace, schema):
    files, present = workspace.files(ROOTS)
    require(DB in files, "missing_native_database")
    config, _ = workspace.read("home/.opencode/opencode.json")
    require(isinstance(json.loads(config), dict), "invalid_native_config")
    with private_database(workspace, files) as path:
        result = catalog(path, schema)
    return files, present, result


def require_original_history(workspace, files, original_path):
    with private_database(workspace, files) as current_path:
        original, current = db_open(original_path), db_open(current_path)
        try:
            for (table,) in original.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"):
                # New activity legitimately updates session counters/titles and
                # timestamps. The stable session identity and all original
                # messages, queued inputs, credentials and events must remain.
                columns = "id,project_id,parent_id,directory" if table == "session" else "*"
                query = "SELECT " + columns + " FROM " + quote(table)
                current_rows = {digest([typed(v) for v in row]) for row in current.execute(query)}
                for row in original.execute(query):
                    require(digest([typed(v) for v in row]) in current_rows,
                            "original_native_history_missing")
        finally:
            current.close()
            original.close()


def migration_status(capsule, req):
    manifest, output = checked_capsule(capsule, req)
    raw = read_receipt(capsule, "opencode-preserve-receipt.json")
    result = json.loads(raw)
    require(result == migration_result(manifest, output), "migration_receipt_mismatch")
    return result, manifest


def migrate(workspace, capsule, req, schema):
    result_path = capsule.path / "opencode-preserve-receipt.json"
    if result_path.exists():
        return migration_status(capsule, req)[0]
    if (capsule.path / "opencode-preserve").exists():
        manifest, output = checked_capsule(capsule, req)
        result = migration_result(manifest, output)
        publish(result_path, result)
        return result
    before, roots, source = current_inventory(workspace, schema)
    require(shutil.disk_usage(capsule.path).free > sum(f["bytes"] for f in before.values()) * 3 + (16 << 20),
            "insufficient_capsule_space")
    stage = Path(tempfile.mkdtemp(prefix=".opencode-preserve-", dir=capsule.path))
    write_new(stage / "intent.json", encode({"identity": identity(req), "protocol": PROTOCOL}))
    original = stage / "original"
    original.mkdir(mode=0o700)
    for name, fact in before.items():
        target = original / name
        target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        require(workspace.scan(name, target) == fact, "source_changed_during_snapshot")
    # Never open the raw archive with SQLite. Recovery/checkpoint may alter its
    # sidecars, so work from a second private copy and retain the original WAL.
    with tempfile.TemporaryDirectory(prefix=".sqlite-", dir=stage) as scratch:
        for suffix in ("", "-wal", "-shm", "-journal"):
            source_file = original / (DB + suffix)
            if source_file.exists():
                shutil.copyfile(source_file, Path(scratch) / ("opencode.db" + suffix))
        src = db_open(Path(scratch) / "opencode.db")
        target = sqlite3.connect(stage / "database.sqlite")
        try:
            src.backup(target)
            target.commit()
        finally:
            target.close()
            src.close()
    os.chmod(stage / "database.sqlite", 0o600)
    saved = catalog(stage / "database.sqlite", schema)
    after, _, current = current_inventory(workspace, schema)
    require(before == after and source == saved == current, "source_changed_during_snapshot")
    # Retain intent too: it binds an interrupted unpublished directory to its
    # original operation. A later attempt never deletes or reuses that data.
    stage_tree = Tree(stage, req["uid"])
    try:
        # Tree.files requires directory roots; collect stage under its capsule
        # parent and remap the eventual atomically published name.
        facts, _ = capsule.files([stage.name])
        facts = {"opencode-preserve/" + k.split("/", 1)[1]: v for k, v in facts.items()}
    finally:
        stage_tree.close()
    manifest = {"schema_version": 1, "protocol": PROTOCOL, "identity": identity(req),
                "native_revision": schema["revision"], "native_version": schema["version"],
                "journal_sha256": digest(sorted(schema["migrations"])),
                "original_roots": roots, "original_files": before,
                "database": saved, "files": facts, "created_at": now()}
    raw = encode(manifest)
    write_new(stage / "manifest.json", raw)
    sync_dir(stage)
    os.rename(stage, capsule.path / "opencode-preserve")
    sync_dir(capsule.path)
    result = migration_result(manifest, hashlib.sha256(raw).hexdigest())
    publish(result_path, result)
    return result


def migration_result(manifest, output):
    req, facts, saved = manifest["identity"], manifest["files"], manifest["database"]
    before = manifest["original_files"]
    return {"instance_id": req["instance_id"], "rollout_id": req["rollout_id"],
            "generation": req["generation"], "status": "validated", "output_sha256": output,
            "archive_bytes": sum(f["bytes"] for f in facts.values()), "archive_files": len(facts),
            "rollback_available": True, "session_count": saved["session_count"],
            "session_catalog_sha256": saved["session_sha256"], "completed_at": manifest["created_at"],
            "data_preserved": True,
            "preservation_sha256": digest({"files": before, "database": saved["sha256"]}),
            "config_original_sha256": before["home/.opencode/opencode.json"]["sha256"],
            "config_target_sha256": before["home/.opencode/opencode.json"]["sha256"]}


def restore(workspace, capsule, req, schema, status_only=False):
    original = dict(req)
    if req.get("resume_generation"):
        require(req["generation"] in (req["resume_generation"], req["resume_generation"] + 1),
                "restore_generation_mismatch")
        original["generation"] = req["resume_generation"]
    _, manifest = migration_status(capsule, original)
    path = capsule.path / "opencode-preserve-restore.json"
    preserve = bool(req.get("preserve_session_sqlite"))
    if path.exists():
        raw = read_receipt(capsule, path.name)
        receipt = json.loads(raw)
        require(receipt["identity"] == identity(req) and receipt["preserve"] == preserve,
                "restore_identity_mismatch")
        return receipt["result"]
    require(not status_only, "restore_not_completed")
    files, _, current = current_inventory(workspace, schema)
    require_original_history(workspace, files, capsule.path / "opencode-preserve/database.sqlite")
    if not preserve:
        # No schema was changed in this strategy. Reverting a database with new
        # committed rows is unsafe; the caller must explicitly preserve it.
        require(current == manifest["database"], "restore_would_discard_new_history")
    # Both binaries use the identical schema. Retaining the complete current
    # state is the rollback, including messages committed since activation.
    # No deletion or file replacement is needed or permitted by this strategy.
    result = {"instance_id": req["instance_id"], "rollout_id": req["rollout_id"],
              "generation": req["generation"], "status": "restored",
              "output_sha256": digest({"original": manifest["database"], "current": current,
                                       "preserve": preserve}),
              "config_restored": True, "state_restored": True,
              "preserved_session_sqlite": preserve, "completed_at": now()}
    publish(path, {"identity": identity(req), "preserve": preserve, "result": result})
    return result


def main(payload):
    req = payload["request"]
    require(req["uid"] == os.geteuid() and req["gid"] == os.getegid(), "native_identity_mismatch")
    schema = payload["schema"]
    require(schema["revision"] == "a105350812f05f914c768e468559dbd6bd508d8e" and
            schema["version"] == "1.18.13" and len(schema["migrations"]) == 38,
            "unsupported_native_revision")
    workspace = Tree(payload["workspace"], req["uid"])
    capsule = Tree(payload["capsule"], req["uid"], capsule=True)
    try:
        action = payload["action"]
        if action == "migrate":
            return migrate(workspace, capsule, req, schema)
        if action == "migration-status":
            return migration_status(capsule, req)[0]
        if action in ("restore", "restore-status"):
            return restore(workspace, capsule, req, schema, action == "restore-status")
        files, _, db = current_inventory(workspace, schema)
        if action == "inventory":
            return {"workspace_path": str(workspace.path), "file_count": len(files),
                    "total_bytes": sum(f["bytes"] for f in files.values()),
                    "available_bytes": shutil.disk_usage(capsule.path).free,
                    "database_files": [{"relative_path": DB, "size_bytes": files[DB]["bytes"],
                                        "sqlite_header": True, "official_ok": True,
                                        "output_sha256": db["sha256"]}], "checked_at": now()}
        require(action == "compatibility", "unsupported_native_action")
        config = files["home/.opencode/opencode.json"]
        return {"instance_id": req["instance_id"], "status": "compatible",
                "config_original_sha256": config["sha256"], "config_target_sha256": config["sha256"],
                "config_bytes": config["bytes"], "session_bytes": files[DB]["bytes"],
                "state_bytes": sum(f["bytes"] for f in files.values()),
                "available_bytes": shutil.disk_usage(capsule.path).free,
                "config_validated": True, "doctor_validated": True, "session_dry_run_valid": True,
                "probe_output_sha256": digest({"identity": identity(req), "database": db,
                                                "journal": schema["migrations"]}), "checked_at": now()}
    finally:
        capsule.close()
        workspace.close()


if __name__ == "__main__":
    os.umask(0o077)
    try:
        result = main(json.load(sys.stdin))
        sys.stdout.buffer.write(encode(result))
    except BaseException as error:
        # SQLite errors may contain user identifiers or values. Only fixed
        # helper categories leave this private process.
        code = str(error) if type(error) is ValueError else "native_preservation_failed"
        sys.stderr.write(code + "\n")
        sys.exit(1)
