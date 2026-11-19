"""Exercise managed hooks against the immutable Lite native sources."""
import ast
import contextlib
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import types
import unittest
from unittest.mock import Mock, patch

ROOT = Path(__file__).resolve().parents[1]
PATCHES = ROOT / "patches/hermes-agent"
sys.path.insert(0, str(PATCHES))
import apply_managed_model_catalog_v0225 as installer
import apply_lite_tool_executor as tools
import managed_model_catalog as catalog
NATIVE = Path(os.environ["HERMES_LITE_NATIVE_ROOT"]) if os.environ.get("HERMES_LITE_NATIVE_ROOT") else None


def function(source, name):
    return next(n for n in ast.walk(ast.parse(source)) if isinstance(n, ast.FunctionDef) and n.name == name)


def load(node, namespace):
    module = ast.Module(body=[ast.ImportFrom(module="__future__", names=[ast.alias(name="annotations")], level=0), copy.deepcopy(node)], type_ignores=[])
    ast.fix_missing_locations(module)
    exec(compile(module, "pinned-native-hook", "exec"), namespace)
    return namespace[node.name]


@unittest.skipUnless(NATIVE, "set HERMES_LITE_NATIVE_ROOT to verified v0.22.5 source")
class NativeHooks(unittest.TestCase):
    def source(self, name):
        return (NATIVE / name).read_text(encoding="utf-8")

    def test_exact_sources_idempotence_and_unknown_drift_rejection(self):
        for name, digest in installer.HASHES.items():
            with self.subTest(name=name):
                source = self.source(name)
                self.assertEqual(hashlib.sha256(source.encode()).hexdigest(), digest)
                rendered = installer.render(name, source)
                self.assertEqual(installer.render(name, rendered), rendered)
                with self.assertRaises(ValueError):
                    installer.render(name, source + "\n# unreviewed source\n")
        rendered = installer.render("agent/tool_executor.py", self.source("agent/tool_executor.py"))
        tools.patched_source(rendered.encode())

    def test_native_disabled_guard_runs_before_managed_resolution(self):
        node = function(installer.render("hermes_cli/runtime_provider.py", self.source("hermes_cli/runtime_provider.py")), "resolve_runtime_provider")
        disabled = Mock()
        namespace = {"resolve_requested_provider": lambda requested: requested or "auto", "_raise_if_provider_disabled": disabled,
                     "_get_named_custom_provider": Mock(), "auth_mod": types.SimpleNamespace(resolve_provider=Mock())}
        resolve = load(node, namespace)
        package = types.ModuleType("hermes_cli")
        with patch.dict(sys.modules, {"hermes_cli": package, "hermes_cli.managed_model_catalog": catalog}):
            with patch.object(catalog, "resolve_managed_runtime", return_value={"provider": "managed"}) as managed:
                self.assertEqual(resolve(requested="clawmanager-old", target_model="old"), {"provider": "managed"})
                disabled.assert_called_once_with("clawmanager-old")
                self.assertEqual(managed.call_args.kwargs["requested_provider"], "clawmanager-old")
                disabled.side_effect = ValueError("native provider disabled")
                managed.reset_mock()
                with self.assertRaisesRegex(ValueError, "disabled"):
                    resolve(requested="clawmanager-old")
                managed.assert_not_called()

    def test_actual_shared_dispatch_rechecks_before_plugin_or_tool(self):
        node = function(installer.render("agent/tool_executor.py", self.source("agent/tool_executor.py")), "_dispatch_authorized_once")
        blocked = Mock(return_value="blocked")
        plugin = Mock(side_effect=AssertionError("plugin must not run after revocation"))
        namespace = {"_blocked_tool_result": blocked, "_pre_tool_block": plugin}
        dispatch = load(node, namespace)
        agent = types.SimpleNamespace()
        ref = types.SimpleNamespace(task_id="native-task", args={}, name="write_file")
        package = types.ModuleType("hermes_cli")
        with patch.dict(sys.modules, {"hermes_cli": package, "hermes_cli.managed_model_catalog": catalog}):
            with patch.object(catalog, "managed_tool_block", return_value="revoked") as check:
                for _ in range(2):
                    state = types.SimpleNamespace(blocked=False, args={})
                    execute = Mock(side_effect=AssertionError("revoked tool dispatched"))
                    advance = Mock()
                    self.assertEqual(dispatch(agent, state, ref, execute=execute, scope_block=None, display_index=None,
                                              begin_execution=advance, authorization_gate=None), "blocked")
                    self.assertTrue(state.blocked)
                    execute.assert_not_called()
                    advance.assert_called_once_with(None)
                self.assertEqual(check.call_count, 2)
                check.assert_called_with(agent, "native-task")
                plugin.assert_not_called()

    def test_real_api_callback_uses_exact_native_task_and_preserves_input(self):
        node = function(installer.render("agent/turn_api_call.py", self.source("agent/turn_api_call.py")), "_perform_api_call")
        agent = types.SimpleNamespace(api_mode="chat_completions", session_id="native-session", provider="custom", model="opaque",
                                      _interruptible_api_call=lambda kwargs: kwargs)
        namespace = {"agent": agent, "effective_task_id": "native-task", "_use_streaming": False, "api_request_id": "request-id", "retry_count": 0}
        call = load(node, namespace)
        relay = types.ModuleType("agent")
        relay.relay_llm = types.SimpleNamespace(execute=lambda kwargs, callback, **options: callback(kwargs))
        package = types.ModuleType("hermes_cli")
        original = {"messages": ["keep"]}
        with patch.dict(sys.modules, {"agent": relay, "hermes_cli": package, "hermes_cli.managed_model_catalog": catalog}):
            with patch.object(catalog, "apply_task_headers", return_value={**original, "extra_headers": {"X-ClawManager-Task-ID": "native-task"}}) as headers:
                result = call(original)
                self.assertEqual(result["extra_headers"]["X-ClawManager-Task-ID"], "native-task")
                headers.assert_called_once_with(agent, "native-task", original)
                self.assertEqual(original, {"messages": ["keep"]})

    def test_native_turn_monitor_joins_on_success_and_failure(self):
        rendered = installer.render("agent/turn_facade.py", self.source("agent/turn_facade.py"))
        node = next(n for n in ast.walk(ast.parse(rendered)) if isinstance(n, ast.With) and
                    any(isinstance(i.context_expr, ast.Call) and isinstance(i.context_expr.func, ast.Name) and
                        i.context_expr.func.id == "monitor_turn" for i in n.items))
        for fail in [False, True]:
            events = []
            agent = types.SimpleNamespace()
            @contextlib.contextmanager
            def monitor(owner, task):
                self.assertIs(owner, agent)
                self.assertEqual(task, "native-task")
                events.append("start")
                try:
                    yield
                finally:
                    events.append("joined")
            def run(*args, **kwargs):
                events.append("native")
                if fail:
                    raise RuntimeError("native failure")
                return "result"
            names = {n.id: None for n in ast.walk(node) if isinstance(n, ast.Name) and isinstance(n.ctx, ast.Load)}
            names.update(self=agent, effective_task_id="native-task", monitor_turn=monitor, run_conversation=run)
            module = ast.fix_missing_locations(ast.Module(body=[copy.deepcopy(node)], type_ignores=[]))
            if fail:
                with self.assertRaisesRegex(RuntimeError, "native failure"):
                    exec(compile(module, "native-turn-scope", "exec"), names)
            else:
                exec(compile(module, "native-turn-scope", "exec"), names)
                self.assertEqual(names["result"], "result")
            self.assertEqual(events, ["start", "native", "joined"])

    def test_complete_lite_patch_chain(self):
        lock = json.loads((ROOT / "hermes-lite.lock.json").read_text())
        extras = {"gateway/run.py", "gateway/run_adapters.py", "plugins/model-providers/custom/__init__.py"}
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary)
            for relative in set(lock["source_sha256"]) | set(installer.HASHES) | extras:
                file = target / relative
                file.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(NATIVE / relative, file)
            patches = [
                ("apply_lite_gateway_boundary.py", target),
                ("apply_managed_model_catalog_v0225.py", target),
                ("apply_lite_non_native.py", target),
                ("apply_lite_tool_executor.py", target),
                ("apply_lite_startup.py", target),
                ("apply_lite_model_catalog.py", target),
                ("apply_lite_picker_budget.py", target),
                ("apply_lite_models_metadata.py", target),
                ("apply_lite_team_completion_stop.py", target / "agent/turn_tool_round.py"),
                ("apply_lite_skill_dedupe.py", target / "tools/skills_sync.py"),
                ("apply_team_context_reader.py", target / "gateway/run.py"),
                ("apply_lite_managed_reasoning.py", target),
            ]
            for script, argument in patches:
                result = subprocess.run([sys.executable, "-B", str(PATCHES / script), str(argument)], capture_output=True, text=True)
                self.assertEqual(result.returncode, 0, script + ": " + result.stderr)


if __name__ == "__main__":
    unittest.main()
