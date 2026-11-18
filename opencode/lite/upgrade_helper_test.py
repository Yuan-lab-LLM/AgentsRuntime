"""Real SQLite/WAL regression; executed on the test cluster, never locally."""
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest

HERE = Path(__file__).parent


class PreserveTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.workspace = Path(self.tmp.name) / "workspace"
        self.capsule = Path(self.tmp.name) / "capsule"
        self.capsule.mkdir(mode=0o700)
        self.data = self.workspace / "home/.local/share/opencode"
        self.data.mkdir(parents=True, mode=0o700)
        config = self.workspace / "home/.opencode"
        config.mkdir(mode=0o700)
        (config / "opencode.json").write_text('{"provider":{}}')
        (self.workspace / "project").mkdir()
        (self.workspace / "project/never-copy.sqlite").write_bytes(b"user project")
        self.req = dict(rollout_id="r1", instance_id=7, user_id=3, generation=2,
                        uid=os.geteuid(), gid=os.getegid())

    def fixture(self):
        manifest = json.loads((HERE / "upgrade_schema.json").read_text())
        db = sqlite3.connect(self.data / "opencode.db")
        self.addCleanup(db.close)
        db.executescript("\n".join(manifest["sql"]))
        db.execute("PRAGMA journal_mode=WAL")
        db.execute("PRAGMA wal_autocheckpoint=0")
        db.execute('CREATE TABLE "migration" (id TEXT PRIMARY KEY, time_completed INTEGER NOT NULL)')
        db.executemany("INSERT INTO migration VALUES(?,1)", [(x,) for x in manifest["migrations"]])
        db.execute("INSERT INTO project(id,worktree,time_created,time_updated,sandboxes) VALUES('prj','/workspace/project',1,1,'[]')")
        db.execute("INSERT INTO session(id,project_id,slug,directory,title,version,time_created,time_updated) VALUES('ses','prj','s','/workspace/project','Fixture','1.18.13',1,1)")
        db.execute("INSERT INTO session_message(id,session_id,type,seq,time_created,time_updated,data) VALUES('msg','ses','user',1,1,1,'{\"text\":\"last WAL commit\"}')")
        db.execute("INSERT INTO message(id,session_id,time_created,time_updated,data) VALUES('legacy','ses',1,1,'{\"role\":\"user\",\"text\":\"original history\"}')")
        db.execute("INSERT INTO part(id,message_id,session_id,time_created,time_updated,data) VALUES('part','legacy','ses',1,1,'{}')")
        db.execute("INSERT INTO session_input(id,session_id,prompt,delivery,admitted_seq,time_created) VALUES('input','ses','{}','steer',1,1)")
        db.execute("INSERT INTO session_context_epoch(session_id,baseline,snapshot,baseline_seq) VALUES('ses','baseline','{}',1)")
        db.execute("INSERT INTO credential(id,label,value,time_created,time_updated) VALUES('cred','fixture','secret-preserved-never-printed',1,1)")
        db.execute("INSERT INTO event_sequence(aggregate_id,seq) VALUES('ses',1)")
        db.execute("INSERT INTO event(id,aggregate_id,seq,type,data) VALUES('evt','ses',1,'fixture','{}')")
        db.commit()
        return db

    def call(self, action, ok=True, fault=None, **extra):
        payload = dict(action=action, workspace=str(self.workspace), capsule=str(self.capsule),
                       request={**self.req, **extra}, schema=json.loads((HERE / "upgrade_schema.json").read_text()))
        command = ["/usr/bin/python3", "-I", "-B", str(HERE / "upgrade_helper.py")]
        if fault:
            script = "import errno,os,runpy\n"
            if fault == "rename_after_success":
                script += "original=os.rename\ndef fail(*a,**kw):\n original(*a,**kw)\n raise OSError(errno.ESTALE,'private fault path must not leak')\nos.rename=fail\n"
            elif fault == "capsule_fsync_after_write":
                script += "original=os.fsync\ndef fail(fd):\n original(fd)\n if os.readlink('/proc/self/fd/'+str(fd)).startswith(" + repr(str(self.capsule)) + "):\n  raise OSError(errno.EIO,'private fault path must not leak')\nos.fsync=fail\n"
            else:
                self.fail("unknown fault fixture")
            script += "runpy.run_path(" + repr(str(HERE / "upgrade_helper.py")) + ",run_name='__main__')\n"
            command = ["/usr/bin/python3", "-I", "-B", "-c", script]
        p = subprocess.run(command,
                           input=json.dumps(payload), text=True, capture_output=True)
        if ok:
            self.assertEqual(0, p.returncode, p.stderr)
            return json.loads(p.stdout)
        self.assertNotEqual(0, p.returncode, p.stdout)
        self.assertNotIn("last WAL commit", p.stderr)
        return p

    def test_wal_backup_preserves_native_history_and_projects_are_out_of_scope(self):
        db = self.fixture()
        self.assertGreater((self.data / "opencode.db-wal").stat().st_size, 0)
        source_bytes = {p.name: p.read_bytes() for p in self.data.iterdir()}
        result = self.call("migrate")
        self.assertEqual("validated", result["status"])
        self.assertTrue(result["data_preserved"])
        self.assertEqual(1, result["session_count"])
        saved = sqlite3.connect(self.capsule / "opencode-preserve/database.sqlite")
        self.addCleanup(saved.close)
        self.assertIn("last WAL commit", saved.execute("SELECT data FROM session_message").fetchone()[0])
        self.assertEqual("ok", saved.execute("PRAGMA integrity_check").fetchone()[0])
        for table in ("message", "part", "session_input", "session_context_epoch", "credential", "event"):
            self.assertEqual(1, saved.execute("SELECT count(*) FROM " + table).fetchone()[0])
        self.assertFalse(any("never-copy" in str(p) for p in self.capsule.rglob("*")))
        self.assertEqual(result, self.call("migrate"))
        self.assertEqual(result, self.call("migration-status"))
        self.assertEqual(source_bytes, {p.name: p.read_bytes() for p in self.data.iterdir()})
        self.assertEqual(1, db.execute("SELECT count(*) FROM session_message").fetchone()[0])

    def test_unknown_journal_and_schema_rejected_without_changes(self):
        db = self.fixture()
        db.execute("INSERT INTO migration VALUES('future-schema',1)")
        db.commit()
        before = (self.data / "opencode.db-wal").read_bytes()
        self.call("migrate", ok=False)
        self.assertEqual(before, (self.data / "opencode.db-wal").read_bytes())
        db.execute("DELETE FROM migration WHERE id='future-schema'")
        db.execute("ALTER TABLE session_message ADD COLUMN unsupported TEXT")
        db.commit()
        self.call("compatibility", ok=False)

    def test_missing_native_unique_index_is_rejected(self):
        db = self.fixture()
        db.execute("DROP INDEX event_aggregate_seq_idx")
        db.commit()
        before = (self.data / "opencode.db-wal").read_bytes()
        self.call("compatibility", ok=False)
        self.assertEqual(before, (self.data / "opencode.db-wal").read_bytes())
        self.assertFalse((self.capsule / "opencode-preserve").exists())

    def test_hidden_generated_column_is_rejected(self):
        db = self.fixture()
        db.execute("ALTER TABLE event ADD COLUMN generated_hidden TEXT GENERATED ALWAYS AS(type) VIRTUAL")
        db.commit()
        self.call("compatibility", ok=False)

    def test_changed_native_check_constraint_is_rejected(self):
        db = self.fixture()
        sql = db.execute("SELECT sql FROM sqlite_schema WHERE name='event'").fetchone()[0]
        at = sql.rfind(")")
        changed = sql[:at] + ", CHECK(seq >= 0)" + sql[at:]
        db.execute("PRAGMA writable_schema=ON")
        db.execute("UPDATE sqlite_schema SET sql=? WHERE name='event'", (changed,))
        db.commit()
        self.call("compatibility", ok=False)

    def test_unknown_drizzle_journal_layout_is_rejected(self):
        db = self.fixture()
        db.execute("CREATE TABLE __drizzle_migrations(arbitrary BLOB)")
        db.commit()
        self.call("compatibility", ok=False)

    def test_sqlite_like_custom_table_is_not_hidden_from_native_schema_validation(self):
        db = self.fixture()
        db.execute("CREATE TABLE sqliteXcustom(id TEXT PRIMARY KEY, data TEXT)")
        db.execute("INSERT INTO sqliteXcustom VALUES('id','must not be silently excluded')")
        db.commit()
        before = (self.data / "opencode.db-wal").read_bytes()
        self.call("compatibility", ok=False)
        self.assertEqual(before, (self.data / "opencode.db-wal").read_bytes())

    def test_sqlite_like_custom_trigger_is_not_hidden_from_native_schema_validation(self):
        db = self.fixture()
        db.execute("CREATE TRIGGER sqliteXcustom AFTER INSERT ON message BEGIN DELETE FROM event; END")
        db.commit()
        before = (self.data / "opencode.db-wal").read_bytes()
        self.call("compatibility", ok=False)
        self.assertEqual(before, (self.data / "opencode.db-wal").read_bytes())

    def test_restore_preserves_new_history_and_rejects_stale_identity(self):
        db = self.fixture()
        self.call("migrate")
        db.execute("INSERT INTO session_message(id,session_id,type,seq,time_created,time_updated,data) VALUES('new','ses','user',2,2,2,'{\"text\":\"new history\"}')")
        db.commit()
        self.call("restore", ok=False, preserve_session_sqlite=False)
        r = self.call("restore", preserve_session_sqlite=True)
        self.assertEqual("restored", r["status"])
        self.assertTrue(r["preserved_session_sqlite"])
        self.assertEqual(2, db.execute("SELECT count(*) FROM session_message").fetchone()[0])
        self.assertEqual(r, self.call("restore-status", preserve_session_sqlite=True))
        self.call("migration-status", ok=False, generation=3)

    def test_rollback_next_generation_uses_original_capsule(self):
        self.fixture()
        self.call("migrate")
        result = self.call("restore", generation=3, resume_generation=2, preserve_session_sqlite=True)
        self.assertEqual(3, result["generation"])
        self.assertEqual(result, self.call("restore-status", generation=3, resume_generation=2,
                                          preserve_session_sqlite=True))
        self.call("restore", ok=False, generation=4, resume_generation=2, preserve_session_sqlite=True)

    def test_preserve_rollback_requires_original_history_even_when_integrity_is_ok(self):
        db = self.fixture()
        self.call("migrate")
        db.execute("DELETE FROM session_message WHERE id='msg'")
        db.commit()
        self.assertEqual("ok", db.execute("PRAGMA integrity_check").fetchone()[0])
        self.call("restore", ok=False, preserve_session_sqlite=True)

    def test_preserve_rollback_allows_session_counters_and_appended_messages(self):
        db = self.fixture()
        self.call("migrate")
        db.execute("UPDATE session SET time_updated=99,cost=2,tokens_input=3")
        db.execute("INSERT INTO session_message(id,session_id,type,seq,time_created,time_updated,data) VALUES('new','ses','user',2,2,2,'{}')")
        db.commit()
        self.call("restore", preserve_session_sqlite=True)

    def test_preserve_rollback_allows_native_event_progress_and_credential_rotation(self):
        db = self.fixture()
        self.call("migrate")
        db.execute("INSERT INTO event(id,aggregate_id,seq,type,data) VALUES('next','ses',2,'fixture','{}')")
        db.execute("UPDATE event_sequence SET seq=2")
        db.execute("UPDATE credential SET value='rotated-secret',time_updated=2,active=1")
        db.execute("UPDATE session_context_epoch SET baseline_seq=2,snapshot='{\"new_epoch\":true}',baseline='new-baseline'")
        db.execute("UPDATE session_input SET promoted_seq=2")
        db.commit()
        self.call("restore", preserve_session_sqlite=True)

    def test_same_sequence_epoch_cannot_discard_original_baseline_or_snapshot(self):
        db = self.fixture()
        db.execute("UPDATE session_context_epoch SET baseline='original-baseline',snapshot='{\"original\":true}'")
        db.commit()
        self.call("migrate")
        db.execute("UPDATE session_context_epoch SET baseline='',snapshot='{}'")
        db.commit()
        self.call("restore", ok=False, preserve_session_sqlite=True)

    def test_fractional_sequence_is_not_native_epoch_progress(self):
        db = self.fixture()
        self.call("migrate")
        db.execute("UPDATE session_context_epoch SET baseline_seq=1.5,baseline='',snapshot='{}'")
        db.commit()
        self.call("restore", ok=False, preserve_session_sqlite=True)

    def test_preserve_rollback_rejects_deleted_original_legacy_message(self):
        db = self.fixture()
        self.call("migrate")
        db.execute("DELETE FROM part")
        db.execute("DELETE FROM message")
        db.commit()
        self.call("restore", ok=False, preserve_session_sqlite=True)

    def test_preserve_rollback_rejects_deleted_original_part(self):
        db = self.fixture()
        self.call("migrate")
        db.execute("DELETE FROM part")
        db.commit()
        self.call("restore", ok=False, preserve_session_sqlite=True)

    def test_preserve_rollback_rejects_deleted_original_input(self):
        db = self.fixture()
        self.call("migrate")
        db.execute("DELETE FROM session_input")
        db.commit()
        self.call("restore", ok=False, preserve_session_sqlite=True)

    def test_restore_rejects_lost_original_config_without_overwriting_current(self):
        self.fixture()
        config = self.workspace / "home/.opencode/opencode.json"
        config.write_text('{"provider":{},"permission":{"bash":"ask"}}')
        self.call("migrate")
        config.write_text("{}")
        self.call("restore", ok=False, preserve_session_sqlite=True)
        self.assertEqual("{}", config.read_text())

    def test_restore_and_restore_status_require_original_non_database_state(self):
        self.fixture()
        state = self.workspace / "home/.local/state/opencode/important.json"
        state.parent.mkdir(parents=True)
        state.write_text('{"setting":"preserved"}')
        self.call("migrate")
        self.call("restore", preserve_session_sqlite=True)
        state.unlink()
        self.call("restore-status", ok=False, preserve_session_sqlite=True)

    def test_restore_allows_config_additions_and_append_only_native_logs(self):
        self.fixture()
        log = self.data / "log/native.log"
        log.parent.mkdir()
        log.write_text("original log\n")
        self.call("migrate")
        (self.workspace / "home/.opencode/opencode.json").write_text('{"provider":{},"new_option":true}')
        log.write_text("original log\nnew log\n")
        self.call("restore", preserve_session_sqlite=True)

    def test_final_capsule_recovers_lost_receipt_publication(self):
        self.fixture()
        original = self.call("migrate")
        (self.capsule / "opencode-preserve-receipt.json").unlink()
        self.call("migration-status", ok=False)
        self.assertEqual(original, self.call("migrate"))

    def test_receipt_link_publication_recovers_interrupted_temp_unlink(self):
        self.fixture()
        original = self.call("migrate")
        receipt = self.capsule / "opencode-preserve-receipt.json"
        temporary = self.capsule / (receipt.name + ".tmp-" + "a" * 32)
        os.link(receipt, temporary)
        self.assertEqual(original, self.call("migration-status"))
        self.assertTrue(temporary.exists())  # status never writes its RO mount
        self.assertEqual(2, receipt.stat().st_nlink)

    def test_tampered_capsule_and_unsafe_links_rejected(self):
        db = self.fixture()
        self.call("migrate")
        with (self.capsule / "opencode-preserve/database.sqlite").open("ab") as f:
            f.write(b"tamper")
        self.call("migration-status", ok=False)

    def test_missing_database_is_not_an_empty_instance(self):
        self.call("inventory", ok=False)

    def test_symlink_and_hardlink_native_files_rejected(self):
        self.fixture()
        foreign = self.workspace / "project/never-copy.sqlite"
        (self.data / "unsafe").symlink_to(foreign)
        self.call("migrate", ok=False)
        (self.data / "unsafe").unlink()
        os.link(foreign, self.data / "unsafe")
        self.call("migrate", ok=False)

    def test_partial_backup_is_not_a_successful_receipt(self):
        self.fixture()
        incomplete = self.capsule / "opencode-preserve"
        incomplete.mkdir()
        (incomplete / "database.sqlite").write_bytes(b"partial")
        self.call("migration-status", ok=False)
        self.call("migrate", ok=False)

    def test_ambiguous_native_capsule_io_returns_uncertain_exit_and_can_resume(self):
        self.fixture()
        before = {p.name: p.read_bytes() for p in self.data.iterdir()}
        for fault in ("capsule_fsync_after_write", "rename_after_success"):
            result = self.call("migrate", ok=False, fault=fault)
            self.assertEqual(75, result.returncode, result.stderr)
            self.assertNotIn("private fault", result.stderr)
        self.assertEqual(before, {p.name: p.read_bytes() for p in self.data.iterdir()})
        self.assertEqual("validated", self.call("migrate")["status"])

    def test_known_schema_rejection_is_a_confirmed_failure(self):
        db = self.fixture()
        db.execute("DROP INDEX event_aggregate_seq_idx")
        db.commit()
        self.assertEqual(1, self.call("migrate", ok=False).returncode)

    def test_native_repository_cache_is_not_traversed_or_copied(self):
        self.fixture()
        project = self.data / "repos/actual-project/.git/objects"
        project.mkdir(parents=True)
        os.mkfifo(project / "must-never-open")
        (project / "source-object").write_bytes(b"project git object")
        self.call("migrate")
        self.assertFalse(any("repos" in p.parts for p in (self.capsule / "opencode-preserve/original").rglob("*")))
        self.assertTrue((project / "must-never-open").is_fifo())

    def test_native_snapshot_git_objects_are_not_traversed_or_copied(self):
        self.fixture()
        project = self.data / "snapshot/project-id/objects"
        project.mkdir(parents=True)
        os.mkfifo(project / "must-never-open")
        self.call("migrate")
        self.assertFalse(any("snapshot" in p.parts for p in (self.capsule / "opencode-preserve/original").rglob("*")))


if __name__ == "__main__":
    unittest.main()
