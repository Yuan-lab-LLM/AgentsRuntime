"""Exercise real pinned Hermes steering without a model or external side effect."""
import asyncio
import importlib.util
import json
import sys
import tempfile
import threading
from pathlib import Path
from types import SimpleNamespace

from gateway.config import PlatformConfig
from gateway.run import GatewayRunner
from run_agent import AIAgent
from agent.agent_runtime_helpers import apply_pending_steer_to_tool_results

spec = importlib.util.spec_from_file_location("cm_context_smoke", "/opt/hermes-agent/plugins/platforms/redis_team/adapter.py")
adapter = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = adapter
spec.loader.exec_module(adapter)


async def main():
    with tempfile.TemporaryDirectory() as directory:
        settings = adapter.RedisTeamSettings(enabled=True, redis_url="redis://unused.invalid", team_id="42", member_id="worker", role="worker", shared_dir=directory)
        active = {"taskId":"team-42-task-7","assignmentId":"work","revision":1,"messageId":"original","from":"chief"}
        adapter._persist_active_envelope(settings, active)
        native = object.__new__(AIAgent)
        native._pending_steer_lock = threading.Lock()
        native._pending_steer = None
        gateway = object.__new__(GatewayRunner)
        gateway._session_key_for_source = lambda source: "owned"
        gateway._peek_session_state = lambda key: SimpleNamespace(turn=SimpleNamespace(agent=native))
        bridge = adapter.RedisTeamAdapter(PlatformConfig())
        bridge.settings = settings
        bridge.set_clawmanager_team_agent_reader(gateway._clawmanager_team_active_agent)
        notice = {"taskId":active["taskId"],"messageId":"notice","from":"chief","text":"Keep the existing work and report progress","requiresCompletion":False}
        await bridge._dispatch_envelope(notice, context_only=True)
        await bridge._dispatch_envelope(notice, context_only=True)
        messages = [{"role":"tool","content":"Actual tool result"}]
        apply_pending_steer_to_tool_results(native,messages,1)
        # 0.22.5 persists native steering as a new user row instead of
        # rewriting the previous tool result. Accept either native layout.
        assert sum(str(row.get("content", "")).count("Keep the existing work") for row in messages) == 1
        adapter._record_context_request(settings, {"request_messages":messages,"api_request_id":"request-1","session_id":"session-1"})
        value = adapter._read_json(adapter._context_notice_path(settings))["notice"]
        assert value["state"] == "request_included" and value["apiRequestId"] == "request-1"
        assert adapter._load_active_envelope(settings) == active
        print(json.dumps({"native_steer":True,"request_included":True,"duplicate_injections":0,"original_assignment_retained":True}))


asyncio.run(main())
