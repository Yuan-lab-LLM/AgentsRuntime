#!/usr/bin/env python3
"""Patch only the pinned upstream sources, after the independent network patch."""
import hashlib
from pathlib import Path
import shutil
import sys

EXPECTED = {
    "tui_gateway/server.py": "e264c1c7cc2e99051c1e36f4019dd29b739d5b7f1c61b8e364f87ad6039d1900",
    "tui_gateway/methods_session.py": "c4c0b3355be3ecc7f7fdf8ebcbd46fb3f360f9dded5ca9908f96ed0ce7e561d0",
    "model_tools.py": "5227bfe30f7c3f6eb20d064f648420138031e038d38eac2829d25b749739ad11",
    "hermes_cli/env_loader.py": "26712ba3e020b5c306cd456e8d2cc96084fd2295f7ec4104466fffd0adeab87d",
}


def replace_once(source, before, after):
    if source.count(before) != 1:
        raise ValueError("Lite non-native patch anchor changed")
    return source.replace(before, after)


def patched_sources(root):
    sources = {}
    for relative, digest in EXPECTED.items():
        content = (root / relative).read_bytes()
        if hashlib.sha256(content).hexdigest() != digest:
            raise ValueError("Lite non-native patch requires exact locked upstream sources")
        sources[relative] = content.decode("utf-8")
    server = sources["tui_gateway/server.py"]
    server = replace_once(server, "logger = logging.getLogger(__name__)",
                          "from hermes_cli import lite_non_native as _lite_non_native\n\nlogger = logging.getLogger(__name__)")
    for name in ("_gui_surface_toolsets", "_load_enabled_toolsets"):
        server = replace_once(server, "def " + name + "(", "@_lite_non_native.toolset_filter\ndef " + name + "(")
    server = replace_once(server, "    rid, method, params = normalized\n    fn = _methods.get(method)",
                          "    rid, method, params = normalized\n"
                          "    transport = current_transport()\n"
                          "    denial = _lite_non_native.rpc_denial(method, params, transport, stdio=transport is _stdio_transport)\n"
                          "    if denial is not None:\n"
                          "        return _err(rid, *denial)\n"
                          "    denial = _lite_non_native.rpc_prepare(method, params, globals(), transport, stdio=transport is _stdio_transport)\n"
                          "    if denial is not None:\n"
                          "        return _err(rid, *denial)\n"
                          "    fn = _methods.get(method)")
    server = replace_once(server, "    try:\n        return fn(rid, params)\n    finally:\n        _current_rpc_method.reset(token)",
                          "    try:\n"
                          "        with _sessions_lock:\n"
                          "            session = _sessions.get(params.get(\"session_id\"))\n"
                          "            if session is not None:\n"
                          "                _lite_non_native.require_compatible_session(session)\n"
                          "        return _lite_non_native.rpc_result(method, fn(rid, params), globals(), transport, stdio=transport is _stdio_transport)\n"
                          "    except _lite_non_native.NonNativeSessionError:\n"
                          "        return _err(rid, 4033, \"incompatible_lite_live_session\")\n"
                          "    finally:\n        _current_rpc_method.reset(token)")
    server = replace_once(server, '@method("config.set")\n@_profile_scoped\ndef _(rid, params: dict) -> dict:',
                          '@method("config.set")\n@_profile_scoped\n@_lite_non_native.session_config\ndef _(rid, params: dict) -> dict:')
    # A CM page/socket is only a viewer. Retain accepted work until its normal
    # turn finalization; the existing timer still reclaims completed orphans.
    # Explicit interrupt/close and model/security deadlines keep their authority.
    server = replace_once(server,
                          '            if _session_has_active_delegations(sid, current):\n',
                          '            if (_session_has_active_delegations(sid, current)\n'
                          '                    or (_lite_non_native.ENABLED and current.get("running"))):\n')
    # Explicit close, disconnect, supersession and orphan reaping already claim
    # under the resume lock. Bring the idle/LRU/shutdown convenience funnel into
    # that same lifecycle order; keep slow finalization outside both locks.
    server = replace_once(server,
                          '    if predicate is None:\n        session = _pop_session_by_id(sid)\n    else:\n        with _sessions_lock:\n            current = _sessions.get(sid)\n            if current is None or not predicate(current):\n                return False\n            session = _pop_session_by_id(sid)\n    return _teardown_popped_session(session, end_reason=end_reason)',
                          '    with _session_resume_lock:\n        if predicate is None:\n            session = _pop_session_by_id(sid)\n        else:\n            with _sessions_lock:\n                current = _sessions.get(sid)\n                if current is None or not predicate(current):\n                    return False\n                session = _pop_session_by_id(sid)\n    return _teardown_popped_session(session, end_reason=end_reason)')
    server = replace_once(server,
                          '            with _sessions_lock:\n                discarded = _sessions.pop(sid, None) if _sessions.get(sid) is session else None',
                          '            with _session_resume_lock:\n                with _sessions_lock:\n                    discarded = _sessions.pop(sid, None) if _sessions.get(sid) is session else None')
    server = replace_once(server, "    agent._context_cwd_is_launch_artifact = bool(\n        context_cwd_is_launch_artifact\n    )\n    return agent",
                          "    agent._context_cwd_is_launch_artifact = bool(\n        context_cwd_is_launch_artifact\n    )\n"
                          "    return _lite_non_native.seal_agent(agent)")
    server = replace_once(server, "    with _session_resume_lock:\n        live = _find_live_session_by_key(session_key)",
                          "    with _session_resume_lock:\n"
                          "        with _sessions_lock:\n"
                          "            _lite_non_native.require_compatible_runtimes(list(_sessions.items()), session_key)\n"
                          "        live = _find_live_session_by_key(session_key)")
    server = replace_once(server, ") -> dict:\n    with session[\"history_lock\"]:\n        if cols is not None:",
                          ") -> dict:\n    _lite_non_native.require_compatible_session(session)\n"
                          "    with session[\"history_lock\"]:\n        if cols is not None:")
    server = replace_once(server, "    global _desktop_ui_wired\n    if _desktop_ui_wired:",
                          "    global _desktop_ui_wired\n    if _lite_non_native.ENABLED:\n        return\n    if _desktop_ui_wired:")
    sources["tui_gateway/server.py"] = server
    sessions = sources["tui_gateway/methods_session.py"]
    sessions = replace_once(sessions, "                if live is not None:\n                    if owns_db:",
                            "                if live is not None:\n"
                            "                    _lite_non_native.require_compatible_session(live)\n"
                            "                    if owns_db:")
    sessions = replace_once(sessions, "            with _session_resume_lock:\n                if _sessions.get(sid) is not session:",
                            "            with _session_resume_lock:\n"
                            "                _lite_non_native.require_compatible_session(session)\n"
                            "                if _sessions.get(sid) is not session:")
    sources["tui_gateway/methods_session.py"] = sessions
    tools = sources["model_tools.py"]
    tools = replace_once(tools, "def get_tool_definitions(\n",
                          "from hermes_cli import lite_non_native as _lite_non_native\n\n\ndef get_tool_definitions(\n")
    tools = replace_once(tools, "    filtered_tools = registry.get_definitions(tools_to_include, quiet=quiet_mode)",
                          "    tools_to_include = _lite_non_native.filter_tool_names(tools_to_include)\n"
                          "    filtered_tools = registry.get_definitions(tools_to_include, quiet=quiet_mode)")
    tools = replace_once(tools, "def handle_function_call(\n", "@_lite_non_native.tool_dispatch\ndef handle_function_call(\n")
    sources["model_tools.py"] = tools
    env = sources["hermes_cli/env_loader.py"]
    env = replace_once(env, "from dotenv import load_dotenv\n",
                       "from hermes_cli import lite_environment as _lite_environment\n"
                       "from hermes_cli.lite_environment import load_dotenv\n")
    env = replace_once(env, "def load_hermes_dotenv(\n",
                       "@_lite_environment.preserve_environment\ndef load_hermes_dotenv(\n")
    sources["hermes_cli/env_loader.py"] = env
    for relative, source in sources.items():
        compile(source, relative, "exec")
    return sources


def apply(root):
    # Validate every input and anchor before writing any target.
    sources = patched_sources(root)
    for relative, source in sources.items():
        (root / relative).write_text(source, encoding="utf-8", newline="\n")
    shutil.copyfile(Path(__file__).with_name("lite_non_native.py"), root / "hermes_cli/lite_non_native.py")
    shutil.copyfile(Path(__file__).with_name("lite_environment.py"), root / "hermes_cli/lite_environment.py")


if __name__ == "__main__":
    apply(Path(sys.argv[1]))
