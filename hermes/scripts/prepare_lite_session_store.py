"""Initialize missing/old stores; healthy restarts perform read-only probes only."""
import os
import json
from contextlib import closing
from pathlib import Path
import sqlite3
import sys
import time


def timing(phase, action, started):
    # Fixed vocabulary and numeric timings only; no paths, secrets or DB content.
    elapsed = min(999999999, max(0, round((time.monotonic() - started) * 1000)))
    print(f"hermes_lite_startup phase={phase} action={action} elapsed_ms={elapsed}", file=sys.stderr, flush=True)


def probe(path, statements, version):
    try:
        size = path.stat().st_size
    except FileNotFoundError:
        return 'initialize'
    if path.is_symlink():
        raise RuntimeError('session_store_symlink')
    if size == 0:
        return 'initialize'
    # mode=ro: this fast path never creates a database, runs DDL or opens a writer.
    with closing(sqlite3.connect(path.resolve().as_uri() + '?mode=ro', uri=True, timeout=1)) as db:
        db.execute('PRAGMA query_only=ON')
        try:
            row = db.execute('SELECT version FROM schema_version LIMIT 1').fetchone()
            if row is not None and row[0] > version:
                raise RuntimeError('session_store_newer_than_runtime')
            for statement in statements:
                db.execute(statement).fetchone()
        except sqlite3.OperationalError as exc:
            from hermes_state_reconcile import is_declarative_drift_error
            if is_declarative_drift_error(exc):
                return 'migrate'
            raise  # Locked, corrupt or I/O failure is not permission to rewrite.
        if row is None or row[0] < version:
            return 'migrate'
        if row[0] != version:
            raise RuntimeError('session_store_newer_than_runtime')
    return 'skip'


def initialize(path):
    from hermes_state import SessionDB
    db = SessionDB(db_path=path)
    db.close()


def write_plan(path):
    # Generated from the locked source once during image construction, never
    # from a workspace. Release inventory binds this file to the deployed image.
    from hermes_state_schema import schema_read_probe_statements
    from hermes_state_common import SCHEMA_VERSION
    path.write_text(json.dumps({'version': SCHEMA_VERSION, 'statements': schema_read_probe_statements()}), encoding='utf-8')


def prepare(path):
    started = time.monotonic()
    try:
        step = time.monotonic()
        plan = json.loads(Path(__file__).with_name('lite_startup_schema.json').read_text(encoding='utf-8'))
        statements, version = plan['statements'], plan['version']
        timing('db_import', 'done', step)
        step = time.monotonic()
        action = probe(path, statements, version)
        timing('db_probe', action, step)
        if action != 'skip':
            step = time.monotonic()
            initialize(path)
            timing('db_initialize', action, step)
            if probe(path, statements, version) != 'skip':
                raise RuntimeError('session_store_schema_not_ready')
        timing('db_total', action, started)
        return action
    except Exception:
        timing('db_total', 'failed', started)
        raise


if __name__ == '__main__':
    try:
        if len(sys.argv) == 3 and sys.argv[1] == '--write-plan':
            write_plan(Path(sys.argv[2]))
        else:
            prepare(Path(os.environ['HERMES_HOME']) / 'state.db')
    except Exception:
        # The agent reports startup failure; don't leak DB errors or paths.
        sys.exit(1)
