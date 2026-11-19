#!/usr/bin/env python3
"""Exercise the real Lite /api/ws with a deterministic loopback model server.

Run inside the candidate with --network none. Only disposable instance data is
written, no paid/external model is used. This is Runtime evidence, not CM browser
or BFF acceptance. The production gateway/agent/tool implementations are used.
"""
import argparse
import asyncio
import hashlib
import http.cookiejar
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request

from websockets.asyncio.client import connect
from websockets.exceptions import InvalidStatus

ORIGIN = "http://clawmanager-gateway.smoke.svc:9001"
from hermes_cli.lite_non_native import NATIVE_TOOLS as NATIVE_NAMES


def require(condition, message):
    if not condition:
        raise AssertionError(message)


class ModelStub:
    def __init__(self, workspace, api_key, tool_reply=None):
        self.workspace = workspace
        self.calls = []
        self.tools = set()
        self.responses = {}
        self.unsupported_paths = set()
        self.lock = threading.Lock()
        self.slow_started = threading.Event()
        owner = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_GET(self):
                body = json.dumps({"object": "list", "data": [{"id": "smoke-model", "object": "model", "owned_by": "local-test"}]}).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_POST(self):
                try:
                    require(self.headers.get("Authorization") == "Bearer " + api_key, "instance model credential was not preserved")
                    size = int(self.headers.get("Content-Length", "0"))
                    require(0 < size <= 4 << 20, "model request size")
                    payload = json.loads(self.rfile.read(size))
                    if self.path != "/v1/chat/completions":
                        # Provider capability probes may intentionally try another
                        # API. Return an explicit unsupported response, never fake
                        # a successful completion for a protocol we do not serve.
                        owner.unsupported_paths.add(self.path.split("?", 1)[0])
                        body = b'{"error":{"message":"unsupported local stub API","type":"invalid_request_error"}}'
                        self.send_response(404)
                        self.send_header("Content-Type", "application/json")
                        self.send_header("Content-Length", str(len(body)))
                        self.end_headers()
                        self.wfile.write(body)
                        return
                    messages = payload.get("messages", [])
                    user = next((message.get("content", "") for message in reversed(messages) if message.get("role") == "user"), "")
                    user = user if isinstance(user, str) else json.dumps(user)
                    marker = next((tag for tag in ("SMOKE_SLOW", "SMOKE_CLARIFY", "SMOKE_ONCE", "SMOKE_DENY", "SMOKE_NATIVE", "SMOKE_TEXT") if tag in user), "AUXILIARY")
                    names = {tool.get("function", {}).get("name", "") for tool in payload.get("tools", [])}
                    with owner.lock:
                        owner.calls.append(marker)
                        owner.tools.update(names)
                        number = owner.responses.get(marker, 0)
                        owner.responses[marker] = number + 1
                    function = None
                    if number == 0 and marker in {"SMOKE_CLARIFY", "SMOKE_ONCE", "SMOKE_DENY", "SMOKE_NATIVE"}:
                        if marker == "SMOKE_CLARIFY":
                            function = {"name": "clarify", "arguments": json.dumps({"questions": [{"question": "Choose a fixture value", "choices": ["alpha", "beta"]}]})}
                        elif marker == "SMOKE_NATIVE":
                            function = {"name": "read_terminal", "arguments": "{}"}
                        else:
                            # This can only delete this smoke's disposable fixture,
                            # after the real approval workflow resolves to once.
                            target = owner.workspace / ("allow-fixture" if marker == "SMOKE_ONCE" else "deny-fixture")
                            require(target.parent == owner.workspace and target.name.endswith("-fixture"), "unsafe fixture target")
                            function = {"name": "terminal", "arguments": json.dumps({"command": "rm -rf -- " + str(target)})}
                    if tool_reply is not None:
                        function = tool_reply(payload)
                    message = {"role": "assistant", "content": "Local deterministic response " + marker}
                    finish = "stop"
                    if function:
                        message = {"role": "assistant", "content": None, "tool_calls": [{"id": "call_" + marker, "type": "function", "function": function}]}
                        finish = "tool_calls"
                    if payload.get("stream"):
                        self.send_response(200)
                        self.send_header("Content-Type", "text/event-stream")
                        self.send_header("Cache-Control", "no-cache")
                        self.end_headers()

                        def chunk(delta, reason=None):
                            packet = {"id": "chatcmpl-local", "object": "chat.completion.chunk", "created": int(time.time()), "model": "smoke-model", "choices": [{"index": 0, "delta": delta, "finish_reason": reason}]}
                            self.wfile.write(("data: " + json.dumps(packet) + "\n\n").encode())
                            self.wfile.flush()

                        chunk({"role": "assistant"})
                        if function:
                            chunk({"tool_calls": [{"index": 0, **message["tool_calls"][0]}]})
                        elif marker == "SMOKE_SLOW":
                            owner.slow_started.set()
                            for _ in range(45):
                                chunk({"content": "slow "})
                                time.sleep(0.2)
                        else:
                            for word in message["content"].split():
                                chunk({"content": word + " "})
                                time.sleep(0.01)
                        chunk({}, finish)
                        self.wfile.write(b"data: [DONE]\n\n")
                        self.wfile.flush()
                    else:
                        if marker == "SMOKE_SLOW":
                            owner.slow_started.set()
                            time.sleep(9)
                        body = json.dumps({"id": "chatcmpl-local", "object": "chat.completion", "created": int(time.time()), "model": "smoke-model", "choices": [{"index": 0, "message": message, "finish_reason": finish}], "usage": {"prompt_tokens": 12, "completion_tokens": 8, "total_tokens": 20}}).encode()
                        self.send_response(200)
                        self.send_header("Content-Type", "application/json")
                        self.send_header("Content-Length", str(len(body)))
                        self.end_headers()
                        self.wfile.write(body)
                except (BrokenPipeError, ConnectionResetError):
                    pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()

    @property
    def url(self):
        return "http://127.0.0.1:" + str(self.server.server_port) + "/v1"

    def close(self):
        self.server.shutdown()
        self.server.server_close()


class RPC:
    def __init__(self, socket):
        self.socket = socket
        self.pending = {}
        self.events = []
        self.requests = []
        self.sequence = 0
        self.changed = asyncio.Condition()
        self.reader = asyncio.create_task(self.read())

    async def read(self):
        try:
            async for raw in self.socket:
                require(isinstance(raw, str), "RPC emitted a binary application frame")
                for line in raw.splitlines():
                    if not line.strip():
                        continue
                    packet = json.loads(line)
                    require(packet.get("jsonrpc") == "2.0", "invalid JSON-RPC envelope")
                    if "id" in packet and packet["id"] in self.pending:
                        future = self.pending.pop(packet["id"])
                        if not future.done():
                            future.set_result(packet)
                    elif packet.get("method") == "event":
                        async with self.changed:
                            self.events.append(packet.get("params", {}))
                            self.changed.notify_all()
                    elif "id" in packet and packet.get("method") in {"clarify", "approval"}:
                        async with self.changed:
                            self.requests.append(packet)
                            self.changed.notify_all()
        except Exception as error:
            for future in self.pending.values():
                if not future.done():
                    future.set_exception(error)

    async def packet(self, method, params=None):
        self.sequence += 1
        rid = "probe-" + str(self.sequence)
        future = asyncio.get_running_loop().create_future()
        self.pending[rid] = future
        await self.socket.send(json.dumps({"jsonrpc": "2.0", "id": rid, "method": method, "params": params or {}}))
        return await asyncio.wait_for(future, 45)

    async def call(self, method, params=None):
        packet = await self.packet(method, params)
        require("result" in packet and "error" not in packet, "RPC failed: " + method + " code=" + str(packet.get("error", {}).get("code")))
        return packet["result"]

    async def event(self, kind, sid=None, after=0, timeout=60):
        async def find():
            async with self.changed:
                while True:
                    for event in self.events[after:]:
                        if event.get("type") == kind and (sid is None or event.get("session_id") == sid):
                            return event
                    await self.changed.wait()
        return await asyncio.wait_for(find(), timeout)

    async def close(self):
        await self.socket.close()
        self.reader.cancel()
        await asyncio.gather(self.reader, return_exceptions=True)

    async def server_request(self, method, sid, after=0):
        async def find():
            async with self.changed:
                while True:
                    for packet in self.requests[after:]:
                        if packet["method"] == method and packet.get("params", {}).get("session_id") == sid:
                            return packet
                    await self.changed.wait()
        return await asyncio.wait_for(find(), 60)

    async def reply(self, request, result):
        await self.socket.send(json.dumps({"jsonrpc": "2.0", "id": request["id"], "result": result}))


class Fixture:
    def __init__(self, root, instance_id=77):
        self.root = root
        self.uid = 200000 + instance_id
        self.workspace = root / ("instance-" + str(instance_id))
        self.home = self.workspace / "home"
        self.state = self.home / ".hermes"
        self.state.mkdir(parents=True)
        self.secrets = [secrets.token_urlsafe(32), secrets.token_urlsafe(32)]
        self.stub = ModelStub(self.workspace, self.secrets[1])
        self.port = 19940 + instance_id
        self.base = "http://127.0.0.1:" + str(self.port)
        import hermes_yaml as yaml
        config = {"model": {"provider": "clawmanager", "default": "smoke-model"}, "providers": {"clawmanager": {"name": "clawmanager", "base_url": self.stub.url, "key_env": "OPENAI_API_KEY", "enabled": True, "api_mode": "chat_completions"}}, "approvals": {"mode": "manual"}, "agent": {"max_turns": 8}, "memory": {"memory_enabled": False, "user_profile_enabled": False}}
        (self.state / "config.yaml").write_text(yaml.safe_dump(config), encoding="utf-8")
        (self.state / "gateway.json").write_text("{}", encoding="utf-8")
        # Stored user configuration must not overwrite process-owned identity,
        # authentication, model routing, or the fixed Lite execution policy.
        (self.state / ".env").write_text(
            "CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED=false\n"
            "CLAWMANAGER_CONTROL_UI_ORIGIN=https://foreign.invalid\n"
            "CLAWMANAGER_TRUSTED_PROXY_CIDRS=0.0.0.0/0\n"
            "HERMES_DASHBOARD_BASIC_AUTH_USERNAME=foreign\n"
            "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD=wrong-fixture-password\n"
            "OPENAI_API_KEY=wrong-fixture-key\n"
            "HERMES_SMOKE_USER_SETTING=preserved\n", encoding="utf-8")
        for name in ("allow-fixture", "deny-fixture"):
            (self.workspace / name).mkdir()
            (self.workspace / name / "sentinel.txt").write_text("disposable fixture", encoding="utf-8")
        self.root.chmod(0o755)
        for item in [self.workspace, *self.workspace.rglob("*")]:
            os.chown(item, self.uid, self.uid)
            item.chmod(0o700 if item.is_dir() else 0o600)
        self.env = {key: os.environ[key] for key in ("PATH", "LANG", "PYTHONDONTWRITEBYTECODE", "SSL_CERT_FILE") if key in os.environ}
        self.env.update({"HOME": str(self.home), "HERMES_HOME": str(self.state), "CLAWMANAGER_WORKSPACE_PATH": str(self.workspace), "PORT": str(self.port), "CLAWMANAGER_GATEWAY_PORT": str(self.port), "CLAWMANAGER_CONTROL_UI_ORIGIN": ORIGIN, "CLAWMANAGER_TRUSTED_PROXY_CIDRS": "192.0.2.0/24", "CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED": "true", "CLAWMANAGER_HERMES_BACKEND_MODE": "serve", "HERMES_DASHBOARD_BASIC_AUTH_USERNAME": "clawmanager", "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD": self.secrets[0], "CLAWMANAGER_LLM_BASE_URL": self.stub.url, "CLAWMANAGER_LLM_API_KEY": self.secrets[1], "OPENAI_BASE_URL": self.stub.url, "OPENAI_API_KEY": self.secrets[1], "HERMES_TUI_TOOLSETS": "terminal,clarify,desktop_ui,desktop_project,computer_use", "TERMINAL_CWD": str(self.workspace), "TERMINAL_ENV": "local", "PYTHONUNBUFFERED": "1"})
        self.cookies = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(self.cookies))
        self.log = (root / "gateway.log").open("wb")
        self.process = None

    def request(self, path, body=None, origin=ORIGIN):
        headers = {"Accept": "application/json"}
        if origin is not None:
            headers["Origin"] = origin
        if body is not None:
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(self.base + path, data=json.dumps(body).encode() if body is not None else None, headers=headers)
        try:
            with self.opener.open(request, timeout=8) as response:
                return response.status, response.read(4 << 20), response.headers
        except urllib.error.HTTPError as error:
            return error.code, error.read(4 << 20), error.headers

    def start(self):
        self.process = subprocess.Popen(["/usr/local/bin/start-hermes-lite-backend"], env=self.env, cwd=self.workspace, stdout=self.log, stderr=subprocess.STDOUT, start_new_session=True, user=self.uid, group=self.uid, extra_groups=[])
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            require(self.process.poll() is None, "gateway exited at startup")
            try:
                if self.request("/api/health")[0] == 200:
                    break
            except OSError:
                pass
            time.sleep(0.2)
        else:
            raise AssertionError("gateway startup timeout")
        self.cookies.clear()
        status, body, _ = self.request("/auth/password-login", {"provider": "basic", "username": "clawmanager", "password": self.secrets[0], "next": "/chat"})
        require(status == 200 and json.loads(body).get("ok"), "managed password login")
        self.secrets.extend(cookie.value for cookie in self.cookies)

    def ticket(self):
        status, body, _ = self.request("/api/auth/ws-ticket", {})
        require(status == 200, "upstream ticket response")
        result = json.loads(body)
        require(0 < result["ttl_seconds"] <= 30, "upstream ticket lifetime")
        self.secrets.append(result["ticket"])
        return result["ticket"]

    async def connect(self, ticket=None):
        socket = await connect(self.base.replace("http:", "ws:") + "/api/ws", origin=ORIGIN, subprotocols=["hermes-gateway-v1", "hermes-gateway-ticket." + (ticket or self.ticket())], open_timeout=15, max_size=8 << 20, proxy=None)
        require(socket.subprotocol == "hermes-gateway-v1", "secret subprotocol selected or public protocol missing")
        rpc = RPC(socket)
        await rpc.call("client.capabilities", {"server_requests": True})
        return rpc

    def stop(self):
        if self.process and self.process.poll() is None:
            os.killpg(self.process.pid, signal.SIGTERM)
            try:
                self.process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                os.killpg(self.process.pid, signal.SIGKILL)
                self.process.wait(timeout=5)
        self.process = None

    def close(self):
        self.stop()
        self.log.close()
        self.stub.close()


async def exercise(fixture, cases):
    def passed(name):
        cases.append({"name": name, "status": "passed"})
        print("PASS: " + name, flush=True)

    for path in ("/api/status", "/api/model/info", "/api/model/options?explicit_only=1", "/api/sessions"):
        status, body, headers = fixture.request(path)
        require(status == 200 and "application/json" in headers.get("Content-Type", ""), "Core HTTP failed: " + path)
        json.loads(body)
    require(fixture.request("/api/auth/ws-ticket", {}, origin=None)[0] == 403, "missing Origin accepted")
    require(fixture.request("/api/status?token=forbidden")[0] == 403, "credential query accepted")
    passed("core_http_json_origin_and_process_env_boundary")

    peer = Fixture(fixture.root / "peer", instance_id=78)
    try:
        peer.start()
        for source, target in ((fixture, peer), (peer, fixture)):
            cookie = "; ".join(item.name + "=" + item.value for item in source.cookies)
            request = urllib.request.Request(target.base + "/api/auth/me", headers={"Origin": ORIGIN, "Cookie": cookie})
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            try:
                with opener.open(request, timeout=5) as response:
                    status = response.status
            except urllib.error.HTTPError as error:
                status = error.code
            require(status == 401, "cross-instance cookie authenticated")
        code = "import pathlib,sys\ntry: pathlib.Path(sys.argv[1]).read_bytes()\nexcept PermissionError: sys.exit(0)\nsys.exit(1)"
        denied = subprocess.run(["python", "-c", code, str(fixture.state / "config.yaml")], env=peer.env, user=peer.uid, group=peer.uid, extra_groups=[], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
        require(denied.returncode == 0, "peer UID could read another instance config")
        passed("two_real_gateways_cross_instance_cookie_and_uid_rejection")
    finally:
        peer.close()

    expired_ticket = fixture.ticket()
    expiry_deadline = time.monotonic() + 31
    for origin, suffix in (("https://foreign.invalid", ""), (None, ""), (ORIGIN, "?token=forbidden")):
        try:
            socket = await connect(fixture.base.replace("http:", "ws:") + "/api/ws" + suffix, origin=origin, subprotocols=["hermes-gateway-v1", "hermes-gateway-ticket." + fixture.ticket()], open_timeout=5, proxy=None)
        except InvalidStatus as error:
            require(error.response.status_code == 403, "WS boundary returned unexpected rejection")
        else:
            await socket.close()
            raise AssertionError("invalid WS Origin/query accepted")
    passed("real_rpc_websocket_wrong_missing_origin_and_query_rejection")

    ticket = fixture.ticket()
    rpc = await fixture.connect(ticket)
    try:
        ready = await rpc.event("gateway.ready", timeout=15)
        require(await rpc.call("ping") is not None, "ping result missing")
        error = await rpc.packet("unknown.smoke.method")
        require(isinstance(error.get("error", {}).get("code"), int) and isinstance(error["error"].get("message"), str), "JSON-RPC error shape")
        for method, params in (("config.get", {}), ("shell.exec", {"command": "echo should-not-run"}), ("session.create", {"source": "web", "profile": "../other"}), ("session.create", {"source": "web", "cwd": "/"})):
            require("error" in await rpc.packet(method, params), "non-Core RPC accepted: " + method)
        passed("real_rpc_handshake_ping_errors_and_native_rpc_rejection")

        require((await rpc.call("setup.status")).get("provider_configured") is True, "configured provider readiness missing")
        require((await rpc.call("setup.runtime_check")).get("ok") is True, "runtime provider readiness failed")
        catalog = await rpc.call("model.options", {"explicit_only": True})
        choices = [row for row in catalog.get("providers", []) if "smoke-model" in row.get("models", []) and (row.get("is_current") or catalog.get("provider") == row.get("slug") or catalog.get("provider") in row.get("aliases", []))]
        require(choices, "configured model catalogue missing local model")
        provider = choices[0]["slug"]
        created = await rpc.call("session.create", {"source": "web", "cols": 96, "model": "smoke-model", "provider": provider, "reasoning_effort": "high", "fast": False})
        sid, stored = created["session_id"], created["stored_session_id"]
        require(sid and stored and sid != stored, "runtime/durable session identity")
        await rpc.call("session.status", {"session_id": sid})
        offset = len(rpc.events)
        result = await rpc.call("prompt.submit", {"session_id": sid, "text": "SMOKE_TEXT"})
        require(result.get("status") == "streaming", "prompt acknowledgement")
        complete = await rpc.event("message.complete", sid, offset)
        require(complete.get("payload", {}).get("status") != "error", "model turn failed")
        require(any(event.get("type") == "message.delta" and event.get("session_id") == sid for event in rpc.events[offset:]), "streaming delta missing")
        history = await rpc.call("session.history", {"session_id": sid})
        require(history["count"] == 2 and "SMOKE_TEXT" in json.dumps(history), "first history not exactly one user/assistant turn")
        listing = await rpc.call("session.list")
        require(stored in json.dumps(listing), "durable session missing from list")
        status, body, _ = fixture.request("/api/sessions/" + stored + "/messages")
        require(status == 200 and "SMOKE_TEXT" in body.decode(), "HTTP history missing")
        require(not fixture.stub.tools.intersection(NATIVE_NAMES), "actual model tool schema contains native tools")
        require({"terminal", "clarify"}.issubset(fixture.stub.tools), "Core interactive tools unavailable")
        passed("session_create_prompt_streaming_persistence_actual_tools")

        config_before = (fixture.state / "config.yaml").read_bytes()
        require((await rpc.call("session.activate", {"session_id": sid, "cols": 96, "omit_messages": True})).get("session_id") == sid, "live Desktop activation lost session identity")
        require(isinstance(await rpc.call("session.usage", {"session_id": sid}), dict), "session usage missing")
        for key, value in (("reasoning", "low"), ("fast", "normal"), ("model", "smoke-model --provider " + provider + " --session")):
            await rpc.call("config.set", {"session_id": sid, "key": key, "value": value})
            for missing in ({"key": key, "value": value}, {"session_id": "missing-runtime", "key": key, "value": value}):
                require("error" in await rpc.packet("config.set", missing), "missing session entered global configuration")
        # Serve v2 intentionally delegates setting semantics to Hermes rather
        # than the old fixed RPC whitelist. Test supported session settings and
        # the ownership boundary, not the removed whitelist's rejection text.
        require((fixture.state / "config.yaml").read_bytes() == config_before, "session-only model options rewrote global config")
        require("providers" in await rpc.call("model.options", {"session_id": sid, "explicit_only": True}), "live session model catalogue unavailable")
        disposable = await rpc.call("session.create", {"source": "web", "cols": 96})
        disposable_id = disposable["session_id"]
        require((await rpc.call("session.close", {"session_id": disposable_id})).get("closed") is True, "Desktop session close failed")
        require("error" in await rpc.packet("config.set", {"session_id": disposable_id, "key": "reasoning", "value": "high"}), "closed session entered global configuration")
        require((fixture.state / "config.yaml").read_bytes() == config_before, "closed session rewrote global config")
        passed("desktop_activation_usage_model_options_and_session_only_configuration")

        for marker, event_name, method, choice in (("SMOKE_CLARIFY", "clarify.request", "clarify.respond", "alpha"), ("SMOKE_ONCE", "approval.request", "approval.respond", "once"), ("SMOKE_DENY", "approval.request", "approval.respond", "deny")):
            offset = len(rpc.events)
            request_offset = len(rpc.requests)
            await rpc.call("prompt.submit", {"session_id": sid, "text": marker})
            request = await rpc.server_request("clarify" if method == "clarify.respond" else "approval", sid, request_offset)
            payload = request["params"]
            if method == "clarify.respond":
                answer = {"answers": {question["qid"]: choice for question in payload["questions"]}, "outcome": "submitted"}
            else:
                pending = await rpc.call("approval.pending", {"session_id": sid})
                require(any(row.get("request_id") == payload["request_id"] for row in pending.get("approvals", [])), "pending approval replay missing")
                require({"once", "deny"} <= set(payload.get("choices", [])), "approval request omitted explicit allow/deny")
                acknowledged = await rpc.call("approval.received", {"session_id": sid, "request_id": payload["request_id"]})
                require(acknowledged.get("acknowledged") is True, "pending approval acknowledgement failed")
                answer = {"choice": choice}
            await rpc.reply(request, answer)
            await rpc.event("message.complete", sid, offset)
            if marker == "SMOKE_ONCE":
                require(not (fixture.workspace / "allow-fixture").exists(), "once approval did not execute disposable fixture command")
            if marker == "SMOKE_DENY":
                require((fixture.workspace / "deny-fixture/sentinel.txt").exists(), "denied command executed")
            passed(marker.lower() + "_real_tool_roundtrip")

        offset = len(rpc.events)
        native_request_offset = len(rpc.requests)
        await rpc.call("prompt.submit", {"session_id": sid, "text": "SMOKE_NATIVE"})
        await rpc.event("message.complete", sid, offset)
        require(not any(event.get("type") in {"terminal.read.request", "window.read.request", "preview.read.request"} for event in rpc.events[offset:]), "native request escaped to absent Electron client")
        require(not any(packet.get("method") in {"terminal.read", "window.read", "preview.read", "preview.act"} for packet in rpc.requests[native_request_offset:]), "native server request escaped to absent Electron client")
        passed("model_requested_native_tool_rejected_without_renderer_wait")

        offset = len(rpc.events)
        await rpc.call("prompt.submit", {"session_id": sid, "text": "SMOKE_SLOW"})
        require(await asyncio.to_thread(fixture.stub.slow_started.wait, 30), "slow local model did not start")
        await rpc.call("session.interrupt", {"session_id": sid})
        completion = await rpc.event("message.complete", sid, offset, timeout=30)
        require(completion.get("payload", {}).get("status") == "interrupted", "interrupt did not finish the active turn")
        passed("real_stream_interrupt")

        history_before = await rpc.call("session.history", {"session_id": sid})
        observed = [event["seq"] for event in rpc.events if event.get("session_id") == sid and "seq" in event]
        require(observed and observed == sorted(set(observed)), "event sequence missing/duplicated/out of order")
        watermark = observed[-min(4, len(observed))]
        await rpc.close()
        try:
            rejected = await fixture.connect(ticket)
        except InvalidStatus:
            pass
        else:
            await rejected.close()
            raise AssertionError("used upstream ticket accepted")
        rpc = await fixture.connect()
        resumed = await rpc.call("session.resume", {"session_id": stored, "source": "web", "defer_history": True})
        require(resumed["session_id"] == sid, "warm resume did not reuse safe live session")
        require((await rpc.call("session.activate", {"session_id": sid, "cols": 96, "omit_messages": True})).get("session_id") == sid, "reconnected Desktop activation failed")
        replay = await rpc.call("session.events.since", {"session_id": sid, "last_seen": watermark})
        require(replay["count"] > 0 and all(event["seq"] > watermark for event in replay["events"]), "reconnect replay lost events")
        require((await rpc.call("session.history", {"session_id": sid})) == history_before, "warm resume changed history")
        second = await fixture.connect()
        try:
            concurrent = await asyncio.gather(rpc.call("session.resume", {"session_id": stored, "source": "web", "lazy": True}), second.call("session.resume", {"session_id": stored, "source": "web", "omit_messages": True}))
            require(all(item["session_id"] == sid for item in concurrent), "concurrent resume created duplicate live sessions")
        finally:
            await second.close()
        passed("fresh_ticket_warm_concurrent_resume_reconnect_event_replay")
        await asyncio.sleep(max(0, expiry_deadline - time.monotonic()))
        try:
            expired = await fixture.connect(expired_ticket)
        except InvalidStatus as error:
            require(error.response.status_code == 403, "expired ticket returned unexpected rejection")
        else:
            await expired.close()
            raise AssertionError("expired upstream ticket accepted")
        passed("real_upstream_ticket_expiry")
        await rpc.close()
        fixture.stop()
        fixture.start()
        rpc = await fixture.connect()
        restored = await rpc.call("session.resume", {"session_id": stored, "source": "web", "defer_history": True})
        restored_sid = restored["session_id"]
        # Lazy hydration is async; history reads expose durable storage directly.
        cold_history = await rpc.call("session.history", {"session_id": restored_sid})
        require(cold_history == history_before, "cold restart/resume changed durable conversation")
        new_ready = await rpc.event("gateway.ready", timeout=15)
        require(new_ready != ready, "restart gateway replay identity unchanged")
        passed("cold_gateway_restart_resume_preserves_exact_history")
    finally:
        await rpc.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report", type=Path)
    parser.add_argument("--candidate-digest", default="unrecorded")
    args = parser.parse_args()
    cases = []
    report = {"suite": "desktop_rpc", "status": "failed", "candidate_digest": args.candidate_digest, "script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(), "cases": cases, "external_model_calls": 0, "cm_bff_verified": False, "browser_verified": False}
    try:
        with tempfile.TemporaryDirectory(prefix="hermes-desktop-rpc-") as temporary:
            fixture = Fixture(Path(temporary))
            try:
                fixture.start()
                asyncio.run(exercise(fixture, cases))
                report["local_model_requests"] = len(fixture.stub.calls)
                report["actual_tool_names"] = sorted(fixture.stub.tools)
                report["unsupported_model_probe_paths"] = sorted(fixture.stub.unsupported_paths)
                print("Local model unsupported probe paths: " + json.dumps(report["unsupported_model_probe_paths"]), flush=True)
                report["status"] = "passed"
            except Exception:
                diagnostic = (Path(temporary) / "gateway.log").read_text(encoding="utf-8", errors="replace")[-5000:]
                for value in fixture.secrets:
                    diagnostic = diagnostic.replace(value, "[REDACTED]")
                print(diagnostic, flush=True)
                raise
            finally:
                fixture.close()
    finally:
        if args.report:
            args.report.parent.mkdir(parents=True, exist_ok=True)
            args.report.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
