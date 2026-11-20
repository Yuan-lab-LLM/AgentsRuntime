#!/usr/bin/env python3
"""Guard native tools at the real shared sequential/concurrent execution entry."""
import hashlib
from pathlib import Path
import sys

TARGET = "agent/tool_executor.py"
EXPECTED = "1b1b67d4b88f73c51dc147d0e0383b36c561d921d278d801326de5ba24b29702"
ANCHOR = '    """Run Relay rewrites before Hermes policy and dispatch exactly once."""\n'
GUARD = '''    from hermes_cli import lite_non_native as _lite_non_native
    if _lite_non_native.is_native_call(function_name, function_args):
        # The inline desktop branches bypass model_tools.handle_function_call.
        # Reject before Relay, plugins, callbacks, or argument inspection. Advance
        # only the internal concurrent ordering baton so later tools can proceed.
        if begin_execution is not None:
            begin_execution()
        return _ManagedToolResult(
            result='{"error": "unsupported_native_desktop_tool"}',
            args={}, middleware_trace=[], blocked=True, dispatched=False,
        )
'''


def patched_source(content):
    # The shared catalog installer runs first for Pro and Lite. Preserve its
    # fence while verifying the exact original pinned source.
    TOOL_ANCHOR = "        block_message = scope_block\n"
    TOOL_HOOK = ("        from hermes_cli.managed_model_catalog import managed_tool_block\n"
                 "        block_message = scope_block or managed_tool_block(agent, effective_task_id)\n")
    source = content.decode("utf-8")
    original = source.replace(TOOL_HOOK, TOOL_ANCHOR, 1)
    from apply_managed_model_catalog_v0225 import TOOL_HOOK as V0225_HOOK, TOOL_ANCHOR as V0225_ANCHOR
    original = original.replace(V0225_HOOK, V0225_ANCHOR, 1)
    if hashlib.sha256(original.encode("utf-8")).hexdigest() != EXPECTED:
        raise ValueError("Lite executor patch requires exact locked upstream source")
    if source.count(ANCHOR) != 1:
        raise ValueError("Lite executor patch anchor changed")
    result = source.replace(ANCHOR, ANCHOR + GUARD)
    compile(result, TARGET, "exec")
    return result


def apply(root):
    target = root / TARGET
    result = patched_source(target.read_bytes())
    target.write_text(result, encoding="utf-8", newline="\n")


if __name__ == "__main__":
    apply(Path(sys.argv[1]))
