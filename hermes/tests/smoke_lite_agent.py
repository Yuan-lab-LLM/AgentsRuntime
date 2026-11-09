#!/usr/bin/env python3
"""Real shared-agent/Hermes lifecycle smoke with a disposable control-plane stub.

Run inside the candidate image with --network none. No model requests and no
claim of actual ClawManager BFF/browser acceptance.
"""
import http.server
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import tempfile
import threading
import time
import urllib.request


def main():
    reports = []

    class Backend(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_HEAD(self):
            self.send_response(200)
            self.end_headers()

        def do_POST(self):
            data = self.rfile.read(int(self.headers.get("Content-Length", "0")))
            reports.append(data)
            response = json.dumps({"data": {"pod_id": 1}}).encode()
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(response)))
            self.end_headers()
            try:
                self.wfile.write(response)
            except (BrokenPipeError, ConnectionResetError):
                # The agent may cancel an in-flight report during shutdown.
                pass

    backend = http.server.ThreadingHTTPServer(("127.0.0.1", 19091), Backend)
    threading.Thread(target=backend.serve_forever, daemon=True).start()
    with open("/etc/hosts", "a") as hosts:
        hosts.write("\n127.0.0.1 clawmanager-backend.smoke.svc\n")
    keys = {name: secrets.token_urlsafe(32) for name in ("control", "report", "instance", "llm")}
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))

    def control(path, body=None, method=None):
        data = None if body is None else json.dumps(body).encode()
        request = urllib.request.Request("http://127.0.0.1:19090" + path, data=data, method=method,
                                         headers={"X-ClawManager-Control-Token": keys["control"],
                                                  "Content-Type": "application/json"})
        # Team shutdown drains its Redis consumer as well as the Web process.
        # Match the bounded control-plane operation budget, not a ping timeout.
        with opener.open(request, timeout=30) as response:
            raw = response.read()
            return json.loads(raw) if raw else None

    def await_running(instance, generation):
        deadline = time.monotonic() + 100
        while time.monotonic() < deadline:
            for report in reversed(reports):
                for gateway in json.loads(report).get("gateways", []):
                    if gateway["instance_id"] != instance or gateway["generation"] != generation:
                        continue
                    if gateway["state"] == "error":
                        raise AssertionError("real agent startup failed: " + gateway.get("error_message", "unknown"))
                    if gateway["state"] == "running":
                        return gateway
            time.sleep(0.2)
        raise AssertionError("real agent did not report running")

    with tempfile.TemporaryDirectory(prefix="hsa-") as temporary:
        root = Path(temporary)
        root.chmod(0o755)
        workspaces = root / "workspaces"
        # Model an empty mounted volume: the mount root already exists, while
        # Hermes must create every managed parent under the entrypoint's umask.
        workspaces.mkdir(mode=0o755)
        workspaces.chmod(0o755)
        workspace = workspaces / "hermes/user-7/instance-11"
        assert not (workspaces / "hermes").exists(), "smoke workspace must start empty"
        env = dict(os.environ, **{
            "RUNTIME_AGENT_CONTROL_TOKEN": keys["control"], "RUNTIME_AGENT_REPORT_TOKEN": keys["report"],
            "CLAWMANAGER_BACKEND_URL": "http://clawmanager-backend.smoke.svc:19091",
            "CLAWMANAGER_RUNTIME_IMAGE_REF": "fixture/hermes@sha256:" + "a" * 64,
            "RUNTIME_WORKSPACE_ROOT": str(workspaces), "RUNTIME_AGENT_DATA_DIR": str(root / "agent-data"),
            "CLAWMANAGER_CONTROL_UI_ORIGIN": "http://clawmanager-backend.smoke.svc:19091",
            "CLAWMANAGER_TRUSTED_PROXY_CIDRS": "192.0.2.0/24",
            "CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED": "true", "CLAWMANAGER_HERMES_BACKEND_MODE": "serve",
            "POD_NAMESPACE": "isolated-smoke",
        })
        request = {"instance_id": 11, "user_id": 7, "agent_type": "hermes", "workspace_path": str(workspace),
                   "gateway_port": 20001, "uid": 10001, "gid": 10001, "generation": 1,
                   "environment": {"CLAWMANAGER_INSTANCE_TOKEN": keys["instance"],
                                   "CLAWMANAGER_LLM_API_KEY": keys["llm"],
                                   "CLAWMANAGER_LLM_BASE_URL": "http://clawmanager-backend.smoke.svc:19091/v1",
                                   "CLAWMANAGER_LLM_MODEL": "auto",
                                   "RUNTIME_AGENT_CONTROL_TOKEN": "must-not-forward",
                                   "PYTHONPATH": "/tmp/must-not-forward"}}
        log_path = root / "agent.log"
        with log_path.open("wb") as output:
            process = subprocess.Popen(["/usr/local/bin/hermes-lite-entrypoint"], env=env, stdout=output,
                                       stderr=subprocess.STDOUT, start_new_session=True)
            try:
                deadline = time.monotonic() + 20
                while time.monotonic() < deadline:
                    if process.poll() is not None:
                        raise AssertionError("shared agent exited before control readiness")
                    try:
                        if control("/v1/health")["status"] == "ready":
                            break
                    except OSError:
                        pass
                    time.sleep(0.2)
                else:
                    raise AssertionError("shared agent control readiness timed out")
                if Path("/usr/local/share/clawmanager/hermes-proxy-policy-v1").is_file():
                    def renew_policy():
                        current = control("/v1/proxy-policy")
                        control("/v1/proxy-policy", {"namespace": "isolated-smoke", "expected_revision": current["revision"], "proxies": ["192.0.2.8"]}, method="PUT")
                    renew_policy()
                    policy_path = Path("/run/clawmanager-proxy-policy/policy.json")
                    assert policy_path.parent.stat().st_mode & 0o777 == 0o755, "managed lease directory cannot be traversed by isolated readers"
                    reader = subprocess.run(
                        ["/bin/sh", "-c", 'test -r "$1" && test ! -w "$1" && test ! -w "$2"',
                         "managed-reader", str(policy_path), str(policy_path.parent)],
                        user=request["uid"], group=request["gid"], extra_groups=[],
                        stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                        timeout=5, check=False)
                    assert reader.returncode == 0, "real isolated UID cannot read the Agent-created lease without write access"
                    def policy_loop():
                        while process.poll() is None:
                            time.sleep(10)
                            if process.poll() is not None:
                                return
                            try:
                                renew_policy()
                            except OSError:
                                return
                    threading.Thread(target=policy_loop, daemon=True).start()
                agent_status = Path(f"/proc/{process.pid}/status").read_text()
                assert any(line.split() == ["Umask:", "0077"] for line in agent_status.splitlines()), "real entrypoint restrictive umask missing"
                release = json.loads(Path("/usr/local/share/hermes-lite/release.json").read_text())
                capability = control("/v1/health").get("capabilities", {}).get("hermes_desktop_web")
                assert capability == {
                    "contract_version": 2, "enabled": True,
                    "hermes_ref": release["hermes_ref"], "hermes_commit": release["hermes_commit"],
                    "rpc_protocol": "hermes-jsonrpc-v1", "backend_mode": "serve", "auth_mode": "password-cookie",
                    "artifacts_verified": True, "release_accepted": release["desktop_web_accepted"],
                    "payload_sha256": release["payload_sha256"],
                }, "managed runtime compatibility or truthful release acceptance missing"
                started = time.monotonic()
                first = control("/v1/gateways", request)
                assert first["status"] == "starting" and time.monotonic() - started < 3, "create was not asynchronous"
                assert control("/v1/gateways", request)["gateway_id"] == first["gateway_id"], "create not idempotent"
                running = await_running(11, 1)
                pid = running["gateway_pid"]
                # Container root normally lacks CAP_SYS_PTRACE. Inspect as the
                # instance UID and return only success/failure, never its keys.
                environment_check = subprocess.run(
                    ["/opt/hermes-agent/.venv/bin/python", "-I", "-c",
                     "import pathlib,sys; data=pathlib.Path('/proc/'+sys.argv[1]+'/environ').read_bytes(); "
                     "sys.exit(any(key in data for key in "
                     "(b'RUNTIME_AGENT_CONTROL_TOKEN=', b'RUNTIME_AGENT_REPORT_TOKEN=', b'PYTHONPATH=')))",
                     str(pid)], user=10001, group=10001, extra_groups=[], cwd="/", env={"LANG": "C"},
                    stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                    timeout=5, check=False)
                assert environment_check.returncode == 0, "child environment whitelist failed"
                state = workspace / "home/.hermes"
                for name in ("config.yaml", ".env", "gateway.json", ".clawmanager-hermes-workspace.json"):
                    info = (state / name).stat()
                    assert info.st_uid == 10001 and info.st_gid == 10001 and info.st_mode & 0o777 == 0o600, "managed ownership/mode failed"
                sentinel = state / "session-preservation-fixture"
                sentinel.write_text("preserve-me")
                request["generation"] = 2
                second = control("/v1/gateways", request)
                replacement = await_running(11, 2)
                assert replacement["gateway_pid"] != pid and not Path(f"/proc/{pid}").exists(), "old generation survived replacement"
                assert sentinel.read_text() == "preserve-me", "replacement lost workspace"
                if os.environ.get("HERMES_TEAM_TEST_REDIS_URL"):
                    from smoke_lite_team import exercise_team
                    exercise_team(control, await_running, workspaces, keys)
                control("/v1/drain", {"draining": True})
                assert control("/v1/health")["status"] == "draining", "drain not reported"
                control("/v1/gateways/" + second["gateway_id"], method="DELETE")
                assert sentinel.read_text() == "preserve-me", "delete lost workspace"
                assert not list((root / "agent-data/processes").glob("*.json")), "metadata not reclaimed"
                for report in reports:
                    assert keys["instance"].encode() not in report and keys["llm"].encode() not in report, "report leaked instance credential"
                output.flush()
                log = log_path.read_text(errors="replace")
                assert not any(value in log for value in keys.values()), "agent log leaked a credential"
                print("PASS: real entrypoint umask 077 and empty volume, shared agent create/idempotency, HTTP+WS readiness, UID/environment isolation, generation replacement, drain/delete, state preservation")
            except Exception:
                diagnostic = log_path.read_text(errors="replace")[-5000:]
                for value in keys.values():
                    diagnostic = diagnostic.replace(value, "[REDACTED]")
                print(diagnostic)
                raise
            finally:
                if process.poll() is None:
                    process.send_signal(signal.SIGTERM)
                    try:
                        process.wait(timeout=30)
                    except subprocess.TimeoutExpired:
                        os.killpg(process.pid, signal.SIGKILL)
                        process.wait(timeout=5)
                backend.shutdown()


if __name__ == "__main__":
    main()
