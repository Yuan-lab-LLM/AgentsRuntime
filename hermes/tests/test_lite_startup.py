import hashlib
import importlib.util
import os
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

SOURCE = Path(os.environ.get('LITE_STARTUP_FILE', Path(__file__).resolve().parents[1] / 'scripts/prepare_lite_session_store.py'))
spec = importlib.util.spec_from_file_location('lite_startup_test', SOURCE)
startup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(startup)


@unittest.skipUnless(importlib.util.find_spec('hermes_state'), 'requires pinned installed Hermes')
class StartupTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name)
        self.env = patch.dict(os.environ, {'HERMES_HOME': str(self.home)})
        self.env.start()
        self.addCleanup(self.env.stop)
        self.db = self.home / 'state.db'

    def digest(self):
        return hashlib.sha256(self.db.read_bytes()).hexdigest()

    def test_new_and_healthy_restart(self):
        self.assertEqual(startup.prepare(self.db), 'initialize')
        original, mtime = self.digest(), self.db.stat().st_mtime_ns
        with patch.object(startup, 'initialize', side_effect=AssertionError('healthy restart opened writer')):
            self.assertEqual(startup.prepare(self.db), 'skip')
            self.assertEqual(startup.prepare(self.db), 'skip')
        self.assertEqual((self.digest(), self.db.stat().st_mtime_ns), (original, mtime))

    def test_old_version_migrates_once_and_preserves_rows(self):
        startup.prepare(self.db)
        with sqlite3.connect(self.db) as db:
            db.execute('CREATE TABLE startup_preservation (value TEXT)')
            db.execute("INSERT INTO startup_preservation VALUES ('retain')")
            db.execute('UPDATE schema_version SET version=version-1')
        self.assertEqual(startup.prepare(self.db), 'migrate')
        self.assertEqual(startup.prepare(self.db), 'skip')
        with sqlite3.connect(self.db) as db:
            self.assertEqual(db.execute('SELECT value FROM startup_preservation').fetchone(), ('retain',))

    def test_schema_drift_migrates(self):
        startup.prepare(self.db)
        with sqlite3.connect(self.db) as db:
            db.execute('ALTER TABLE sessions DROP COLUMN title_source')
        self.assertEqual(startup.prepare(self.db), 'migrate')
        self.assertEqual(startup.prepare(self.db), 'skip')

    def test_corruption_and_future_version_never_initialize(self):
        self.db.write_bytes(b'not a SQLite database')
        original = self.digest()
        with patch.object(startup, 'initialize', side_effect=AssertionError('must not initialize corrupt store')):
            with self.assertRaises(sqlite3.DatabaseError): startup.prepare(self.db)
        self.assertEqual(self.digest(), original)
        self.db.unlink()
        startup.prepare(self.db)
        with sqlite3.connect(self.db) as db: db.execute('UPDATE schema_version SET version=99999')
        with patch.object(startup, 'initialize', side_effect=AssertionError('must not downgrade')):
            with self.assertRaises(RuntimeError): startup.prepare(self.db)

    def test_locked_store_fails_without_writer_fallback(self):
        startup.prepare(self.db)
        db = sqlite3.connect(self.db)
        try:
            db.execute('PRAGMA journal_mode=DELETE')
            db.execute('BEGIN EXCLUSIVE')
            with patch.object(startup, 'initialize', side_effect=AssertionError('must not rewrite locked store')):
                with self.assertRaises(sqlite3.OperationalError): startup.prepare(self.db)
        finally:
            db.rollback()
            db.close()


if __name__ == '__main__': unittest.main()
