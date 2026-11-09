#!/usr/bin/env python3
"""Deterministic, authenticated model fixture for an isolated acceptance namespace.

Run in a separate test Pod with network egress denied. This file has no client
for any upstream model. Config contains only disposable campaign credentials;
it is never installed as a model proxy for existing user instances.
"""
import argparse
import hmac
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import re
import threading
import time


def require(ok):
    if not ok:
        raise ValueError("invalid_acceptance_model_config")


def validate(config):
    require(config.get("schema_version") == 1)
    run_id = config["run_id"]
    require(re.fullmatch(r"[0-9a-f]{32}", run_id))
    require(isinstance(config["observer_token"], str) and len(config["observer_token"]) >= 32)
    instances = config["instances"]
    require(isinstance(instances, list) and 2 <= len(instances) <= 8)
    keys, ids = set(), set()
    for item in instances:
        require(type(item["instance_id"]) is int and item["instance_id"] > 0)
        require(isinstance(item["token"], str) and len(item["token"]) >= 32)
        require(re.fullmatch(r"/workspaces/hermes/user-[1-9][0-9]*/instance-" + str(item["instance_id"]) + r"/\.acceptance-" + run_id, item["fixture_root"]))
        require(item["token"] not in keys and item["token"] != config["observer_token"])
        require(item["instance_id"] not in ids)
        keys.add(item["token"])
        ids.add(item["instance_id"])
    return config


class Fixture:
    def __init__(self, config):
        self.config = validate(config)
        self.lock = threading.Lock()
        self.requests = 0
        self.markers = set()
        self.tools = {}
        self.tool_schemas = {}
        self.instances = set()
        self.tool_results = 0
        self.turns = {}

    def authenticate(self, authorization):
        for item in self.config["instances"]:
            if hmac.compare_digest(authorization, "Bearer " + item["token"]):
                return item
        return None

    def observation(self):
        with self.lock:
            return {"schema_version": 1, "run_id": self.config["run_id"],
                    "model_mode": "deterministic-stub", "request_count": self.requests,
                    "external_requests": 0, "prompt_markers": sorted(self.markers),
                    "tool_counts": dict(self.tools), "instances": sorted(self.instances),
                    "tool_schema_counts": dict(self.tool_schemas),
                    "tool_results_observed": self.tool_results}

    def completion(self, item, payload):
        messages = payload.get("messages")
        if not isinstance(messages, list) or len(messages) > 4096:
            raise ValueError("invalid_messages")
        last_user = next((m.get("content", "") for m in reversed(messages) if m.get("role") == "user"), "")
        text = last_user if isinstance(last_user, str) else json.dumps(last_user)
        match = re.search(r"ACCEPTANCE_" + self.config["run_id"] + r"_(TEXT|SLOW|CLARIFY|ONCE|DENY)\b", text)
        if not match:
            raise ValueError("campaign_marker_required")
        marker, kind = match.group(0), match.group(1)
        with self.lock:
            key = (item["instance_id"], marker)
            number = self.turns.get(key, 0)
            self.turns[key] = number + 1
            self.requests += 1
            self.markers.add(marker)
            self.instances.add(item["instance_id"])
            self.tool_results += int(bool(messages) and messages[-1].get("role") == "tool")
            for tool in payload.get("tools", []):
                name = tool.get("function", {}).get("name") if isinstance(tool, dict) else None
                if isinstance(name, str) and re.fullmatch(r"[A-Za-z0-9_]{1,128}", name):
                    self.tool_schemas[name] = self.tool_schemas.get(name, 0) + 1
        function = None
        if number == 0 and kind == "CLARIFY":
            function = {"name": "clarify", "arguments": json.dumps({"questions": [{"question": "Choose an acceptance fixture value", "choices": ["alpha", "beta"]}]})}
        elif number == 0 and kind in {"ONCE", "DENY"}:
            target = item["fixture_root"] + ("/allow-fixture" if kind == "ONCE" else "/deny-fixture")
            function = {"name": "terminal", "arguments": json.dumps({"command": "rm -rf -- " + target})}
        message = {"role": "assistant", "content": "Local deterministic response " + marker}
        if function:
            with self.lock:
                self.tools[function["name"]] = self.tools.get(function["name"], 0) + 1
            message = {"role": "assistant", "content": None, "tool_calls": [{"id": "call_" + kind.lower(), "type": "function", "function": function}]}
        return kind, message, "tool_calls" if function else "stop"


def handler(fixture):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_args):
            pass

        def reply(self, status, value):
            body = json.dumps(value, separators=(",", ":")).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)

        def do_GET(self):
            if self.path == "/__acceptance__/observation":
                if not hmac.compare_digest(self.headers.get("Authorization", ""), "Bearer " + fixture.config["observer_token"]):
                    return self.reply(401, {"error": "unauthorized"})
                return self.reply(200, fixture.observation())
            if self.path == "/healthz":
                return self.reply(200, {"status": "ready", "mode": "deterministic-stub"})
            if self.path == "/v1/models" and fixture.authenticate(self.headers.get("Authorization", "")):
                return self.reply(200, {"object": "list", "data": [{"id": "acceptance-model", "object": "model", "owned_by": "isolated-acceptance"}]})
            return self.reply(404, {"error": "unsupported"})

        def do_POST(self):
            item = fixture.authenticate(self.headers.get("Authorization", ""))
            if not item:
                return self.reply(401, {"error": "unauthorized"})
            if self.path != "/v1/chat/completions":
                return self.reply(404, {"error": "unsupported"})
            try:
                size = int(self.headers.get("Content-Length", "0"))
                if not 0 < size <= 4 * 1024 * 1024:
                    raise ValueError("invalid_size")
                request = json.loads(self.rfile.read(size))
                kind, message, finish = fixture.completion(item, request)
                payload = {"id": "chatcmpl-acceptance", "object": "chat.completion", "created": int(time.time()), "model": "acceptance-model", "choices": [{"index": 0, "message": message, "finish_reason": finish}], "usage": {"prompt_tokens": 12, "completion_tokens": 8, "total_tokens": 20}}
                if request.get("stream") is not True:
                    if kind == "SLOW":
                        time.sleep(9)
                    return self.reply(200, payload)
                # Hermes' OpenAI stream is exercised on the real gateway path.
                self.send_response(200)
                self.send_header("Content-Type", "text/event-stream")
                self.send_header("Cache-Control", "no-cache")
                self.end_headers()

                def chunk(delta, reason=None):
                    packet = {**{k: v for k, v in payload.items() if k not in {"choices", "usage"}}, "object": "chat.completion.chunk", "choices": [{"index": 0, "delta": delta, "finish_reason": reason}]}
                    self.wfile.write(("data: " + json.dumps(packet) + "\n\n").encode())
                    self.wfile.flush()

                chunk({"role": "assistant"})
                if message.get("tool_calls"):
                    chunk({"tool_calls": [{"index": 0, **message["tool_calls"][0]}]})
                elif kind == "SLOW":
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
            except (BrokenPipeError, ConnectionResetError):
                pass
            except (ValueError, KeyError, TypeError):
                self.reply(400, {"error": "invalid_acceptance_request"})
    return Handler


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True, type=Path)
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", default=18080, type=int)
    args = parser.parse_args()
    fixture = Fixture(json.loads(args.config.read_text(encoding="utf-8")))
    server = ThreadingHTTPServer((args.host, args.port), handler(fixture))
    server.daemon_threads = True
    print(json.dumps({"status": "ready", "mode": "deterministic-stub", "run_id": fixture.config["run_id"]}), flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
