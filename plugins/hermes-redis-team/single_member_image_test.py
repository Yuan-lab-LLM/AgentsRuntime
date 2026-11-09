"""Actual installed Hermes adapter, real Redis and ClawManager; no model calls."""
import asyncio
import importlib.util
import json
import os
import sys
from pathlib import Path

spec=importlib.util.spec_from_file_location("single_member_installed", "/opt/hermes-agent/plugins/platforms/redis_team/adapter.py")
adapter=importlib.util.module_from_spec(spec); sys.modules[spec.name]=adapter; spec.loader.exec_module(adapter)

async def main():
    settings=adapter.load_settings(None)
    assert settings.valid, "actual settings invalid"
    redis=adapter.AsyncRedisClient(settings.redis_url); await redis.connect()
    try:
        rows=await redis.command("XREVRANGE",adapter.inbox_key(settings,settings.member_id),"+","-","COUNT","1")
        fields=rows[0][1]; envelope=adapter.normalize_envelope(json.loads(fields[fields.index("payload")+1]))
        assert adapter._is_single_member_envelope(settings,envelope)
        assert "verified sources" in envelope["text"] and "Collected source A and B" in envelope["followupContext"]["text"]
        adapter._persist_active_envelope(settings,envelope)
        before=int(await redis.command("XLEN",adapter.inbox_key(settings,"leader")))
        old=Path(settings.shared_dir)/envelope["followupContext"]["artifactRefs"][0].removeprefix("/team/")
        original=old.read_text(encoding="utf-8")
        output=f'/team/artifacts/{envelope["rootTaskId"]}/members/{settings.member_id}/single-member/revised.md'
        blocked=json.loads(await adapter._tool_team_send({"to":"leader","text":"Summarize"}))
        assert blocked["code"]=="single_member_no_delegation"
        read=json.loads(await adapter._tool_team_artifact_read({"path":envelope["followupContext"]["artifactRefs"][0]}))
        assert "original citations" in json.dumps(read)
        write=json.loads(await adapter._tool_team_artifact_write({"path":output,"content":"# 修改后报告\noriginal citations\n"}))
        assert write.get("ok") is not False,write
        receipt=await adapter._propose_completion(settings,envelope,status="succeeded",summary="报告修改完成",result_markdown="保留原始引用的新报告",artifact_refs=[output],explicit=True)
        assert receipt["decision"]=="accepted",receipt
        assert adapter._load_active_envelope(settings)["terminal"]
        assert old.read_text(encoding="utf-8")==original
        assert int(await redis.command("XLEN",adapter.inbox_key(settings,"leader")))==before
        print(json.dumps({"runtime":"hermes","rootTaskId":envelope["rootTaskId"],"decision":receipt["decision"],"output":output,"installedAdapter":True,"modelCalls":0}))
    finally: redis.close()

asyncio.run(main())
