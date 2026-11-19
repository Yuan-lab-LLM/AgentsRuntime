#!/usr/bin/env python3
"""Version-gated native provider hook. Accept only the lock or known Lite patch."""
import ast
import hashlib
from pathlib import Path
import shutil
import sys

EXPECTED = "955054a4ebac704dc7f8182dbe7431e03716d59bc80044c05da2bf8533590372"
POOL_PREFIX = (
    '    # AgentsRuntime: managed instance credentials outrank saved endpoint pools.\n'
    '    managed_name = str(provider_name or "").strip().lower()\n'
    '    if (os.environ.get("CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED", "").strip().lower() in {"1", "true", "yes", "on"}\n'
    '            and (managed_name == "clawmanager" or managed_name.startswith("clawmanager-"))):\n'
    '        return None\n'
)
HOOK = '''    # AgentsRuntime: owner-authorized historical session references.
    # A disabled custom provider can also be selected through its native alias.
    # Do not mistake its absence from active lookup for a retired managed ref.
    if os.environ.get("CLAWMANAGER_MODEL_REFERENCE_PROTOCOL") == "1" and isinstance(_provs_cfg, dict):
        for managed_key, managed_block in _provs_cfg.items():
            if isinstance(managed_block, dict) and not is_provider_enabled(managed_block):
                managed_aliases = custom_provider_aliases(str(managed_block.get("name") or managed_key), str(managed_key))
                if _normalize_custom_provider_name(requested_provider) in managed_aliases:
                    raise ValueError("当前模型不可用：provider is disabled in config")
    from hermes_cli.managed_model_catalog import resolve_managed_runtime
    managed_runtime = resolve_managed_runtime(
        requested_provider=requested_provider, target_model=target_model,
        explicit_base_url=explicit_base_url, explicit_api_key=explicit_api_key,
        lookup_custom=_get_named_custom_provider, resolve_builtin=auth_mod.resolve_provider,
    )
    if managed_runtime is not None:
        return managed_runtime
'''
# Preserve upstream's disabled-provider gate and virtual MOA provider first.
ANCHOR = "    # Azure Anthropic short-circuit: when explicitly targeting an Azure endpoint\n"
TOOL_EXPECTED = "5aff6c7e95a280adc5124e7c1f1019b54984b8e7091db48bac35ad5bc2387813"
TOOL_ANCHOR = "        block_message = scope_block\n"
TOOL_HOOK = ("        from hermes_cli.managed_model_catalog import managed_tool_block\n"
             "        block_message = scope_block or managed_tool_block(agent, effective_task_id)\n")
LOOP_EXPECTED = "33924321c2ef848a7474d4daf4dd5ec1e98588aa54db967d92ea2fc6aa3ff56a"
AGENT_EXPECTED = "5b2e7083680e6c728f2306adc73e5f814c444aaa9ff3e3b840206142c67a7149"


def render_agent(source, *, expected_hash=AGENT_EXPECTED):
    marker = "                    # ClawManager: scoped owner-task revocation watcher.\n"
    original = source
    if marker in source:
        start = source.index(marker)
        end = source.index("                finally:\n", start)
        block = source[start:end]
        lines = block.splitlines(keepends=True)[3:]
        original = source[:start] + "".join(line[4:] for line in lines) + source[end:]
    if hashlib.sha256(original.encode("utf-8")).hexdigest() != expected_hash:
        raise ValueError("Managed monitor patch requires the exact locked Hermes agent")
    start = original.index("                    result = run_conversation(\n")
    end = original.index("                finally:\n", start)
    block = original[start:end]
    result = (original[:start] + marker +
              "                    from hermes_cli.managed_model_catalog import monitor_turn\n"
              "                    with monitor_turn(self, effective_task_id):\n" +
              "".join("    " + line for line in block.splitlines(keepends=True)) + original[end:])
    ast.parse(result)
    return result
LOOP_ANCHOR = "                def _perform_api_call(next_api_kwargs):\n"
LOOP_HOOK = (LOOP_ANCHOR +
             "                    from hermes_cli.managed_model_catalog import apply_task_headers\n"
             "                    next_api_kwargs = apply_task_headers(agent, effective_task_id, next_api_kwargs)\n")


def render_loop(source):
    original = source.replace(LOOP_HOOK, LOOP_ANCHOR, 1)
    if hashlib.sha256(original.encode("utf-8")).hexdigest() != LOOP_EXPECTED or original.count(LOOP_ANCHOR) != 1:
        raise ValueError("Managed task patch requires the exact locked Hermes loop")
    result = original.replace(LOOP_ANCHOR, LOOP_HOOK, 1)
    ast.parse(result)
    return result


def render_tools(source):
    original = source.replace(TOOL_HOOK, TOOL_ANCHOR, 1)
    if hashlib.sha256(original.encode("utf-8")).hexdigest() != TOOL_EXPECTED:
        raise ValueError("Managed tool patch requires the exact locked Hermes executor")
    if original.count(TOOL_ANCHOR) != 1:
        raise ValueError("Native tool dispatch anchor changed")
    result = original.replace(TOOL_ANCHOR, TOOL_HOOK, 1)
    ast.parse(result)
    return result


def render(source):
    original = source.replace(HOOK, "", 1)
    unpatched = original.replace(POOL_PREFIX, "", 1)
    if hashlib.sha256(unpatched.encode("utf-8")).hexdigest() != EXPECTED:
        raise ValueError("Managed model patch requires the exact locked Hermes resolver")
    tree = ast.parse(original)
    function = next(n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == "resolve_runtime_provider")
    if not {"requested", "target_model", "explicit_base_url", "explicit_api_key"}.issubset({arg.arg for arg in function.args.kwonlyargs}):
        raise ValueError("Native resolver signature changed")
    lines = original.splitlines(keepends=True)
    body = "".join(lines[function.lineno - 1:function.end_lineno])
    if body.count(ANCHOR) != 1:
        raise ValueError("Native resolver anchor changed")
    patched = "".join(lines[:function.lineno - 1]) + body.replace(ANCHOR, HOOK + ANCHOR) + "".join(lines[function.end_lineno:])
    ast.parse(patched)
    return patched


def apply(root):
    target = root / "hermes_cli/runtime_provider.py"
    if target.is_symlink() or not target.is_file():
        raise ValueError("Resolver must be a regular file")
    result = render(target.read_text(encoding="utf-8"))
    tools = root / "agent/tool_executor.py"
    if tools.is_symlink() or not tools.is_file():
        raise ValueError("Tool executor must be a regular file")
    tool_result = render_tools(tools.read_text(encoding="utf-8"))
    loop = root / "agent/conversation_loop.py"
    if loop.is_symlink() or not loop.is_file():
        raise ValueError("Conversation loop must be a regular file")
    loop_result = render_loop(loop.read_text(encoding="utf-8"))
    agent = root / "run_agent.py"
    if agent.is_symlink() or not agent.is_file():
        raise ValueError("Native agent must be a regular file")
    agent_result = render_agent(agent.read_text(encoding="utf-8"))
    helper = Path(__file__).with_name("managed_model_catalog.py")
    ast.parse(helper.read_text(encoding="utf-8"))
    shutil.copyfile(helper, root / "hermes_cli/managed_model_catalog.py")
    target.write_text(result, encoding="utf-8", newline="\n")
    tools.write_text(tool_result, encoding="utf-8", newline="\n")
    loop.write_text(loop_result, encoding="utf-8", newline="\n")
    agent.write_text(agent_result, encoding="utf-8", newline="\n")


if __name__ == "__main__":
    apply(Path(sys.argv[1]))
