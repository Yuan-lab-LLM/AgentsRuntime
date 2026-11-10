"""Checks actual AIAgent native dispatch; called by the non-native image suite."""
import json
from types import SimpleNamespace
from unittest.mock import patch


def check_native_executor(agent):
    from run_agent import AIAgent
    from agent import tool_executor
    from hermes_cli import lite_non_native as policy

    assert isinstance(agent, AIAgent), "executor fixture requires real AIAgent"
    calls = []

    def forbidden(*args, **kwargs):
        calls.append("native callback")
        raise AssertionError("native callback executed")

    callbacks = (
        "read_terminal_callback", "read_preview_callback", "drive_preview_callback",
        "read_window_below_callback", "tour_callback", "setup_mcp_callback",
    )
    originals = {name: getattr(agent, name, None) for name in callbacks}
    try:
        for name in callbacks:
            setattr(agent, name, forbidden)
        # Exercise the real inline branches, not just the registry wrapper.
        # Even an old dispatch table that still lists native tools is rejected.
        old_names = set(agent.valid_tool_names)
        agent.valid_tool_names.update(policy.NATIVE_TOOLS)
        try:
            for name in sorted(policy.NATIVE_TOOLS):
                message = SimpleNamespace(tool_calls=[SimpleNamespace(
                    id="native-fixture-" + name,
                    function=SimpleNamespace(name=name, arguments=json.dumps({"private": "do-not-reflect"})),
                )])
                messages = []
                tool_executor.execute_tool_calls_sequential(agent, message, messages, "executor-fixture", finalize=False)
                assert len(messages) == 1, "native tool omitted its result"
                assert json.loads(messages[0]["content"]) == {"error": "unsupported_native_desktop_tool"}, "inline native tool was not rejected"
                assert "do-not-reflect" not in json.dumps(messages), "native error reflected arguments"

            concurrent = SimpleNamespace(tool_calls=[SimpleNamespace(
                id="concurrent-native-" + name,
                function=SimpleNamespace(name=name, arguments="{}"),
            ) for name in sorted(policy.NATIVE_TOOLS)])
            results = []
            tool_executor.execute_tool_calls_concurrent(agent, concurrent, results, "executor-fixture", finalize=False)
            assert len(results) == len(policy.NATIVE_TOOLS), "concurrent native calls did not all complete"
            assert all(json.loads(result["content"]) == {"error": "unsupported_native_desktop_tool"} for result in results)

            # v0.22.5 can defer GUI tools. Exercise the real parser and executor:
            # both known aliases and removed legacy names must remain blocked.
            from tools import tool_search
            alias_checks = 0
            agent.valid_tool_names.add(tool_search.TOOL_CALL_NAME)
            for name in sorted(policy.NATIVE_TOOLS):
                # Each negative case is an independent turn, not 13 deliberate
                # repeated failures that should trigger the upstream loop guard.
                agent._tool_guardrails.reset_for_turn()
                args = {"name": name, "arguments": {"action": "capture"} if name == "computer_use" else {}}
                underlying, _, error = tool_search.resolve_underlying_call(args)
                assert underlying == name or (underlying is None and error), "unexpected native alias resolution"
                message = SimpleNamespace(tool_calls=[SimpleNamespace(
                    id="alias-native-" + name,
                    function=SimpleNamespace(name=tool_search.TOOL_CALL_NAME, arguments=json.dumps(args)),
                )])
                results = []
                tool_executor.execute_tool_calls_sequential(agent, message, results, "executor-fixture", finalize=False)
                assert len(results) == 1 and json.loads(results[0]["content"]) == {"error": "unsupported_native_desktop_tool"}, "native alias escaped the real parser"
                alias_checks += 1
            assert alias_checks == len(policy.NATIVE_TOOLS)
            concurrent_aliases = SimpleNamespace(tool_calls=[SimpleNamespace(
                id="concurrent-alias-" + name,
                function=SimpleNamespace(name=tool_search.TOOL_CALL_NAME, arguments=json.dumps({"name": name, "arguments": {}})),
            ) for name in sorted(policy.NATIVE_TOOLS)])
            results = []
            tool_executor.execute_tool_calls_concurrent(agent, concurrent_aliases, results, "executor-fixture", finalize=False)
            assert len(results) == len(policy.NATIVE_TOOLS), "concurrent native aliases did not complete"
            assert all(json.loads(result["content"]) == {"error": "unsupported_native_desktop_tool"} for result in results)
        finally:
            agent.valid_tool_names = old_names

        # Both sequential and concurrent scheduling funnel through this function.
        # A blocked native must release ordering without touching Relay/plugins.
        from agent import relay_tools
        ordering = []
        with patch.object(relay_tools, "execute", side_effect=forbidden):
            for name in sorted(policy.NATIVE_TOOLS):
                result = tool_executor._run_agent_tool_execution_middleware(
                    agent, function_name=name, function_args={"private": "do-not-reflect"},
                    effective_task_id="executor-fixture", tool_call_id="direct-" + name,
                    execute=forbidden, begin_execution=lambda: ordering.append(True),
                )
                assert result.blocked and not result.dispatched and result.args == {}, "shared entry did not fail closed"
                assert json.loads(result.result) == {"error": "unsupported_native_desktop_tool"}
        assert len(ordering) == len(policy.NATIVE_TOOLS), "native rejection stranded concurrent order"
        assert calls == [], "native execution reached a renderer or Relay callback"

        # Server-side tools continue through the real shared execution pipeline.
        dispatched = []
        for name in ("terminal", "clarify"):
            result = tool_executor._run_agent_tool_execution_middleware(
                agent, function_name=name, function_args={"controlled": True},
                effective_task_id="executor-fixture", tool_call_id="allowed-" + name,
                execute=lambda args, name=name: dispatched.append(name) or "allowed",
            )
            assert result.result == "allowed" and result.dispatched and not result.blocked
        assert dispatched == ["terminal", "clarify"], "server-side tools were blocked"
        return {"inline_native_rejections": len(policy.NATIVE_TOOLS),
                "concurrent_native_rejections": len(policy.NATIVE_TOOLS),
                "native_alias_rejections": alias_checks,
                "shared_native_rejections": len(policy.NATIVE_TOOLS),
                "native_callbacks": len(calls), "server_dispatch": dispatched}
    finally:
        for name, value in originals.items():
            setattr(agent, name, value)
