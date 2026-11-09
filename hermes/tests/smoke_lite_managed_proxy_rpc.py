#!/usr/bin/env python3
"""Real managed Lite HTTP/RPC through two isolated TCP peers; no external model.

Run with the test relay network and proxy-startup-runtime network alias. The
existing Desktop RPC suite is reused to retain native tools/history semantics.
"""
import asyncio
import json
import os
from pathlib import Path
import socket
import tempfile
import threading
import time
import urllib.error
import urllib.request

from smoke_lite_desktop_rpc import Fixture, ORIGIN, exercise, require


class ManagedFixture(Fixture):
    def __init__(self, root):
        super().__init__(root)
        self.local_base = self.base
        self.peers = ["proxy-startup-peer", "proxy-startup-peer-b"]
        self.addresses = [socket.gethostbyname(peer) for peer in self.peers]
        self.policy = root / "managed-policy.json"
        self.env.update({"CLAWMANAGER_PROXY_POLICY_FILE": str(self.policy),
                         "CLAWMANAGER_PROXY_POLICY_NAMESPACE": "managed-rpc-fixture"})
        self.active = 0
        self.renew = threading.Event()
        self.renewer = None
        self.write_policy()

    def write_policy(self, ttl=60000, namespace="managed-rpc-fixture"):
        data = {"contract": 1, "namespace": namespace,
                "expires_at_ms": int(time.time() * 1000) + ttl,
                "proxies": [self.addresses[self.active]]}
        temporary = self.policy.with_suffix(".tmp")
        temporary.write_text(json.dumps(data), encoding="utf-8")
        temporary.chmod(0o644)
        os.replace(temporary, self.policy)

    def request(self, path, body=None, origin=ORIGIN):
        headers = {"Accept": "application/json",
                   "Cookie": "; ".join(item.name + "=" + item.value for item in self.cookies)}
        if origin is not None:
            headers["Origin"] = origin
        if body is not None:
            headers["Content-Type"] = "application/json"
        request = urllib.request.Request(self.base + path,
            data=json.dumps(body).encode() if body is not None else None, headers=headers)
        try:
            with self.opener.open(request, timeout=30) as response:
                return response.status, response.read(4 << 20), response.headers
        except urllib.error.HTTPError as error:
            return error.code, error.read(4 << 20), error.headers
        except TimeoutError:
            raise AssertionError("managed HTTP timeout: " + path) from None

    def select_peer(self, index):
        self.base = "http://" + self.peers[index] + ":" + str(self.port)

    def start(self):
        self.base = self.local_base
        super().start()
        self.select_peer(self.active)

    def renew_loop(self):
        while not self.renew.wait(10):
            self.write_policy()


async def run(fixture):
    pid = fixture.process.pid
    config = (fixture.state / "config.yaml").read_bytes()
    for active in (0, 1, 0):
        fixture.active = active
        fixture.write_policy()
        fixture.select_peer(active)
        require(fixture.request("/api/status")[0] == 200, "trusted peer HTTP")
        rpc = await fixture.connect()
        try:
            await rpc.event("gateway.ready", timeout=15)
            require(await rpc.call("ping") is not None, "trusted peer RPC")
            require(isinstance(await rpc.call("session.list"), dict), "trusted peer history")
        finally:
            await rpc.close()
        fixture.select_peer(1 - active)
        require(fixture.request("/api/status")[0] == 403, "removed peer HTTP admitted")
        require(fixture.request("/api/auth/ws-ticket", {})[0] == 403, "removed peer ticket admitted")
        fixture.select_peer(active)
    fixture.write_policy(ttl=-1)
    require(fixture.request("/api/status")[0] == 403, "expired lease admitted")
    fixture.write_policy(namespace="other-tenant")
    require(fixture.request("/api/status")[0] == 403, "foreign namespace admitted")
    fixture.policy.write_text("malformed", encoding="utf-8")
    require(fixture.request("/api/status")[0] == 403, "malformed lease admitted")
    fixture.write_policy()
    require(fixture.request("/api/status")[0] == 200, "lease recovery failed")
    require(fixture.process.pid == pid and fixture.process.poll() is None, "hot rotation restarted gateway")
    require((fixture.state / "config.yaml").read_bytes() == config, "hot rotation rewrote config")
    print("PASS: managed exact-peer A/B/A HTTP/RPC, lease fail-closed/recovery, unchanged PID/config", flush=True)
    fixture.renewer = threading.Thread(target=fixture.renew_loop, daemon=True)
    fixture.renewer.start()
    await exercise(fixture, [])


def main():
    with tempfile.TemporaryDirectory(prefix="hermes-managed-rpc-") as temporary:
        fixture = ManagedFixture(Path(temporary))
        try:
            fixture.start()
            asyncio.run(run(fixture))
        except Exception:
            diagnostic = (fixture.root / "gateway.log").read_text(errors="replace")[-5000:]
            for value in fixture.secrets:
                diagnostic = diagnostic.replace(value, "[REDACTED]")
            print(diagnostic, flush=True)
            raise
        finally:
            fixture.renew.set()
            if fixture.renewer:
                fixture.renewer.join(timeout=2)
            fixture.close()


if __name__ == "__main__":
    main()
