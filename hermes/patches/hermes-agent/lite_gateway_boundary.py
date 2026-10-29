"""Lite-only network and Origin boundary, installed outside upstream auth.

Uvicorn proxy rewriting MUST remain disabled: ``scope.client`` must be the
socket peer, never an attacker-controlled X-Forwarded-For value.
"""
import ipaddress
import json
import logging
import os
import time
from urllib.parse import parse_qsl, urlsplit

_LOG = logging.getLogger("hermes.lite_boundary")
_NATIVE_ROUTES = ("/api/console", "/api/ssh", "/api/desktop", "/api/cloud", "/api/hermes/update")
INTERNAL_PROTOCOL_PREFIX = "hermes-internal."
_PATH_KEYS = frozenset({
    "cwd", "path", "directory", "dir", "folder", "folders", "file", "files",
    "primary_path", "workspace", "workspace_path", "root", "root_path",
    "repo_path", "project_path", "file_path", "filename",
})


def path_key(key):
    return isinstance(key, str) and key.lower().replace("-", "_") in _PATH_KEYS


def workspace_path_allowed(value):
    if not isinstance(value, str):
        return False
    if value in {"", "."}:
        return True
    if not isinstance(value, str) or "\\" in value or "\0" in value or len(value) > 4096:
        return False
    if ".." in value.split("/"):
        return False
    root = os.environ.get("CLAWMANAGER_WORKSPACE_PATH", "").strip()
    if not root:
        return not (os.path.isabs(value) or value.startswith("/"))
    base = os.path.realpath(root)
    candidate = os.path.realpath(value if os.path.isabs(value) else os.path.join(base, value))
    try:
        roots = [base]
        # This is the Agent-authored, member-scoped shared directory, not an
        # arbitrary path supplied by an HTTP/RPC request. Never trust /team
        # globally or another member's workspace.
        if os.environ.get("CLAWMANAGER_TEAM_ENABLED", "").lower() in {"1", "true", "yes", "on", "enabled"}:
            shared = os.environ.get("CLAWMANAGER_TEAM_SHARED_DIR", "").strip()
            if os.path.isabs(shared):
                roots.append(os.path.realpath(shared))
        return any(os.path.commonpath((root, candidate)) == root for root in roots)
    except ValueError:
        return False


def json_workspace_allowed(value, key="", depth=0):
    if depth > 12:
        return False
    if isinstance(value, str):
        return not path_key(key) or workspace_path_allowed(value)
    if isinstance(value, list):
        return len(value) <= 10000 and all(json_workspace_allowed(item, key, depth + 1) for item in value)
    if isinstance(value, dict):
        return len(value) <= 10000 and all(
            isinstance(child_key, str) and json_workspace_allowed(child, child_key, depth + 1)
            for child_key, child in value.items()
        )
    return value is None or isinstance(value, (bool, int, float))


def internal_credential(headers):
    values = [item.strip()[len(INTERNAL_PROTOCOL_PREFIX):]
              for key, value in headers if key.lower() == b"sec-websocket-protocol"
              for item in value.decode("latin1").split(",")
              if item.strip().startswith(INTERNAL_PROTOCOL_PREFIX)]
    return values[0] if len(values) == 1 else ""


def verified_internal_peer(scope):
    try:
        if not ipaddress.ip_address(scope["client"][0]).is_loopback:
            return False
        credential = internal_credential(scope.get("headers", []))
        if not credential:
            return False
        from hermes_cli.dashboard_auth.ws_tickets import TicketInvalid, consume_internal_credential
        try:
            consume_internal_credential(credential)
        except TicketInvalid:
            return False
        return True
    except (ValueError, KeyError, TypeError, ImportError):
        return False


def canonical_origin(raw):
    try:
        value = urlsplit(raw)
        if (value.scheme not in {"http", "https"} or not value.hostname or "*" in value.hostname
                or value.username is not None or value.password is not None or value.path not in {"", "/"}
                or value.query or value.fragment or any(c.isspace() or ord(c) < 32 or ord(c) == 127 for c in raw)):
            return None
        port = value.port
        return (value.scheme, value.hostname.lower(), port if port is not None else (443 if value.scheme == "https" else 80))
    except ValueError:
        return None


class LiteGatewayBoundary:
    def __init__(self, app):
        self.app = app
        self.origin_raw = os.environ.get("CLAWMANAGER_CONTROL_UI_ORIGIN", "")
        self.origin = canonical_origin(self.origin_raw)
        self.policy_file = os.environ.get("CLAWMANAGER_PROXY_POLICY_FILE", "")
        self.network_isolated = os.environ.get("CLAWMANAGER_NETWORK_ISOLATED") == "1"
        self.policy_namespace = os.environ.get("CLAWMANAGER_PROXY_POLICY_NAMESPACE", "")
        raw = os.environ.get("CLAWMANAGER_TRUSTED_PROXY_CIDRS", "")
        self.proxies = []
        try:
            self.proxies = [ipaddress.ip_network(item.strip(), strict=False)
                            for item in raw.split(",") if item.strip()]
        except ValueError:
            raise RuntimeError("invalid_trusted_proxy_config") from None
        if not self.origin or (self.network_isolated and not self.policy_file) or (self.policy_file and not self.policy_namespace) or (not self.policy_file and (not self.proxies or any(network.prefixlen == 0 for network in self.proxies))):
            raise RuntimeError("missing_lite_network_boundary")

    def effective_proxies(self):
        if not self.policy_file:
            return self.proxies
        # Agent-owned, atomically replaced lease outside member workspaces.
        # Never fall back to an old environment/config after lease loss.
        try:
            with open(self.policy_file, encoding="utf-8") as source:
                raw = source.read(16385)
            if len(raw) > 16384:
                return []
            policy = json.loads(raw)
            now = int(time.time() * 1000)
            expires = policy.get("expires_at_ms")
            values = policy.get("proxies")
            if (policy.get("contract") != 1 or policy.get("namespace") != self.policy_namespace
                    or type(expires) is not int or not now < expires <= now + 90000
                    or not isinstance(values, list) or not 0 < len(values) <= 128):
                return []
            addresses = [ipaddress.ip_address(value) for value in values if isinstance(value, str)]
            if len(addresses) != len(values) or any(address.is_unspecified or address.is_loopback or address.is_multicast or address.is_link_local or getattr(address, "scope_id", None) for address in addresses):
                return []
            if self.network_isolated:
                # The authenticated parent ingress relays only into this
                # instance's private NET. Preserve native Origin/auth checks;
                # shared-NET peers and expired policies never gain this path.
                return [ipaddress.ip_network("127.0.0.1/32"), ipaddress.ip_network("::1/128")]
            return [ipaddress.ip_network(address) for address in addresses]
        except (OSError, ValueError, TypeError, AttributeError):
            return []

    def denial(self, scope):
        headers = scope.get("headers", [])
        origins = [value.decode("latin1") for name, value in headers if name.lower() == b"origin"]
        forwarded = any(name.lower().startswith(b"x-forwarded-") or name.lower() == b"forwarded"
                        for name, _ in headers)
        try:
            peer = ipaddress.ip_address(scope["client"][0])
        except (ValueError, KeyError, TypeError):
            return "untrusted_gateway_peer"
        trusted = any(peer in network for network in self.effective_proxies())
        if not trusted and (not peer.is_loopback or forwarded):
            return "untrusted_gateway_peer"
        # A server-spawned TUI cannot set an Origin with the standard Node
        # WebSocket API. Only a real loopback peer presenting the process-local
        # credential can be normalized to the deployment's internal Origin.
        if scope["type"] == "websocket" and internal_credential(headers):
            if not verified_internal_peer(scope) or forwarded:
                return "invalid_internal_websocket_peer"
            if not origins:
                headers = list(headers) + [(b"origin", self.origin_raw.encode())]
                scope["headers"] = headers
                origins = [self.origin_raw]
            scope["hermes_lite_internal"] = True
        if len(origins) > 1 or (origins and canonical_origin(origins[0]) != self.origin):
            return "websocket_origin_rejected" if scope["type"] == "websocket" else "origin_rejected"
        if not origins and (scope["type"] == "websocket" or scope.get("method") not in {"GET", "HEAD"}):
            return "origin_required"
        path = scope.get("path", "")
        if any(path == route or path.startswith(route + "/") for route in _NATIVE_ROUTES):
            return "unsupported_native_desktop_api"
        query = parse_qsl(scope.get("query_string", b"").decode("latin1"), keep_blank_values=True)
        if any(name in {"token", "ticket", "internal", "password", "access_token"} for name, _ in query):
            return "credential_query_rejected"
        if path == "/api/pty":
            for name, value in query:
                if name not in {"channel", "resume", "fresh", "attach", "profile"} or (name == "profile" and value not in {"", "current"}):
                    return "unsupported_chat_parameters"
        for name, value in query:
            if path_key(name) and not workspace_path_allowed(value):
                return "workspace_path_rejected"
        return None

    async def __call__(self, scope, receive, send):
        if scope["type"] not in {"http", "websocket"}:
            return await self.app(scope, receive, send)
        reason = self.denial(scope)
        if not reason and scope["type"] == "http" and scope.get("method") not in {"GET", "HEAD"}:
            content_type = next((value.decode("latin1").lower() for name, value in scope.get("headers", []) if name.lower() == b"content-type"), "")
            if "json" in content_type:
                chunks = []
                total = 0
                while True:
                    event = await receive()
                    if event.get("type") != "http.request":
                        reason = "invalid_request_body"
                        break
                    chunk = event.get("body", b"")
                    total += len(chunk)
                    if total > 8 * 1024 * 1024:
                        reason = "request_body_too_large"
                        break
                    chunks.append(chunk)
                    if not event.get("more_body", False):
                        break
                body = b"".join(chunks)
                if not reason and body:
                    try:
                        if not json_workspace_allowed(json.loads(body)):
                            reason = "workspace_path_rejected"
                    except (ValueError, UnicodeDecodeError):
                        reason = "invalid_request_body"
                if not reason:
                    replayed = False
                    original_receive = receive

                    async def replay_receive():
                        nonlocal replayed
                        if not replayed:
                            replayed = True
                            return {"type": "http.request", "body": body, "more_body": False}
                        return await original_receive()

                    receive = replay_receive
        if reason:
            # Fixed categories only: never reflect cookies, URLs or headers.
            _LOG.warning("hermes_lite_request_denied reason=%s", reason)
            if scope["type"] == "websocket":
                await send({"type": "websocket.close", "code": 1008, "reason": reason})
            else:
                await send({"type": "http.response.start", "status": 403,
                            "headers": [(b"content-type", b"application/json"), (b"cache-control", b"no-store")]})
                await send({"type": "http.response.body", "body": ('{"error":"' + reason + '"}').encode()})
            return
        async def safe_send(event):
            if event["type"] == "websocket.accept":
                offered = [part.strip() for key, value in scope.get("headers", [])
                           if key.lower() == b"sec-websocket-protocol"
                           for part in value.decode("latin1").split(",")]
                # Select only the public protocol, never a credential-bearing
                # value; events and PTY otherwise omit protocol selection.
                event = dict(event, subprotocol="hermes-gateway-v1" if "hermes-gateway-v1" in offered else None)
            await send(event)
        await self.app(scope, receive, safe_send)


def install_lite_boundary(app):
    app.add_middleware(LiteGatewayBoundary)
