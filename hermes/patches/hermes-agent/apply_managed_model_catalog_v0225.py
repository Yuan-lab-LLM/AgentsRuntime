"""Managed model hooks for the exact v0.22.5 split native entry points."""
import ast
import hashlib
from pathlib import Path
import shutil
import sys
from apply_managed_model_catalog import render_agent

HASHES = {
    "hermes_cli/runtime_provider.py": "d60a99f56c55f1d0b41f7d9f0b812625eb0339fe96b6ac19861831ca1c4aaa43",
    "agent/tool_executor.py": "1b1b67d4b88f73c51dc147d0e0383b36c561d921d278d801326de5ba24b29702",
    "agent/turn_api_call.py": "809f38471e58b20d68911c73b39945c3b2b4f1e9de1c8be7d451ac3f91774be1",
    "agent/turn_facade.py": "e9176a8ae1d7822fa45c75b680d5ae4be7c949e49c877673aea867cc146856a0",
}
PROVIDER_ANCHOR = "    _raise_if_provider_disabled(requested_provider)\n"
PROVIDER_HOOK = """    from hermes_cli.managed_model_catalog import resolve_managed_runtime
    managed_runtime = resolve_managed_runtime(
        requested_provider=requested_provider, target_model=target_model,
        explicit_base_url=explicit_base_url, explicit_api_key=explicit_api_key,
        lookup_custom=_get_named_custom_provider, resolve_builtin=auth_mod.resolve_provider,
    )
    if managed_runtime is not None:
        return managed_runtime
"""
TOOL_ANCHOR = '    block_message, block_error_type = scope_block, "tool_scope_block"\n'
TOOL_HOOK = ('    from hermes_cli.managed_model_catalog import managed_tool_block\n'
             '    block_message, block_error_type = scope_block or managed_tool_block(agent, ref.task_id), "tool_scope_block"\n')
API_ANCHOR = "    def _perform_api_call(next_api_kwargs):\n"
API_HOOK = (API_ANCHOR +
            "        from hermes_cli.managed_model_catalog import apply_task_headers\n"
            "        next_api_kwargs = apply_task_headers(agent, effective_task_id, next_api_kwargs)\n")


def render(relative, source):
    if relative == "agent/turn_facade.py":
        return render_agent(source, expected_hash=HASHES[relative])
    if relative == "hermes_cli/runtime_provider.py":
        original = source.replace(PROVIDER_HOOK, "", 1)
        before, after = PROVIDER_ANCHOR, PROVIDER_ANCHOR + PROVIDER_HOOK
    elif relative == "agent/tool_executor.py":
        original = source.replace(TOOL_HOOK, TOOL_ANCHOR, 1)
        before, after = TOOL_ANCHOR, TOOL_HOOK
    elif relative == "agent/turn_api_call.py":
        original = source.replace(API_HOOK, API_ANCHOR, 1)
        before, after = API_ANCHOR, API_HOOK
    else:
        raise ValueError("Unsupported native patch target")
    if hashlib.sha256(original.encode()).hexdigest() != HASHES[relative]:
        raise ValueError("Managed model patch requires exact locked v0.22.5 source: " + relative)
    if original.count(before) != 1:
        raise ValueError("Managed model patch anchor changed: " + relative)
    result = original.replace(before, after, 1)
    ast.parse(result)
    return result


def apply(root):
    results = {}
    for relative in HASHES:
        path = root / relative
        if path.is_symlink() or not path.is_file():
            raise ValueError("Native patch target must be a regular file")
        results[relative] = render(relative, path.read_text(encoding="utf-8"))
    helper = Path(__file__).with_name("managed_model_catalog.py")
    ast.parse(helper.read_text(encoding="utf-8"))
    for relative, result in results.items():
        (root / relative).write_text(result, encoding="utf-8", newline="\n")
    shutil.copyfile(helper, root / "hermes_cli/managed_model_catalog.py")


if __name__ == "__main__":
    apply(Path(sys.argv[1]))
