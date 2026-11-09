"""Receipt recovery must outlive a tool loop and fence stale assignment writes."""
import asyncio
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from test_adapter_contract import adapter


class CompletionRecoveryTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.settings = adapter.RedisTeamSettings(
            enabled=True, redis_url="redis://example.invalid:6379/0", team_id="42",
            member_id="developer", role="developer", shared_dir=self.tmp.name,
        )
        adapter.ensure_team_dirs(self.settings)
        self.envelope = {"taskId": "root-1", "rootTaskId": "root-1", "assignmentId": "dev-1", "revision": 1}
        adapter._persist_active_envelope(self.settings, self.envelope)
        self.cid = adapter._completion_id(self.settings, self.envelope)
        self.attempt = "attempt-1"
        self.values = {}
        self.redis = mock.Mock()
        self.redis.command = mock.AsyncMock(side_effect=lambda *args: self.values.get(args[1]))

    def pending(self, decision="submitted"):
        self.assertTrue(adapter._record_completion_decision(
            self.settings, self.envelope, self.cid, self.attempt, "succeeded", "delivery", [], decision,
            new_attempt=True, explicit=True,
        ))

    def ack(self, decision, *, state=False, attempt=None):
        key = (adapter._completion_state_key(self.settings, self.cid) if state else
               adapter._completion_ack_key(self.settings, self.cid, self.attempt))
        self.values[key] = json.dumps({"completionId": self.cid, "attemptId": attempt or self.attempt, "decision": decision})

    def reconcile(self):
        asyncio.run(adapter._reconcile_pending_completion(self.redis, self.settings))

    def test_tool_loop_exit_then_late_ack_recovers_on_new_loop(self):
        async def tool():
            self.pending()
        asyncio.run(tool())  # No surviving asyncio task; only the durable cursor.
        self.reconcile()
        self.assertFalse(adapter._load_active_envelope(self.settings)["terminal"])
        self.ack("accepted")
        self.reconcile()
        self.assertTrue(adapter._load_active_envelope(self.settings)["terminal"])
        status = adapter.read_team_statuses(self.settings, "developer")
        self.assertEqual((status["runtimeStatus"], status["availability"]), ("succeeded", "idle"))

    def test_deferred_attempt_does_not_hide_later_terminal_state(self):
        self.pending("deferred")
        self.ack("deferred")
        self.reconcile()
        self.assertFalse(adapter._load_active_envelope(self.settings)["terminal"])
        self.ack("accepted", state=True)
        self.reconcile()
        self.assertTrue(adapter._load_active_envelope(self.settings)["terminal"])

    def test_rejected_receipt_retains_assignment_for_correction(self):
        self.pending()
        self.ack("rejected")
        self.reconcile()
        active = adapter._load_active_envelope(self.settings)
        self.assertFalse(active["terminal"])
        self.assertEqual(active["completionDecision"], "rejected")
        self.assertEqual(adapter.read_team_statuses(self.settings, "developer")["runtimeStatus"], "running")

    def test_replacement_during_receipt_io_cannot_be_overwritten(self):
        for change in ({"rootTaskId": "root-2", "taskId": "root-2"}, {"assignmentId": "dev-2"},
                       {"revision": 2}, {"completionAttemptId": "attempt-2"}, {"terminal": True}):
            with self.subTest(change=change):
                adapter._persist_active_envelope(self.settings, self.envelope)
                self.pending()
                replacement = {**adapter._load_active_envelope(self.settings), **change}
                async def race(*args, **kwargs):
                    adapter._persist_active_envelope(self.settings, replacement)
                    return {"decision": "accepted"}
                with mock.patch.object(adapter, "_completion_ack", race):
                    self.reconcile()
                self.assertEqual(adapter._load_active_envelope(self.settings), replacement)

    def test_stale_receipt_and_post_terminal_updates_are_ignored(self):
        self.pending()
        self.ack("accepted", attempt="old-attempt")
        self.reconcile()
        self.assertFalse(adapter._load_active_envelope(self.settings)["terminal"])
        self.ack("accepted")
        self.reconcile()
        before = adapter._load_active_envelope(self.settings)
        self.assertFalse(adapter._record_completion_decision(
            self.settings, self.envelope, self.cid, self.attempt, "succeeded", "late pending", [], "deferred",
        ))
        self.ack("rejected")
        self.reconcile()
        self.assertEqual(adapter._load_active_envelope(self.settings), before)

    def test_presence_loop_owns_recovery(self):
        source = Path(adapter.__file__).read_text(encoding="utf-8")
        presence = source.split("async def _presence_loop", 1)[1].split("async def ", 1)[0]
        self.assertIn("await _reconcile_pending_completion", presence)
        self.assertNotIn("_observe_late_completion", source)


if __name__ == "__main__":
    unittest.main()
