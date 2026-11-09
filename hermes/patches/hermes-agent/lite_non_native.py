"""Execution policy for managed Lite, including Team members; not Pro.

The policy is fixed by the Lite-only build artifact. Workspace dotenv files
load with override=True upstream, so no environment switch is an authority
for granting native capabilities. Pro images do not install this module.
"""
from functools import wraps
import json
import os
import re
import shlex

ENABLED = True
POLICY_VERSION = "lite-non-native-v2"
_AGENT_SEAL = object()
NATIVE_TOOLSETS = frozenset({"desktop_ui", "project", "desktop_project", "computer_use", "catalog", "onboarding"})
# Exact locked toolsets.py desktop_ui + project + computer_use members.
NATIVE_TOOLS = frozenset({
    "read_terminal", "close_terminal", "desktop_preview", "drive_preview",
    "annotate_preview", "read_window_below", "focus_pane", "react_to_message",
    "setup_mcp", "tour", "tip", "desktop_project", "computer_use",
    "gui_tour", "show_tip", "set_theme", "manage_catalog", "setup_beat",
})
_SESSION_ID = re.compile(r"[A-Za-z0-9_:-]{1,160}\Z")
_PROFILE_NAME = re.compile(r"[a-z0-9][a-z0-9_-]{0,63}\Z")
_RPC_METHOD = re.compile(r"[A-Za-z][A-Za-z0-9_.:-]{0,127}\Z")
_RPC_KEY = re.compile(r"[A-Za-z][A-Za-z0-9_.:-]{0,127}\Z")
_NATIVE_RPC_PREFIXES = ("shell.", "cli.", "desktop.", "computer.", "browser.", "terminal.")
_NATIVE_RPC_METHODS = frozenset({"tools.call", "tool.call"})
_MODEL_NAME = re.compile(r"[A-Za-z0-9][A-Za-z0-9_./:@+-]{0,255}\Z")
_MODEL_SWITCH = re.compile(r"([A-Za-z0-9][A-Za-z0-9_./:@+-]{0,255}) --provider ([A-Za-z0-9][A-Za-z0-9_./:@+-]{0,255}) --session\Z")
REASONING_EFFORTS = frozenset({"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"})


class NonNativeSessionError(RuntimeError):
    def __init__(self):
        super().__init__("incompatible_lite_live_session")


def filter_tool_names(names):
    return set(names) - NATIVE_TOOLS if ENABLED else names


def is_native_call(function_name, function_args=None):
    if function_name in NATIVE_TOOLS:
        return True
    # v0.22.5 permits GUI tools through the deferred bridge. Reject the wrapper
    # even when its schema/scope probe prevents upstream from unwrapping it.
    name = function_args.get("name") if function_name == "tool_call" and isinstance(function_args, dict) else None
    return isinstance(name, str) and name in NATIVE_TOOLS


def toolset_filter(fn):
    @wraps(fn)
    def filtered(*args, **kwargs):
        selected = fn(*args, **kwargs)
        if not ENABLED or selected is None:
            return selected
        return type(selected)(name for name in selected if name not in NATIVE_TOOLSETS)
    return filtered


def install_gateway(namespace):
    """Install policy wrappers after upstream's split-module rebinding."""
    for name in ("_gui_surface_toolsets", "_load_enabled_toolsets"):
        namespace[name] = toolset_filter(namespace[name])
    namespace["_methods"]["config.set"] = session_config(namespace["_methods"]["config.set"])


def tool_dispatch(fn):
    @wraps(fn)
    def guarded(function_name, *args, **kwargs):
        arguments = args[0] if args else kwargs.get("function_args")
        if ENABLED and is_native_call(function_name, arguments):
            # Do not inspect/reflect arguments or invoke renderer callbacks.
            return json.dumps({"error": "unsupported_native_desktop_tool"})
        return fn(function_name, *args, **kwargs)
    return guarded


def _agent_names(agent):
    definitions = getattr(agent, "tools", None)
    valid = getattr(agent, "valid_tool_names", None)
    if not isinstance(definitions, list) or not isinstance(valid, (set, frozenset)):
        raise NonNativeSessionError()
    try:
        names = {tool["function"]["name"] for tool in definitions}
        if not all(isinstance(name, str) for name in names | set(valid)):
            raise NonNativeSessionError()
    except (KeyError, TypeError):
        raise NonNativeSessionError() from None
    return names | set(valid)


def seal_agent(agent):
    if not ENABLED:
        return agent
    if _agent_names(agent) & NATIVE_TOOLS:
        raise NonNativeSessionError()
    selected = getattr(agent, "enabled_toolsets", None)
    if selected is not None:
        agent.enabled_toolsets = [name for name in selected if name not in NATIVE_TOOLSETS]
    agent._clawmanager_non_native_seal = _AGENT_SEAL
    return agent


def require_compatible_session(session):
    if not ENABLED:
        return
    agent = session.get("agent")
    # Deferred records can only publish through the sealed _make_agent path.
    if agent is None:
        return
    if (getattr(agent, "_clawmanager_non_native_seal", None) is not _AGENT_SEAL
            or _agent_names(agent) & NATIVE_TOOLS
            or set(getattr(agent, "enabled_toolsets", None) or ()) & NATIVE_TOOLSETS):
        raise NonNativeSessionError()


def require_compatible_runtimes(records, session_key):
    if not ENABLED:
        return
    for sid, session in records:
        if (isinstance(session, dict) and not session.get("_finalized")
                and str(session.get("session_key") or sid) == session_key):
            require_compatible_session(session)


def external_transport(transport, *, stdio=False):
    # Only the verified loopback credential can set this ASGI scope bit.
    # A source string / request parameter is never transport authority.
    scope = getattr(getattr(transport, "_ws", None), "scope", {})
    return ENABLED and not (stdio or (isinstance(scope, dict) and scope.get("hermes_lite_internal") is True))


def _path_key(key):
    return key.lower().replace("-", "_") in {
        "cwd", "path", "directory", "dir", "folder", "folders", "primary_path",
        "file", "files", "workspace", "workspace_path", "root", "root_path", "repo_path",
        "project_path", "file_path", "filename",
    }


def _workspace_path_allowed(value):
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


def _rpc_value_allowed(key, value, depth=0):
    if depth > 8:
        return False
    if value is None or isinstance(value, (bool, int, float)):
        return True
    if isinstance(value, str):
        if len(value) > 5 * 1024 * 1024:
            return False
        if key == "profile" and not _PROFILE_NAME.fullmatch(value):
            return False
        return not _path_key(key) or _workspace_path_allowed(value)
    if isinstance(value, list):
        return len(value) <= 1024 and all(_rpc_value_allowed(key, item, depth + 1) for item in value)
    if isinstance(value, dict):
        return (len(value) <= 256 and all(isinstance(child_key, str) and _RPC_KEY.fullmatch(child_key)
                and child_key not in {"connectionId", "connection_id"}
                and _rpc_value_allowed(child_key, child, depth + 1)
                for child_key, child in value.items()))
    return False


def rpc_denial(method, params, transport, *, stdio=False):
    if not external_transport(transport, stdio=stdio):
        return None
    if (not isinstance(method, str) or not _RPC_METHOD.fullmatch(method)
            or method in _NATIVE_RPC_METHODS or method.startswith(_NATIVE_RPC_PREFIXES)):
        return (-32601, "unsupported_lite_web_rpc")
    if (not isinstance(params, dict) or len(params) > 128
            or len(json.dumps(params, separators=(",", ":"), ensure_ascii=False)) > 8 * 1024 * 1024):
        return (-32602, "unsupported_lite_web_parameters")
    for key, value in params.items():
        if (not isinstance(key, str) or not _RPC_KEY.fullmatch(key)
                or key in {"connectionId", "connection_id"} or not _rpc_value_allowed(key, value)):
            return (-32602, "unsupported_lite_web_parameters")
    if "session_id" in params and (not isinstance(params["session_id"], str) or not _SESSION_ID.fullmatch(params["session_id"])):
        return (-32602, "unsupported_lite_web_parameters")
    if method.startswith("profiles."):
        for key in ("name", "clone_from"):
            value = params.get(key)
            if value is not None and (not isinstance(value, str) or not _PROFILE_NAME.fullmatch(value)):
                return (-32602, "unsupported_lite_web_parameters")
    if "source" in params and params["source"] not in {"web", "desktop"}:
        return (-32602, "unsupported_lite_web_parameters")
    if method in {"session.create", "session.resume"}:
        params["source"] = "web"
    return None


def rpc_prepare(method, params, namespace, transport, *, stdio=False):
    """Hermes validates provider/model semantics; CM owns instance scoping."""
    return None


def session_config(fn):
    """Keep a config request and the close/reaper ownership claim serialized.

    Do not hold _sessions_lock across provider I/O or agent construction. The
    existing resume lock is the lifecycle lock; all close paths claim through
    it. Reject unready agents before invoking upstream's blocking recovery path.
    No missing/closed session can reach config.set's global fallback.
    """
    @wraps(fn)
    def guarded(rid, params):
        ns = fn.__globals__
        transport = ns["current_transport"]()
        if not external_transport(transport, stdio=transport is ns["_stdio_transport"]):
            return fn(rid, params)
        key, value = params.get("key"), params.get("value")
        if params.get("scope") not in {None, "", "session"}:
            return ns["_err"](rid, -32602, "unsupported_lite_session_configuration")
        if ((key == "reasoning" and (not isinstance(value, str) or value not in REASONING_EFFORTS))
                or (key == "fast" and (not isinstance(value, str) or value not in {"normal", "fast", "priority", "auto", "cold", "ultrafast"}))):
            return ns["_err"](rid, -32602, "unsupported_lite_session_configuration")
        if key == "model":
            try:
                tokens = shlex.split(value)
            except (ValueError, TypeError):
                tokens = ["--global"]
            if any(token == "--global" or token.startswith("--global=") for token in tokens):
                return ns["_err"](rid, -32602, "unsupported_lite_session_configuration")
        with ns["_session_resume_lock"]:
            with ns["_sessions_lock"]:
                session = ns["_sessions"].get(params.get("session_id"))
                if session is None or session.get("_closing") or session.get("_finalized"):
                    return ns["_err"](rid, 4001, "session not found; resume required")
                require_compatible_session(session)
                if session.get("agent") is None or session.get("resume_hydrating"):
                    return ns["_err"](rid, 5032, "session agent is not ready")
            return fn(rid, params)
    return guarded


def rpc_result(method, response, namespace, transport, *, stdio=False):
    """Keep readiness failures and approval replay on the safe client surface."""
    if not external_transport(transport, stdio=stdio) or not isinstance(response, dict):
        return response
    if method in {"setup.status", "setup.runtime_check", "approval.pending", "approval.received", "config.set", "model.options"} and "error" in response:
        response = dict(response)
        error = response.get("error") or {}
        response["error"] = {"code": error.get("code", -32000), "message": "Runtime request failed"}
        return response
    return response
