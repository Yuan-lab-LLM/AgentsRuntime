#!/usr/bin/env python3
"""Real single-container Hermes protocol smoke. Run inside the candidate image.

This does not replace real ClawManager BFF/browser or model-call acceptance.
It uses isolated disposable workspace state and performs no model requests.
"""
import asyncio
import http.cookiejar
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import tempfile
import time
import urllib.error
import urllib.request

from websockets.asyncio.client import connect
from websockets.exceptions import InvalidStatus

ORIGIN = "https://bff.invalid"
BASE = "http://127.0.0.1:20000"


def main():
    # Exercise the patched boundary against the actual locked upstream credential
    # store, not a mocked verifier. No credential is printed or sent externally.
    from hermes_cli.dashboard_auth.ws_tickets import internal_ws_credential
    from hermes_cli.lite_gateway_boundary import verified_internal_peer
    internal = internal_ws_credential()
    scope = {"client": ("127.0.0.1", 1), "headers": [(b"sec-websocket-protocol", ("hermes-gateway-v1,hermes-internal." + internal).encode())]}
    assert verified_internal_peer(scope), "real internal credential rejected"
    assert verified_internal_peer(scope), "internal reconnect credential rejected"
    assert not verified_internal_peer(dict(scope, client=("192.0.2.1", 1))), "non-loopback internal credential accepted"
    assert not verified_internal_peer(dict(scope, headers=[(b"sec-websocket-protocol", b"hermes-internal.invalid")])), "invalid internal credential accepted"
    private_values = [secrets.token_urlsafe(32), secrets.token_urlsafe(32)]
    with tempfile.TemporaryDirectory(prefix="hermes-lite-smoke-") as temporary:
        workspace = Path(temporary) / "hermes/user-7/instance-11"
        state = workspace / "home/.hermes"
        state.mkdir(parents=True)
        (state / "config.yaml").write_text("model:\n  provider: custom\n  default: auto\nfixture_unknown: preserve-me\n", encoding="utf-8")
        (state / "gateway.json").write_text("{}", encoding="utf-8")
        sentinel = state / "existing-session.txt"
        sentinel.write_text("preserve-me", encoding="utf-8")
        environment = dict(os.environ, **{
            "HOME": str(workspace / "home"), "HERMES_HOME": str(state),
            "CLAWMANAGER_WORKSPACE_PATH": str(workspace),
            "PORT": "20000", "CLAWMANAGER_GATEWAY_PORT": "20000",
            "CLAWMANAGER_CONTROL_UI_ORIGIN": ORIGIN,
            "CLAWMANAGER_TRUSTED_PROXY_CIDRS": "192.0.2.0/24",
            "CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED": "true",
            "CLAWMANAGER_HERMES_BACKEND_MODE": "serve",
            "HERMES_DASHBOARD_BASIC_AUTH_USERNAME": "clawmanager",
            "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD": private_values[0],
            "CLAWMANAGER_LLM_BASE_URL": "http://ai.invalid/v1",
            "CLAWMANAGER_LLM_API_KEY": private_values[1],
            "OPENAI_BASE_URL": "http://ai.invalid/v1", "OPENAI_API_KEY": private_values[1],
        })
        cookies = http.cookiejar.CookieJar()
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(cookies))

        def request(path, body=None, origin=ORIGIN, extra=None):
            headers = {"Origin": origin, "Accept": "application/json"}
            data = None
            if body is not None:
                data = json.dumps(body).encode()
                headers["Content-Type"] = "application/json"
            headers.update(extra or {})
            req = urllib.request.Request(BASE + path, data=data, headers=headers)
            try:
                with opener.open(req, timeout=3) as response:
                    return response.status, response.read()
            except urllib.error.HTTPError as error:
                return error.code, error.read()

        def ticket():
            status, body = request("/api/auth/ws-ticket", {})
            assert status == 200, "ticket endpoint failed"
            value = json.loads(body)["ticket"]
            private_values.append(value)
            return value

        async def websocket_checks():
            value = ticket()
            protocols = ["hermes-gateway-v1", "hermes-gateway-ticket." + value]
            uri = BASE.replace("http://", "ws://") + "/api/events?channel=smoke-readiness"
            async with connect(uri, origin=ORIGIN, subprotocols=protocols, open_timeout=5) as socket:
                waiter = await socket.ping(b"readiness")
                await asyncio.wait_for(waiter, timeout=5)
            try:
                async with connect(uri, origin=ORIGIN, subprotocols=protocols, open_timeout=5):
                    raise AssertionError("single-use ticket replay was accepted")
            except InvalidStatus:
                pass
            try:
                async with connect(uri, origin="https://evil.invalid", subprotocols=["hermes-gateway-v1", "hermes-gateway-ticket." + ticket()], open_timeout=5):
                    raise AssertionError("untrusted websocket origin was accepted")
            except InvalidStatus:
                pass
            # The bundle is a headless session worker used by the managed
            # Desktop protocol; no legacy Dashboard/TUI page is shipped.
            import psutil
            uri = BASE.replace("http://", "ws://") + "/api/pty?channel=smoke-chat&fresh=1"
            async with connect(uri, origin=ORIGIN, subprotocols=["hermes-gateway-v1", "hermes-gateway-ticket." + ticket()], open_timeout=5) as socket:
                output = await asyncio.wait_for(socket.recv(), timeout=25)
                assert output, "classic chat returned no terminal output"
                children = psutil.Process(process.pid).children(recursive=True)
                node_commands = [child.cmdline() for child in children if child.name() == "node"]
                assert ["/usr/local/bin/node", "--expose-gc", "/opt/hermes-agent/hermes_cli/tui_dist/entry.js"] in node_commands, "Desktop chat did not start the managed session worker"
                for command in node_commands:
                    assert not any("internal=" in arg or "token=" in arg for arg in command), "credential appeared in child argv"

        log_path = Path(temporary) / "server.log"
        with log_path.open("wb") as logfile:
            process = subprocess.Popen(["/usr/local/bin/start-hermes-lite-backend"], env=environment,
                                       stdout=logfile, stderr=subprocess.STDOUT, start_new_session=True)
            try:
                deadline = time.monotonic() + 90
                while time.monotonic() < deadline:
                    if process.poll() is not None:
                        raise AssertionError("Hermes exited before readiness")
                    try:
                        if request("/api/health")[0] == 200:
                            break
                    except OSError:
                        pass
                    time.sleep(0.25)
                else:
                    raise AssertionError("Hermes startup timed out")
                assert request("/api/auth/me")[0] == 401, "unauthenticated request accepted"
                assert request("/api/health", origin="https://evil.invalid")[0] == 403, "untrusted Origin accepted"
                assert request("/api/health", origin="https://bff.invalid:0")[0] == 403, "different Origin port accepted"
                assert request("/api/health", extra={"X-Forwarded-For": "192.0.2.8"})[0] == 403, "spoofed proxy accepted"
                status, body = request("/auth/password-login", {"provider": "basic", "username": "clawmanager", "password": private_values[0]})
                assert status == 200 and json.loads(body).get("ok"), "password login failed"
                private_values.extend(cookie.value for cookie in cookies)
                assert cookies, "login returned no session cookie"
                status, body = request("/api/auth/me")
                assert status == 200 and json.loads(body).get("provider") == "basic", "authenticated identity probe failed"
                for path in ("/api/ssh/ownership", "/api/console", "/api/desktop", "/api/cloud", "/api/hermes/update"):
                    assert request(path)[0] == 403, "unsupported native/update API accepted"
                assert request("/api/health?token=fixture-secret")[0] == 403, "credential URL accepted"
                asyncio.run(websocket_checks())
                status, body = request("/")
                assert status == 404 and b"<html" not in body.lower(), "legacy Dashboard UI unexpectedly available"
                assert all(value.encode() not in body for value in private_values), "credential leaked into disabled UI response"
                assert sentinel.read_text() == "preserve-me", "existing state changed"
                print("PASS: actual Hermes login, HTTP identity, WS ping/replay/Origin, managed session worker, legacy UI absence, state preservation")
            except Exception:
                diagnostic = log_path.read_text(encoding="utf-8", errors="replace")[-6000:]
                for value in private_values:
                    diagnostic = diagnostic.replace(value, "[REDACTED]")
                print(diagnostic)
                raise
            finally:
                if process.poll() is None:
                    os.killpg(process.pid, signal.SIGTERM)
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        os.killpg(process.pid, signal.SIGKILL)
                        process.wait(timeout=5)


if __name__ == "__main__":
    main()
