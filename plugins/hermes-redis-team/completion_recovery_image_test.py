"""Installed Hermes dispatch/send/end-turn -> actual App recovery -> tool ACK.

Only the model handler is replaced by a deterministic answer; no model calls.
"""
import asyncio
import importlib.util
import json
import os
import sys

spec = importlib.util.spec_from_file_location("installed_recovery", "/opt/hermes-agent/plugins/platforms/redis_team/adapter.py")
adapter = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = adapter
spec.loader.exec_module(adapter)

async def main():
    instance = adapter.RedisTeamAdapter(adapter.PlatformConfig())
    settings = instance.settings
    redis = adapter.AsyncRedisClient(settings.redis_url)
    await redis.connect()
    instance._redis = redis
    answer = "本轮完整答案：已核对十二个来源，沿用原始引用，无需额外文件。"
    receipt = None
    turns = 0
    async def handle(event):
        nonlocal receipt, turns
        if "[ROOT_TASK_RECOVERY]" in event.text:
            assert answer in event.text, "recovery lost the exact previous answer"
            assert "text-only answer" in event.text
            receipt = json.loads(await adapter._tool_team_complete_task({"status":"succeeded", "summary":"资讯来源说明", "resultMarkdown":answer}))
        else:
            turns += 1
            await instance.send(event.source.chat_id, answer)
        await instance.on_processing_complete(event, adapter.ProcessingOutcome.SUCCESS)
    instance.handle_message = handle
    def decode(row):
        fields = row[1]
        return json.loads(fields[fields.index("payload")+1])
    try:
        inbox = adapter.inbox_key(settings)
        rows = await redis.command("XREVRANGE",inbox,"+","-","COUNT","100")
        initial = next(decode(r) for r in rows if decode(r).get("rootTaskId")==os.environ["TEAM_RECOVERY_ROOT"] and decode(r).get("intent")=="run_task")
        await instance._handle_redis_message(initial)
        for _ in range(300):
            rows = await redis.command("XREVRANGE",inbox,"+","-","COUNT","5")
            reminder = next((decode(r) for r in rows if decode(r).get("intent")=="root_assignment_recovery" and decode(r).get("rootTaskId")==initial["rootTaskId"]),None)
            if reminder:
                await instance._handle_redis_message(reminder)
                break
            await asyncio.sleep(.1)
        assert receipt and receipt.get("decision")=="accepted", receipt
        assert turns==1, turns
        assert adapter._load_active_envelope(settings)["terminal"]
        print(json.dumps({"runtime":"hermes","root":initial["rootTaskId"],"immediateRecovery":True,"decision":"accepted","modelCalls":0}))
    finally:
        redis.close()

asyncio.run(main())
