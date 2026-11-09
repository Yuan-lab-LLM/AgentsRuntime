#!/usr/bin/env python3
"""Real AIAgent/live-registry fixture, run only inside a disposable Lite image.

No user state or external model is used. A local OpenAI-compatible stub holds
one actual run_conversation request while real resume helpers reject a legacy
native agent. The test releases that request and waits for normal completion.
"""
import contextlib
import copy
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import tempfile
import threading
import time


def check():
    entered = threading.Event()
    release = threading.Event()
    requests = []
    prompt = "Finish the controlled fixture normally."

    class Model(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_GET(self):
            if self.path != "/v1/models":
                self.send_error(404)
                return
            body = json.dumps({"object": "list", "data": [{"id": "fixture-model", "object": "model"}]}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_POST(self):
            payload = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))))
            if (self.path != "/v1/chat/completions" or not payload.get("tools")
                    or prompt not in json.dumps(payload.get("messages", []))):
                self.send_error(404)
                return
            requests.append(payload)
            entered.set()
            assert release.wait(60), "local fixture release timed out"
            self.send_response(200)
            if payload.get("stream"):
                self.send_header("Content-Type", "text/event-stream")
                self.end_headers()
                for delta, finish in [({"role": "assistant", "content": "fixture completed"}, None), ({}, "stop")]:
                    chunk = {"id": "fixture", "object": "chat.completion.chunk", "created": 1, "model": "fixture-model", "choices": [{"index": 0, "delta": delta, "finish_reason": finish}]}
                    self.wfile.write(("data: " + json.dumps(chunk) + "\n\n").encode())
                self.wfile.write(b"data: [DONE]\n\n")
            else:
                body = json.dumps({"id": "fixture", "object": "chat.completion", "created": 1, "model": "fixture-model", "choices": [{"index": 0, "message": {"role": "assistant", "content": "fixture completed"}, "finish_reason": "stop"}], "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}}).encode()
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

    model = ThreadingHTTPServer(("127.0.0.1", 0), Model)
    model.daemon_threads = True
    service = threading.Thread(target=model.serve_forever, daemon=True)
    service.start()
    worker = None
    temporary_state = tempfile.TemporaryDirectory(prefix="hermes-non-native-")
    try:
        with contextlib.nullcontext(temporary_state.name) as temporary:
            state = Path(temporary) / "home/.hermes"
            state.mkdir(parents=True)
            endpoint = "http://127.0.0.1:%d/v1" % model.server_port
            os.environ.update({
                "HOME": str(state.parent), "HERMES_HOME": str(state),
                "CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED": "true",
                "CLAWMANAGER_CONTROL_UI_ORIGIN": "https://fixture.internal",
                "CLAWMANAGER_TRUSTED_PROXY_CIDRS": "127.0.0.0/8",
                "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD": "local-auth-fixture-only",
                "PORT": "20001",
                "HERMES_TUI_TOOLSETS": "terminal,clarify,desktop_ui,project,computer_use",
                "OPENAI_BASE_URL": endpoint, "OPENAI_API_KEY": "local-fixture-only",
                "HERMES_IGNORE_RULES": "true", "HERMES_QUIET": "1",
                "HERMES_TUI_WS_ORPHAN_REAP_GRACE_S": "0",
            })
            import hermes_yaml as yaml
            config = {
                "model": {"provider": "clawmanager", "default": "fixture-model", "base_url": endpoint},
                "providers": {"clawmanager": {"name": "clawmanager", "base_url": endpoint, "key_env": "OPENAI_API_KEY", "enabled": True, "api_mode": "chat_completions"}},
                "agent": {"max_turns": 2},
                "memory": {"memory_enabled": False, "user_profile_enabled": False},
                "terminal": {"backend": "local", "cwd": temporary},
            }
            (state / "config.yaml").write_text(yaml.safe_dump(config), encoding="utf-8")
            (state / ".env").write_text("OPENAI_API_KEY=workspace-override\nCLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED=false\n"
                                        "CLAWMANAGER_CONTROL_UI_ORIGIN=https://wrong.example\n"
                                        "CLAWMANAGER_TRUSTED_PROXY_CIDRS=0.0.0.0/0\n"
                                        "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD=workspace-override\n"
                                        "HOME=/wrong\nHERMES_HOME=/wrong\nPORT=1\nFIXTURE_USER_OPTION=retained\n", encoding="utf-8")
            os.chdir(temporary)

            # Reproduce actual upstream dotenv precedence BEFORE policy import.
            # A user workspace cannot disable the immutable Lite artifact.
            from hermes_cli.env_loader import load_hermes_dotenv
            load_hermes_dotenv(hermes_home=state, load_external_secrets=False)
            assert os.environ["CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED"] == "true", "workspace changed deployment flag"
            assert os.environ["CLAWMANAGER_CONTROL_UI_ORIGIN"] == "https://fixture.internal", "workspace changed internal origin"
            assert os.environ["CLAWMANAGER_TRUSTED_PROXY_CIDRS"] == "127.0.0.0/8", "workspace changed trusted proxies"
            assert os.environ["HERMES_DASHBOARD_BASIC_AUTH_PASSWORD"] == "local-auth-fixture-only", "workspace changed auth"
            assert os.environ["OPENAI_API_KEY"] == "local-fixture-only", "workspace changed managed model credential"
            assert os.environ["HOME"] == str(state.parent) and os.environ["HERMES_HOME"] == str(state), "workspace changed instance paths"
            assert os.environ["PORT"] == "20001" and os.environ["FIXTURE_USER_OPTION"] == "retained", "dotenv filtering lost permitted user configuration"
            from run_agent import AIAgent
            from hermes_cli import lite_non_native as policy
            import model_tools
            from tui_gateway import server
            assert policy.ENABLED, "workspace dotenv disabled Lite execution policy"

            # First inspect a genuinely constructed gateway agent, including
            # the explicit desktop source and native toolsets supplied above.
            safe = server._make_agent("safe-fixture", "safe-fixture", platform_override="desktop")
            assert isinstance(safe, AIAgent), "fixture did not construct the real agent"
            safe_names = {tool["function"]["name"] for tool in safe.tools}
            assert not safe_names & policy.NATIVE_TOOLS, "native schemas registered on fresh Lite agent"
            assert not safe.valid_tool_names & policy.NATIVE_TOOLS, "native dispatcher names registered"
            assert {"terminal", "clarify"} <= safe_names, "supported server tools missing"
            policy.require_compatible_session({"agent": safe, "source": "desktop"})
            from check_lite_tool_executor import check_native_executor
            execution_evidence = check_native_executor(safe)

            # Reconstruct an old warm agent's REAL schema and dispatch tables
            # using the locked registry entry, not source metadata alone.
            native = server._make_agent("legacy-runtime", "legacy-history", platform_override="desktop")
            entry = model_tools.registry.get_entry("read_terminal")
            assert entry is not None and callable(entry.handler), "native upstream handler not present in fixture"
            definition = {"type": "function", "function": copy.deepcopy(entry.schema)}
            assert definition["function"]["name"] == "read_terminal", "unexpected native schema"
            native.tools.append(definition)
            native.valid_tool_names.add("read_terminal")
            native.enabled_toolsets.append("desktop_ui")
            del native._clawmanager_non_native_seal
            db = server._get_db()
            db.create_session("legacy-history", source="desktop", model="fixture-model")
            record = server._deferred_session_record("legacy-history", cols=80, cwd=temporary, history=[], lease=None, source="desktop")
            record["agent"] = native
            record["agent_ready"].set()
            record["running"] = True
            original_transport = record["transport"]
            with server._sessions_lock:
                server._sessions["legacy-runtime"] = record

            outcome = []
            def run_existing_work():
                try:
                    outcome.append(native.run_conversation(prompt, task_id="legacy-history"))
                except BaseException as error:
                    outcome.append(type(error).__name__)
            worker = threading.Thread(target=run_existing_work)
            worker.start()
            assert entered.wait(45), "real legacy agent did not reach local model stub"
            assert worker.is_alive(), "existing agent work was not active"

            for params in ({"session_id": "legacy-runtime", "source": "web"},
                           {"session_id": "legacy-history", "source": "web", "omit_messages": True}):
                response = server.handle_request({"jsonrpc": "2.0", "id": "warm", "method": "session.resume", "params": params})
                assert response.get("error", {}).get("message") == "incompatible_lite_live_session", "unsafe warm resume was accepted"

            failures = []
            def concurrent_claim(index):
                candidate = server._deferred_session_record("legacy-history", cols=80, cwd=temporary, history=[], lease=None, source="web")
                try:
                    server._claim_or_reuse_live("candidate-" + str(index), "legacy-history", candidate, None)
                except policy.NonNativeSessionError:
                    failures.append(index)
            claimers = [threading.Thread(target=concurrent_claim, args=(index,)) for index in range(2)]
            for thread in claimers:
                thread.start()
            for thread in claimers:
                thread.join(5)
                assert not thread.is_alive(), "concurrent claim did not terminate"
            assert len(failures) == 2, "concurrent native winner was reused"
            try:
                server._live_session_payload("legacy-runtime", record, transport=object(), omit_messages=True)
            except policy.NonNativeSessionError:
                pass
            else:
                raise AssertionError("unsafe reattach accepted")

            started = time.monotonic()
            result = model_tools.handle_function_call("read_terminal", {"ignored": "no-private-output"})
            assert json.loads(result) == {"error": "unsupported_native_desktop_tool"}, "native execution reached renderer handler"
            assert time.monotonic() - started < 1, "native rejection waited for a renderer"
            assert worker.is_alive() and not native._interrupt_requested, "resume guard interrupted existing work"
            assert server._sessions.get("legacy-runtime") is record and record["transport"] is original_transport, "resume guard replaced or reattached existing work"
            assert {tool["function"]["name"] for tool in native.tools} >= {"read_terminal"}, "guard hid unsafe state by rewriting old agent"
            assert db.get_session("legacy-history")["source"] == "desktop", "guard rewrote history source"

            release.set()
            worker.join(30)
            assert not worker.is_alive() and len(outcome) == 1 and isinstance(outcome[0], dict), "existing work did not finish normally"
            assert len(requests) == 1, "fixture unexpectedly repeated model prompt (%d)" % len(requests)
            assert "fixture completed" in json.dumps(outcome[0]), "local stub result was lost"
            assert db.get_session("legacy-history") is not None, "history lost after rejected resume"
            with server._sessions_lock:
                server._sessions.pop("legacy-runtime", None)
            return {"fresh_agent_native_tools": 0, "supported_tools": ["terminal", "clarify"],
                    "legacy_agent_class": type(native).__name__, "warm_rejections": 2,
                    "concurrent_winner_rejections": 2, "reattach_rejected": True,
                    "native_dispatch_rejected": True, "existing_work_completed": True,
                    "workspace_dotenv_cannot_disable_policy": True,
                    "workspace_dotenv_preserves_deployment": True,
                    "execution_boundary": execution_evidence,
                    "local_model_requests": len(requests)}
    finally:
        # Release only this fixture's held stub request, never interrupt work.
        release.set()
        if worker is not None:
            worker.join(30)
        model.shutdown()
        model.server_close()
        temporary_state.cleanup()


def main():
    with open(os.devnull, "w") as discard, contextlib.redirect_stdout(discard), contextlib.redirect_stderr(discard):
        result = check()
    print(json.dumps({"status": "passed", "non_native": result}), flush=True)


if __name__ == "__main__":
    main()
