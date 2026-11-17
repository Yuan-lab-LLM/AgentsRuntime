"""Real handler/Redis tests; run only against the marked disposable fixture."""
import asyncio
import dataclasses
import json
import os
import tempfile
import types
import unittest
import uuid
from unittest import mock
from test_adapter_contract import adapter


@unittest.skipUnless(os.environ.get("HERMES_REDIS_TEST_URL"), "isolated Redis required")
class ContextHandoffTests(unittest.TestCase):
    def test_losing_context_observer_cannot_claim_after_serial_completion(self):
        with tempfile.TemporaryDirectory() as root:
            cfg = adapter.RedisTeamSettings(enabled=True, redis_url=os.environ["HERMES_REDIS_TEST_URL"], team_id="serial-winner", member_id="worker", shared_dir=root)
            envelope = {"taskId":"root", "messageId":"notice"}
            journal = adapter.TerminalJournal(cfg)
            journal.context(envelope, "queued")
            self.assertTrue(journal.started(envelope))
            self.assertTrue(journal.accepted(envelope))
            self.assertIsNone(journal.context(envelope, "submitting"))
            self.assertEqual(journal.read(envelope)["state"], "accepted")

    def test_closed_scope_can_compact_a_large_handoff(self):
        async def run():
            with tempfile.TemporaryDirectory() as root:
                cfg = adapter.RedisTeamSettings(enabled=True, redis_url=os.environ["HERMES_REDIS_TEST_URL"], team_id="compact-handoff", member_id="worker", shared_dir=root)
                envelope = {"messageId":"notice", "taskId":"root", "rootTaskId":"root", "assignmentId":"work", "revision":1}
                journal = adapter.TerminalJournal(cfg)
                journal.started(envelope)
                journal.context(envelope, "queued", owned=True, handoff={"text":"x" * 16000, "activeMessageId":"original"})
                journal.stage(envelope, {"event":"completion_proposed", "eventId":"event", "completionId":"completion", "attemptId":"attempt", "assignmentId":"work", "revision":1})
                journal.accepted(envelope)
                async def command(*args):
                    if args[0] == "EVAL": return [1, "2-0"]
                    if ":completion-ack:" in str(args[1]): return json.dumps({"decision":"accepted", "teamId":cfg.team_id, "memberId":"worker", "rootTaskId":"root", "completionId":"completion", "attemptId":"attempt"})
                    return None
                await journal.flush(types.SimpleNamespace(command=command), envelope, True)
                self.assertTrue(journal.read(envelope)["compact"])
                self.assertNotIn("contextHandoff", journal.read(envelope))
                self.assertIsNone(journal.context(envelope, "submitting"))
                self.assertTrue(journal.accepted(envelope), "exact accepted completion permits transport commit after compaction")
                with mock.patch.object(adapter, "load_settings", return_value=cfg):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                instance._redis = types.SimpleNamespace(command=mock.AsyncMock(return_value=None))
                instance.handle_message = mock.AsyncMock()
                await instance._handle_redis_message({**envelope, "redisId":"1-0"})
                self.assertTrue(any(call.args[0] == "XACK" for call in instance._redis.command.call_args_list))
                instance.handle_message.assert_not_awaited()
        asyncio.run(run())

    def test_stale_scope_observer_cannot_resolve_a_native_submission(self):
        with tempfile.TemporaryDirectory() as root:
            cfg = adapter.RedisTeamSettings(enabled=True, redis_url=os.environ["HERMES_REDIS_TEST_URL"], team_id="stale-ignore", member_id="worker", shared_dir=root)
            envelope = {"taskId":"root", "messageId":"notice"}
            journal = adapter.TerminalJournal(cfg)
            journal.started(envelope)
            journal.context(envelope, "queued", owned=True, handoff={"activeMessageId":"original"})
            self.assertTrue(journal.context(envelope, "submitting"))
            self.assertIsNone(journal.context(envelope, "ignored", {"reason":"execution_changed_or_delivered"}))
            self.assertIsNone(journal.accepted(envelope))
            self.assertEqual(journal.read(envelope)["state"], "started")

    def scenario(self, fault, change=None):
        async def run():
            with tempfile.TemporaryDirectory() as root:
                cfg = adapter.RedisTeamSettings(enabled=True, auto_run=True, redis_url=os.environ["HERMES_REDIS_TEST_URL"], team_id="handoff-" + uuid.uuid4().hex, member_id="worker", role="worker", shared_dir=root)
                redis = adapter.AsyncRedisClient(cfg.redis_url)
                await redis.connect()
                self.assertEqual(await redis.command("GET", "cm:test:runtime-durable:isolated"), "runtime-durable-20261118")
                active = {"taskId":"root", "rootTaskId":"root", "assignmentId":"work", "revision":1, "messageId":"original", "from":"chief"}
                adapter._persist_active_envelope(cfg, active)
                envelope = {**active, "teamId":cfg.team_id, "to":"worker", "messageId":"notice", "idempotencyKey":"notice", "text":"Please report actual progress", "requiresCompletion":False, "intent":"question", "businessDeliveryKind":"context", "businessMutation":False}
                await redis.command("XGROUP", "CREATE", adapter.inbox_key(cfg), cfg.consumer_group, "0", "MKSTREAM")
                await redis.command("XADD", adapter.inbox_key(cfg), "*", "payload", json.dumps(envelope))
                raw = await redis.command("XREADGROUP", "GROUP", cfg.consumer_group, cfg.member_id, "STREAMS", adapter.inbox_key(cfg), ">")
                envelope = adapter._parse_stream_response(raw)[0]
                native = types.SimpleNamespace(steer=mock.Mock(return_value=True))
                normal = mock.AsyncMock()
                def instance():
                    with mock.patch.object(adapter, "load_settings", return_value=cfg):
                        inst = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                    inst._redis = redis
                    inst.set_clawmanager_team_agent_reader(lambda source: native)
                    inst.handle_message = normal
                    return inst
                inst = instance()
                path = adapter._context_notice_path(cfg)
                original = adapter._atomic_write_json
                failures = []
                def write(file, value):
                    if file == path and not failures:
                        failures.append(True)
                        if fault in {"queue_file_fsync", "queue_directory_fsync"}:
                            sync = os.fsync
                            calls = 0
                            def fail_sync(fd):
                                nonlocal calls
                                calls += 1
                                sync(fd)
                                if calls == (1 if fault == "queue_file_fsync" else 2):
                                    raise OSError(5, "injected queue fsync response failure")
                            with mock.patch.object(os, "fsync", fail_sync):
                                return original(file, value)
                        if fault == "after_queue": original(file, value)
                        raise OSError(28, "injected context queue commit failure")
                    return original(file, value)
                if fault == "capacity":
                    original(path, {str(i):{"state":"submitting", "reportedState":"submitting", "envelope":{"messageId":str(i), "taskId":"root"}} for i in range(64)})
                    await inst._handle_redis_message(envelope)
                elif fault in {"before_queue", "after_queue", "queue_file_fsync", "queue_directory_fsync"}:
                    with mock.patch.object(adapter, "_atomic_write_json", side_effect=write):
                        await inst._handle_redis_message(envelope)
                    self.assertTrue(failures)
                elif fault == "native_uncertain":
                    def steer(text):
                        raise OSError("native response lost after possible acceptance")
                    native.steer.side_effect = steer
                    await inst._handle_redis_message(envelope)
                    native.steer.side_effect = None
                elif fault == "journal_after_native":
                    save = adapter.TerminalJournal._save
                    def fail_submitted(journal, row):
                        if row.get("contextDelivery", {}).get("state") == "submitted":
                            raise OSError(28, "native accepted but submitted fsync failed")
                        return save(journal, row)
                    with mock.patch.object(adapter.TerminalJournal, "_save", fail_submitted):
                        await inst._handle_redis_message(envelope)
                elif fault == "legacy":
                    j = adapter.TerminalJournal(cfg)
                    j.started(envelope)
                    j.context(envelope, "queued", owned=True)
                elif fault == "declined":
                    native.steer.side_effect = [False, True]
                    await inst._handle_redis_message(envelope)
                else:
                    # Leave a durably queued handoff; native agent is still initializing.
                    inst.set_clawmanager_team_agent_reader(lambda source: object())
                    await inst._handle_redis_message(envelope)
                if fault != "capacity":
                    path.unlink(missing_ok=True)  # Auxiliary state may disappear independently.
                if change:
                    if change == "generation": cfg = dataclasses.replace(cfg, generation=cfg.generation + 1)
                    elif change == "terminal": active["terminal"] = True
                    else: active[change] = {"revision":2, "messageId":"replacement", "assignmentId":"other", "rootTaskId":"other"}[change]
                    adapter._persist_active_envelope(cfg, active)
                await redis.command("DEL", adapter._processed_message_key(cfg, "notice"))
                restarted = instance()
                await restarted._handle_redis_message(envelope)
                await asyncio.gather(restarted._flush_context_notices(), instance()._flush_context_notices())
                await restarted._flush_context_notices()
                row = adapter.TerminalJournal(cfg).read(envelope)
                pending = (await redis.command("XPENDING", adapter.inbox_key(cfg), cfg.consumer_group))[0]
                self.assertEqual(normal.await_count, 0)
                self.assertFalse(adapter.TerminalJournal(cfg).started(envelope))
                events = await redis.command("XRANGE", adapter.events_key(cfg), "-", "+")
                event_names = [json.loads(fields[fields.index("payload") + 1]).get("event") for _, fields in events]
                self.assertFalse(set(event_names) & {"dlq", "task_failed", "assignment_attempt_failed"})
                if fault in {"native_uncertain", "journal_after_native"}:
                    self.assertEqual(native.steer.call_count, 1)
                    self.assertEqual(row["state"], "started")
                    self.assertEqual(row["contextDelivery"]["state"], "submitting")
                    self.assertEqual(pending, 1)
                elif fault in {"capacity", "legacy"}:
                    self.assertEqual(native.steer.call_count, 0)
                    self.assertEqual(pending, 1)
                elif change:
                    self.assertEqual(native.steer.call_count, 0)
                    self.assertEqual(row["contextDelivery"]["state"], "ignored")
                else:
                    self.assertEqual(native.steer.call_count, 2 if fault == "declined" else 1, "durable queued handoff must recover without the auxiliary file")
                    self.assertEqual(row["contextDelivery"]["state"], "submitted")
                    self.assertEqual(pending, 0)
                print(json.dumps({"fault":fault, "change":change, "nativeCalls":native.steer.call_count, "pending":pending, "state":row["state"], "context":row.get("contextDelivery", {}).get("state")}))
                redis.close()
        asyncio.run(run())

    def test_queue_commit_failures_recover(self):
        for fault in ("before_queue", "after_queue", "queue_file_fsync", "queue_directory_fsync", "ack_loss", "declined"):
            with self.subTest(fault=fault): self.scenario(fault)

    def test_uncertain_native_claim_is_never_replayed_or_acked(self):
        for fault in ("native_uncertain", "journal_after_native", "legacy", "capacity"):
            with self.subTest(fault=fault): self.scenario(fault)

    def test_recovery_never_rebinds_the_original_execution(self):
        for change in ("generation", "revision", "messageId", "assignmentId", "rootTaskId", "terminal"):
            with self.subTest(change=change): self.scenario("before_queue", change)
