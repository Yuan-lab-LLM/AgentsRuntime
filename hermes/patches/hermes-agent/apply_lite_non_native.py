#!/usr/bin/env python3
"""Patch the v0.22.5 split gateway, before upstream binds it to server globals."""
import hashlib
from pathlib import Path
import shutil
import sys

EXPECTED = {'tui_gateway/server.py': 'a5502fddd35a1f82180a1408bda924ec3c7fac2982168ff0b7f2080347bf3173',
 'tui_gateway/agent_factory.py': 'c7d3cbea68653509a2dd26415d7ab0944ededc8b1dd0bde7e916628ae2ad3c94',
 'tui_gateway/rpc_dispatch.py': 'd81dd5b12e67305c5838c5a8b19a900a6aa2285beec5fd95454671051aaa22b1',
 'tui_gateway/session_registry.py': '7e6f8cd4764f3b1b010e67d61b5ee9a7c6d98db229667de06243565dec5f7ffa',
 'tui_gateway/session_lifecycle.py': '5fef6910c3c81302b1a52228d29c6acacbd4558fc8681b4dae5beff858d2359b',
 'tui_gateway/methods_session.py': '54a19fb9e1a93ed29d0b321b5794c86ab3144ac280e51ef7bb30d294a6ae8fa3',
 'tui_gateway/session_notifications.py': '22e7ce2953eeb665c0324f22a9456005ccf14df3086bf21fc878c1d6d2c6e7f6',
 'model_tools.py': 'da5b7db1b05de615658e56c4172426ed82099a093c5b586e48e65ef930b778c8',
 'hermes_cli/env_loader.py': '4cf068f5e73e5a4b1e153fd692ff6bd1a96222ea10de9188eaba977d5917b689',
 'agent/secret_scope.py': 'dce64ce99a6966836108e202e1db41c4be97bd3dad41a79e357f39b8d7e90dec',
 'hermes_cli/web_routers/models.py': '7250d2b17addca12b3e2d50b1b019dec96cbec03b7227de186f35810bbdd9d73',
 'hermes_cli/web_routers/messaging.py': '745ccade21ff3c60e8881f9e2d73299abb6dea7d2dc921dd2bb15dfcf08a2e23',
 'tui_gateway/methods_complete.py': 'e9125c95ec3bc37b85744b599c63e58072361b99907349c9c229ec7579473990',
 'tui_gateway/command_discovery.py': '433e301971368efe21bb20c74a8e89574346e1af9306476c77e2c1f526756187',
 'tools/skills_tool.py': 'f75e1047efba2640daefa2f03b897db12ac74890792621151ed9fbfd974176b8',
 'tools/skills_tool_plugin.py': '173ab14a51453556d617ce3681305e85018f7efb3903062c84bbf19b95320984',
 'agent/skill_commands.py': 'f392971ceea36792eac3e705da6ac6a9a14ab1d5f86caa25e783d0dd4cf6c4c5'}


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

    def edit(path, before, after):
        sources[path] = replace_once(sources[path], before, after)

    edit("tui_gateway/server.py", "logger = logging.getLogger(__name__)",
         "from hermes_cli import lite_non_native as _lite_non_native\n\nlogger = logging.getLogger(__name__)")
    # Decorators must be applied AFTER bind_module has rebound upstream bodies.
    edit("tui_gateway/server.py", "del _m\n", "del _m\n_lite_non_native.install_gateway(globals())\n")
    # The existing pool carries transport/profile context and retirement
    # accounting. Slow NFS discovery must not occupy the serial WS lane.
    edit("tui_gateway/server.py", "_LONG_HANDLERS = frozenset({\n",
         "_LONG_HANDLERS = frozenset({\n    \"commands.catalog\",  # Lite: bounded background skill discovery\n")
    edit("tui_gateway/command_discovery.py", "def command_catalog(load_cfg=None, module_loader=import_module, scope=None) -> dict:\n",
         "from hermes_cli import lite_environment as _lite_environment\n\n"
         "@_lite_environment.env_read_request\n"
         "def command_catalog(load_cfg=None, module_loader=import_module, scope=None) -> dict:\n")
    edit("tools/skills_tool.py", "def _skill_catalog(*, skip_disabled: bool = False, include_hidden: bool = False) -> list[dict[str, Any]]:\n",
         "from hermes_cli import lite_environment as _lite_environment\n\n"
         "def _skill_catalog(*, skip_disabled: bool = False, include_hidden: bool = False) -> list[dict[str, Any]]:\n")
    edit("tools/skills_tool.py", "        for skill_md in _iter(scan_dir):\n",
         "        for skill_md in _lite_environment.prefetch_skill_files(\n"
         "                p for p in _iter(scan_dir) if not any(part in _EXCLUDED_SKILL_DIRS for part in p.parts)):\n")
    edit("tools/skills_tool_plugin.py", '    return path.read_text(encoding="utf-8-sig", errors="replace")\n',
         '    from hermes_cli.lite_environment import read_skill_text\n'
         '    return read_skill_text(path, encoding="utf-8-sig", errors="replace")\n')
    edit("agent/skill_commands.py", "    frontmatter, body = _parse_frontmatter(skill_md.read_text(encoding='utf-8-sig'))\n",
         "    from hermes_cli.lite_environment import read_skill_text\n"
         "    frontmatter, body = _parse_frontmatter(read_skill_text(skill_md, encoding='utf-8-sig'))\n")
    edit("tui_gateway/agent_factory.py", "    return agent\n", "    return _lite_non_native.seal_agent(agent)\n")
    edit("tui_gateway/rpc_dispatch.py", "    rid, method, params = normalized\n",
         "    rid, method, params = normalized\n"
         "    transport = current_transport()\n"
         "    denial = _lite_non_native.rpc_denial(method, params, transport, stdio=transport is _stdio_transport)\n"
         "    if denial is not None:\n        return _err(rid, *denial)\n")
    edit("tui_gateway/rpc_dispatch.py", "        response = fn(rid, params)\n",
         "        with _sessions_lock:\n"
         "            session = _sessions.get(params.get(\"session_id\"))\n"
         "            if session is not None:\n                _lite_non_native.require_compatible_session(session)\n"
         "        response = _lite_non_native.rpc_result(method, fn(rid, params), globals(), transport, stdio=transport is _stdio_transport)\n"
         "    except _lite_non_native.NonNativeSessionError:\n"
         "        return _err(rid, 4033, \"incompatible_lite_live_session\")\n")
    edit("tui_gateway/session_registry.py", "            return sid, session\n",
         "            _lite_non_native.require_compatible_session(session)\n            return sid, session\n")
    edit("tui_gateway/session_registry.py", "    transport: Transport | None = None, omit_messages: bool = False, inline_images: bool = True) -> dict:\n",
         "    transport: Transport | None = None, omit_messages: bool = False, inline_images: bool = True) -> dict:\n"
         "    _lite_non_native.require_compatible_session(session)\n")
    edit("tui_gateway/session_registry.py",
         '            with _sessions_lock:\n                discarded = _sessions.pop(sid, None) if _sessions.get(sid) is session else None',
         '            with _session_resume_lock:\n                with _sessions_lock:\n                    discarded = _sessions.pop(sid, None) if _sessions.get(sid) is session else None')
    edit("tui_gateway/session_lifecycle.py",
         '    with _sessions_lock:  # RLock: predicate + claim in one critical section\n        current = _sessions.get(sid)\n        if predicate is not None and (current is None or not predicate(current)):\n            return False\n        session = _pop_session_by_id(sid)',
         '    with _session_resume_lock:\n        with _sessions_lock:  # Predicate + ownership claim remain atomic\n            current = _sessions.get(sid)\n            if predicate is not None and (current is None or not predicate(current)):\n                return False\n            session = _pop_session_by_id(sid)')
    edit("tui_gateway/methods_session.py", "    if (refusal := _reattach_refusal(ctx.rid, sid, session)) is not None:",
         "    _lite_non_native.require_compatible_session(session)\n    if (refusal := _reattach_refusal(ctx.rid, sid, session)) is not None:")
    edit("tui_gateway/session_notifications.py", "    global _desktop_ui_wired\n",
         "    global _desktop_ui_wired\n    if _lite_non_native.ENABLED:\n        return\n")
    edit("model_tools.py", "def get_tool_definitions(",
         "from hermes_cli import lite_non_native as _lite_non_native\n\n\ndef get_tool_definitions(")
    edit("model_tools.py", "        filtered_tools = _apply_dynamic_schemas(registry.get_definitions(tools_to_include, quiet=quiet_mode))",
         "        tools_to_include = _lite_non_native.filter_tool_names(tools_to_include)\n"
         "        filtered_tools = _apply_dynamic_schemas(registry.get_definitions(tools_to_include, quiet=quiet_mode))")
    edit("model_tools.py", "def handle_function_call(\n", "@_lite_non_native.tool_dispatch\ndef handle_function_call(\n")
    edit("hermes_cli/env_loader.py", "import dotenv\n", "import dotenv\nfrom hermes_cli import lite_environment as _lite_environment\n")
    edit("hermes_cli/env_loader.py", "def load_hermes_dotenv(\n",
         "@_lite_environment.preserve_environment\ndef load_hermes_dotenv(\n")
    edit("hermes_cli/env_loader.py", "            if value is None or (not override and name in os.environ):",
         "            if _lite_environment.protected(name) or value is None or (not override and name in os.environ):")
    edit("agent/secret_scope.py", "    if _is_global_env(name):\n",
         "    from hermes_cli import lite_environment as _lite_environment\n"
         "    if _lite_environment.protected(name):\n"
         "        return _lite_environment.managed_value(name, default)\n"
         "    if _is_global_env(name):\n")
    edit("agent/secret_scope.py", "def load_env_file(",
         "from hermes_cli import lite_environment as _lite_environment\n\n"
         "@_lite_environment.secret_file\ndef load_env_file(")
    edit("agent/secret_scope.py", "    secrets.update((k, v) for k, v in load_managed_env().items() if not _is_global_env(k))\n    return secrets\n",
         "    secrets.update((k, v) for k, v in load_managed_env().items() if not _is_global_env(k))\n"
         "    from hermes_cli import lite_environment as _lite_environment\n"
         "    return _lite_environment.secret_scope(secrets, _is_global_env)\n")
    # Only read-only catalog builders receive a request-local dotenv snapshot.
    # Keep write routes and all calls outside this bounded request uncached.
    edit("hermes_cli/web_routers/models.py", "        def _build_payload_scoped() -> dict:\n",
         "        from hermes_cli import lite_environment as _lite_environment\n"
         "        @_lite_environment.env_read_request\n"
         "        def _build_payload_scoped() -> dict:\n")
    edit("hermes_cli/web_routers/messaging.py",
         "async def get_messaging_platforms(profile: Optional[str] = None):\n",
         "async def get_messaging_platforms(profile: Optional[str] = None):\n"
         "    from hermes_cli import lite_environment as _lite_environment\n")
    edit("hermes_cli/web_routers/messaging.py",
         "    def _run():\n        # Profile-scoped so the dashboard's global profile switcher",
         "    @_lite_environment.env_read_request\n"
         "    def _run():\n        # Profile-scoped so the dashboard's global profile switcher")
    edit("hermes_cli/web_routers/messaging.py",
         "        gateway_config = load_gateway_config()\n",
         "        from hermes_cli import lite_environment as _lite_environment\n"
         "        gateway_config = _lite_environment.catalog_value(\"gateway-config\", load_gateway_config)\n")
    edit("hermes_cli/web_routers/messaging.py",
         "    gateway_running = resolve_gateway_liveness(\n",
         "    from hermes_cli import lite_environment as _lite_environment\n"
         "    gateway_running = _lite_environment.catalog_value(\"gateway-liveness\", lambda: resolve_gateway_liveness(\n")
    edit("hermes_cli/web_routers/messaging.py", "    ).running\n", "    )).running\n")
    # The Desktop picker prefers RPC, so it needs the same bounded catalog
    # snapshot as REST. Keep route context construction inside the snapshot.
    edit("tui_gateway/methods_complete.py",
         "    return _ok(rid, build_model_options_payload(\n"
         "        _model_picker_context(_session_agent(params)), explicit_only=bool(params.get(\"explicit_only\")),\n"
         "        include_unconfigured=bool(params.get(\"include_unconfigured\")), refresh=bool(params.get(\"refresh\"))))",
         "    from hermes_cli import lite_environment as _lite_environment\n"
         "    @_lite_environment.env_read_request\n"
         "    def catalog():\n"
         "        return build_model_options_payload(\n"
         "            _model_picker_context(_session_agent(params)), explicit_only=bool(params.get(\"explicit_only\")),\n"
         "            include_unconfigured=bool(params.get(\"include_unconfigured\")), refresh=bool(params.get(\"refresh\")))\n"
         "    return _ok(rid, catalog())")
    for relative, source in sources.items():
        compile(source, relative, "exec")
    return sources


def apply(root):
    for relative, source in patched_sources(root).items():
        (root / relative).write_text(source, encoding="utf-8", newline="\n")
    for name in ("lite_non_native.py", "lite_environment.py"):
        shutil.copyfile(Path(__file__).with_name(name), root / "hermes_cli" / name)


if __name__ == "__main__":
    apply(Path(sys.argv[1]))
