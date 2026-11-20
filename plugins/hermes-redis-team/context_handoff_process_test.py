"""Crash actual handlers, then recover in concurrent fresh processes on isolated Redis."""
import asyncio
import errno
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import types
import uuid
from unittest import mock
from test_adapter_contract import adapter


async def worker(phase, root, fault):
    meta = root / "fixture.json"
    if phase == "prepare":
        meta.write_text(json.dumps({"team":"handoff-process-" + uuid.uuid4().hex}))
    fixture = json.loads(meta.read_text())
    cfg = adapter.RedisTeamSettings(enabled=True, auto_run=True, redis_url=os.environ["HERMES_REDIS_TEST_URL"], team_id=fixture["team"], member_id="worker", role="worker", shared_dir=str(root))
    redis = adapter.AsyncRedisClient(cfg.redis_url)
    await redis.connect()
    assert await redis.command("GET", "cm:test:runtime-durable:isolated") == "runtime-durable-20261118"
    if phase == "prepare":
        active = {"taskId":"root", "rootTaskId":"root", "assignmentId":"work", "revision":1, "messageId":"original", "from":"chief"}
        adapter._persist_active_envelope(cfg, active)
        e = {**active, "teamId":cfg.team_id, "to":"worker", "messageId":"notice", "idempotencyKey":"notice", "text":"Report progress", "requiresCompletion":False, "intent":"question", "businessDeliveryKind":"context", "businessMutation":False}
        await redis.command("XGROUP", "CREATE", adapter.inbox_key(cfg), cfg.consumer_group, "0", "MKSTREAM")
        await redis.command("XADD", adapter.inbox_key(cfg), "*", "payload", json.dumps(e))
        raw = await redis.command("XREADGROUP", "GROUP", cfg.consumer_group, cfg.member_id, "STREAMS", adapter.inbox_key(cfg), ">")
        fixture["envelope"] = adapter._parse_stream_response(raw)[0]
        meta.write_text(json.dumps(fixture))
    e = fixture["envelope"]
    journal = adapter.TerminalJournal(cfg)
    def steer(text):
        with (root / "native.jsonl").open("a") as handle:
            handle.write(json.dumps(text) + "\n")
            handle.flush()
            os.fsync(handle.fileno())
        if phase == "prepare" and fault == "native_effect": os._exit(72)
        return True
    with mock.patch.object(adapter, "load_settings", return_value=cfg):
        instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
    instance._redis = redis
    instance.set_clawmanager_team_agent_reader(lambda source: types.SimpleNamespace(steer=steer))
    async def normal(*args, **kwargs):
        (root / "normal-dispatch").write_text("unexpected serial execution")
    instance.handle_message = normal
    original_write = adapter._atomic_write_json
    original_save = adapter.TerminalJournal._save
    original_command = redis.command
    def write(file, value):
        if phase == "prepare" and file == adapter._context_notice_path(cfg):
            if fault == "queue_before": os._exit(72)
            if fault == "queue_after":
                original_write(file, value)
                os._exit(72)
        return original_write(file, value)
    def save(owner, row):
        state = (row.get("contextDelivery") or {}).get("state")
        if phase == "prepare" and state == "queued" and fault == "journal_before": os._exit(72)
        if phase == "prepare" and state == "submitting" and fault == "claim_before": os._exit(72)
        original_save(owner, row)
        if phase == "prepare" and state == "queued" and fault == "real_enospc":
            assert "cm-runtime-enospc" in str(root)
            with (root / "filler").open("wb", buffering=0) as handle:
                try:
                    while True: handle.write(b"x" * (1024 * 1024))
                except OSError as error:
                    assert error.errno == errno.ENOSPC
                os.fsync(handle.fileno())
            assert os.statvfs(root).f_bavail == 0
        if phase == "prepare" and ((state == "queued" and fault == "journal_after") or (state == "submitting" and fault == "claim_after") or (state == "submitted" and fault == "receipt_after")):
            os._exit(72)
    async def command(*args):
        if phase == "prepare" and fault == "ack_loss" and args[0] == "XACK": os._exit(72)
        return await original_command(*args)
    if phase in {"recover", "proof"}:
        await redis.command("DEL", adapter._processed_message_key(cfg, "notice"))
        if phase == "proof":
            request = json.loads((root / "native.jsonl").read_text().splitlines()[0])
            adapter._record_context_request(cfg, {"request_messages":[{"content":request}], "session_id":"native-original"})
    if phase != "verify":
        with mock.patch.object(adapter, "_atomic_write_json", write), mock.patch.object(adapter.TerminalJournal, "_save", save), mock.patch.object(redis, "command", command):
            await instance._handle_redis_message(e)
            if phase == "prepare" and fault == "real_enospc":
                assert journal.read(e)["contextDelivery"]["state"] == "queued"
                assert not (root / "native.jsonl").exists()
                assert (await redis.command("XPENDING", adapter.inbox_key(cfg), cfg.consumer_group))[0] == 1
                (root / "filler").unlink()
                os._exit(72)
            await instance._flush_context_notices()
    else:
        row = journal.read(e)
        calls = len((root / "native.jsonl").read_text().splitlines()) if (root / "native.jsonl").exists() else 0
        pending = (await redis.command("XPENDING", adapter.inbox_key(cfg), cfg.consumer_group))[0]
        uncertain = fault in {"journal_before", "claim_after", "native_effect"}
        assert not (root / "normal-dispatch").exists()
        assert calls == (0 if fault in {"journal_before", "claim_after"} else 1), (fault, calls)
        assert pending == int(uncertain), (fault, pending)
        assert row["state"] == ("started" if uncertain else "accepted"), row
        assert not journal.started(e)
        print(json.dumps({"fault":fault, "nativeCalls":calls, "pending":pending, "state":row["state"], "context":row.get("contextDelivery", {}).get("state")}))
    redis.close()


if __name__ == "__main__":
    if len(sys.argv) > 1:
        asyncio.run(worker(sys.argv[1], Path(sys.argv[2]), sys.argv[3]))
    else:
        faults = ("real_enospc",) if os.getenv("RUNTIME_ENOSPC_ROOT") else ("queue_before", "queue_after", "journal_before", "journal_after", "claim_before", "claim_after", "native_effect", "receipt_after", "ack_loss")
        for fault in faults:
            with tempfile.TemporaryDirectory(dir=os.getenv("RUNTIME_ENOSPC_ROOT")) as directory:
                command = [sys.executable, __file__]
                assert subprocess.run([*command, "prepare", directory, fault]).returncode == 72
                # Real process death discards all memory. Also lose the optional mirror.
                for file in Path(directory).glob(".hermes-redis-team/context-*.json"): file.unlink()
                children = [subprocess.Popen([*command, "recover", directory, fault]) for _ in range(2)]
                assert all(child.wait() == 0 for child in children)
                subprocess.run([*command, "verify", directory, fault], check=True)
                if fault == "native_effect":
                    subprocess.run([*command, "proof", directory, fault], check=True)
                    subprocess.run([*command, "verify", directory, "proof_resolved"], check=True)
