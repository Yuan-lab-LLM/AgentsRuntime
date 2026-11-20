#!/usr/bin/env python3
"""Offline adapter/installer contracts; optional --native-source verifies lock."""
import ast
import hashlib
import json
import os
from pathlib import Path
import sys
import types
import threading
import unittest
from unittest.mock import Mock, MagicMock, patch

ROOT = Path(__file__).resolve().parents[1] / "patches/hermes-agent"
sys.path.insert(0, str(ROOT))
import managed_model_catalog as catalog
import apply_managed_model_catalog as installer

NATIVE = None
NATIVE_TOOL = None
if "--native-tool-source" in sys.argv:
    index = sys.argv.index("--native-tool-source")
    NATIVE_TOOL = Path(sys.argv[index + 1])
    del sys.argv[index:index + 2]
if "--native-source" in sys.argv:
    index = sys.argv.index("--native-source")
    NATIVE = Path(sys.argv[index + 1])
    del sys.argv[index:index + 2]

MODEL = {"id": "new-name", "stable_id": "cm-deployment-1", "aliases": ["old name", "old/namespace/model"],
         "provider_refs": [{"provider_id": "old name", "model_id": "old name"}]}
AUTO = {"id": "auto", "provider_refs": [{"provider_id": "auto", "model_id": "auto"}]}
ENV = {catalog.PROTOCOL: "1", "CLAWMANAGER_LLM_BASE_URL": "https://gateway.test/llm",
       "CLAWMANAGER_LLM_API_KEY": "instance-test-token"}


class ResolutionTests(unittest.TestCase):
    def setUp(self):
        self.env = patch.dict(os.environ, ENV, clear=True)
        self.env.start()
        self.addCleanup(self.env.stop)
        self.fetch = patch.object(catalog, "load_catalog", return_value=[AUTO, MODEL]).start()
        self.addCleanup(patch.stopall)

    def resolve(self, provider="clawmanager-old-name", model="old name", custom=None, **options):
        return catalog.resolve_managed_runtime(requested_provider=provider, target_model=model,
            explicit_base_url=options.get("base"), explicit_api_key=options.get("key"),
            lookup_custom=lambda _: custom, resolve_builtin=lambda _: options.get("builtin"))

    def test_historical_provider_and_alias_keep_same_wire_model(self):
        for provider in ("old name", "clawmanager-old name", "clawmanager-old-name", "custom:clawmanager-old-name"):
            with self.subTest(provider=provider):
                result = self.resolve(provider)
                self.assertEqual(result["base_url"], ENV["CLAWMANAGER_LLM_BASE_URL"])
                self.assertEqual(result["api_key"], ENV["CLAWMANAGER_LLM_API_KEY"])
                self.assertNotIn("model", result)  # caller's saved wire ref remains unchanged
                self.assertNotIn("credential_pool", result)

    def test_canonical_accepts_stable_current_and_historical_refs(self):
        for model in ("new-name", "cm-deployment-1", "old name", "old/namespace/model", "auto"):
            self.assertIsNotNone(self.resolve("clawmanager", model))

    def test_recreated_display_name_cannot_restore_deleted_selection(self):
        self.fetch.return_value = [AUTO, {"id": "old name", "stable_id": "cm-deploy-v1-new", "legacy_name_unavailable": True}]
        with self.assertRaises(catalog.ManagedModelUnavailable):
            self.resolve("clawmanager", "old name")
        self.assertIsNotNone(self.resolve("clawmanager", "cm-deploy-v1-new"))

    def test_deleted_revoked_or_unknown_managed_reference_is_rejected(self):
        self.fetch.return_value = [AUTO]
        for provider in ("clawmanager", "clawmanager-old-name"):
            with self.assertRaisesRegex(catalog.ManagedModelUnavailable, "当前模型不可用"):
                self.resolve(provider)

    def test_cannot_guess_leaf_model_or_another_provider(self):
        for provider, model in (("clawmanager-wrong", "old name"), ("clawmanager", "model")):
            with self.assertRaises(catalog.ManagedModelUnavailable):
                self.resolve(provider, model)

    def test_ambiguous_wire_alias_is_rejected(self):
        self.fetch.return_value = [MODEL, {"id": "other", "stable_id": "cm-deployment-2", "aliases": ["old name"]}]
        with self.assertRaises(catalog.ManagedModelUnavailable):
            self.resolve()

    def test_foreign_custom_provider_takes_precedence_even_with_reserved_name(self):
        for custom in ({"base_url": "https://other.test", "api_key": ENV["CLAWMANAGER_LLM_API_KEY"]},
                       {"base_url": ENV["CLAWMANAGER_LLM_BASE_URL"], "api_key": "other-key"}):
            self.assertIsNone(self.resolve("clawmanager", custom=custom))
        self.fetch.assert_not_called()

    def test_owned_custom_provider_refreshes_permissions(self):
        custom = {"base_url": ENV["CLAWMANAGER_LLM_BASE_URL"], "api_key": ENV["CLAWMANAGER_LLM_API_KEY"]}
        self.assertIsNotNone(self.resolve("custom:my-provider", custom=custom))
        self.fetch.return_value = []
        with self.assertRaises(catalog.ManagedModelUnavailable):
            self.resolve("custom:my-provider", custom=custom)
        self.assertEqual(self.fetch.call_count, 2)

    def test_existing_provider_protocol_and_limits_are_retained(self):
        custom = {"base_url": ENV["CLAWMANAGER_LLM_BASE_URL"], "api_key": ENV["CLAWMANAGER_LLM_API_KEY"],
                  "max_output_tokens": 4096, "capabilities": {"vision": True},
                  "api_mode": "anthropic_messages", "extra_body": {"temperature": 0.1}}
        result = self.resolve("clawmanager", custom=custom)
        for field in ("max_output_tokens", "capabilities", "api_mode"):
            self.assertEqual(result[field], custom[field])
        self.assertEqual(result["request_overrides"], {"extra_body": custom["extra_body"]})

    def test_builtin_explicit_foreign_and_disabled_protocol_are_untouched(self):
        self.assertIsNone(self.resolve("openai", builtin="openai"))
        self.assertIsNone(self.resolve(base="https://foreign.test"))
        self.assertIsNone(self.resolve(key="another-key"))
        self.assertIsNone(self.resolve(model=None))
        os.environ.pop(catalog.PROTOCOL)
        self.assertIsNone(self.resolve())
        self.fetch.assert_not_called()

    def test_catalog_outage_never_falls_back(self):
        self.fetch.side_effect = catalog.ManagedCatalogUnavailable("unavailable")
        with self.assertRaises(catalog.ManagedCatalogUnavailable):
            self.resolve()

    def test_exact_provider_cannot_use_a_wire_alias_for_another_deployment(self):
        self.fetch.return_value = [{"id": "new-name", "stable_id": "one", "provider_refs": MODEL["provider_refs"]},
                                  {"id": "old name", "stable_id": "two"}]
        with self.assertRaises(catalog.ManagedModelUnavailable):
            self.resolve()


class TransportTests(unittest.TestCase):
    def fetch(self, raw):
        response = Mock()
        response.__enter__ = Mock(return_value=response)
        response.__exit__ = Mock(return_value=False)
        response.read.return_value = raw
        with patch.object(catalog.urllib.request, "build_opener") as build:
            build.return_value.open.return_value = response
            result = catalog.load_catalog(ENV["CLAWMANAGER_LLM_BASE_URL"], ENV["CLAWMANAGER_LLM_API_KEY"])
            args, kwargs = build.return_value.open.call_args
            self.assertEqual(kwargs["timeout"], 2.5)
            self.assertEqual(args[0].full_url, "https://gateway.test/llm/runtime/catalog")
            self.assertEqual(build.call_args.args[0].proxies, {})
            response.read.assert_called_once_with(catalog.MAX_BYTES + 1)
            return result

    def test_valid_catalog(self):
        self.assertEqual(self.fetch(json.dumps({"models": [AUTO, MODEL]}).encode()), [AUTO, MODEL])

    def test_bad_catalog_is_not_an_empty_success(self):
        for payload in ({}, {"models": {}}, {"models": [{"id": "old"}]},
                        {"models": [{**MODEL, "aliases": "not-a-list"}]},
                        {"models": [{**MODEL, "provider_refs": [{}]}]}):
            with self.subTest(payload=payload), self.assertRaises(catalog.ManagedCatalogUnavailable):
                self.fetch(json.dumps(payload).encode())
        for raw in (b"not json secret", b"x" * (catalog.MAX_BYTES + 1)):
            with self.assertRaises(catalog.ManagedCatalogUnavailable):
                self.fetch(raw)

    def test_transport_errors_do_not_leak_secrets_and_redirects_are_refused(self):
        with patch.object(catalog.urllib.request, "build_opener", side_effect=TimeoutError("secret endpoint and key")):
            with self.assertRaises(catalog.ManagedCatalogUnavailable) as raised:
                catalog.load_catalog(ENV["CLAWMANAGER_LLM_BASE_URL"], "private")
            self.assertNotIn("secret", str(raised.exception))
            self.assertNotIn("private", str(raised.exception))
        self.assertIsNone(catalog._NoRedirect().redirect_request(None, None, 302, "", {}, "https://evil.test"))


FIXTURE = '''def resolve_runtime_provider(*, requested=None, target_model=None, explicit_base_url=None, explicit_api_key=None):
    requested_provider = resolve_requested_provider(requested)
    # Azure Anthropic short-circuit: when explicitly targeting an Azure endpoint
    return {"provider": "native"}
'''


class ToolFenceTests(unittest.TestCase):
    def setUp(self):
        self.env = patch.dict(os.environ, ENV, clear=True)
        self.env.start()
        self.addCleanup(self.env.stop)
        self.fetch = patch.object(catalog, "load_catalog", return_value=[AUTO, MODEL]).start()
        self.addCleanup(patch.stopall)
        self.agent = types.SimpleNamespace(base_url=ENV["CLAWMANAGER_LLM_BASE_URL"],
            api_key=ENV["CLAWMANAGER_LLM_API_KEY"], model="old name", interrupt=Mock())

    def test_allowed_alias_and_permission_refresh_before_every_tool(self):
        self.assertIsNone(catalog.managed_tool_block(self.agent))
        self.fetch.return_value = [AUTO]
        self.assertIn("当前模型不可用", catalog.managed_tool_block(self.agent))
        self.assertEqual(self.fetch.call_count, 2)
        self.agent.interrupt.assert_called_once()
        self.assertTrue(self.agent.interrupt.call_args.kwargs["hard_cancel"])

    def test_force_stop_is_opt_in_team_excluded_latched_and_bounded(self):
        with patch.object(catalog, "_force_stop_pending", threading.Event()):
            with patch.object(catalog.threading, "Thread") as thread:
                catalog._arm_force_stop()
                thread.assert_not_called()
                with patch.dict(os.environ, {"CLAWMANAGER_HERMES_FORCE_STOP_ON_REVOKE": "1",
                                             "CLAWMANAGER_TEAM_ENABLED": "true"}):
                    catalog._arm_force_stop()
                thread.assert_not_called()
                with patch.dict(os.environ, {"CLAWMANAGER_HERMES_FORCE_STOP_ON_REVOKE": "1"}):
                    catalog._arm_force_stop()
                    catalog._arm_force_stop()
                thread.assert_called_once()
                self.assertTrue(catalog._force_stop_pending.is_set())
                target = thread.call_args.kwargs["target"]
                waiter = Mock()
                with patch.object(catalog.threading, "Event", return_value=waiter), patch.object(catalog, "_terminate_runtime") as terminate:
                    target()
                    waiter.wait.assert_called_once_with(10)
                    terminate.assert_called_once_with()
                self.agent.session_id = "session-1"
                with patch.dict(os.environ, {"CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL": "1"}):
                    with self.assertRaisesRegex(catalog.ManagedCatalogUnavailable, "正在因模型撤权停止"):
                        catalog.task_headers(self.agent, "next-turn")

    def test_only_authenticated_revocation_marker_is_a_revoked_task(self):
        import urllib.error
        self.agent.session_id = "session-1"
        with patch.dict(os.environ, {"CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL": "1",
                                     "CLAWMANAGER_GATEWAY_GENERATION": "3"}):
            for status, headers, expected in [
                (403, {"X-ClawManager-Task-State": "revoked"}, catalog.ManagedTaskRevoked),
                (403, {}, catalog.ManagedCatalogUnavailable),
                (503, {"X-ClawManager-Task-State": "revoked"}, catalog.ManagedCatalogUnavailable),
            ]:
                opener = Mock()
                opener.open.side_effect = urllib.error.HTTPError("https://gateway.test", status, "private", headers, None)
                with patch.object(catalog.urllib.request, "build_opener", return_value=opener):
                    with self.assertRaises(expected) as raised:
                        catalog.check_task(self.agent, "turn-1")
                    self.assertIs(type(raised.exception), expected)
                    self.assertNotIn("private", str(raised.exception))

    def test_pro_fail_stop_requires_matching_private_session_and_group(self):
        for raw, group, session, should_kill in [
            ("500",500,500,True), ("500",501,500,False),
            ("500",500,501,False), ("1",1,1,False), ("",500,500,False),
        ]:
            with patch.dict(os.environ, {"CLAWMANAGER_HERMES_FAIL_STOP_PGID":raw}), \
                 patch.object(catalog.os, "getpgrp", return_value=group, create=True), \
                 patch.object(catalog.os, "getsid", return_value=session, create=True), \
                 patch.object(catalog.os, "killpg", create=True) as kill, \
                 patch.object(catalog.os, "_exit") as exit_process:
                catalog._terminate_runtime()
                self.assertEqual(kill.call_count, int(should_kill))
                exit_process.assert_called_once_with(78)

    def test_monitor_arms_force_stop_only_for_confirmed_revocation(self):
        self.agent.session_id = "session-1"
        with patch.dict(os.environ, {"CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL": "1",
                                     "CLAWMANAGER_GATEWAY_GENERATION": "3"}):
            for error, expected in [(catalog.ManagedTaskRevoked("revoked"), 1),
                                    (catalog.ManagedCatalogUnavailable("timeout"), 0)]:
                interrupted = threading.Event()
                self.agent.interrupt.side_effect = lambda *args, **kwargs: interrupted.set()
                with patch.object(catalog, "check_task", side_effect=[None, error]), patch.object(catalog, "_arm_force_stop") as arm:
                    with catalog.monitor_turn(self.agent, "turn-1"):
                        self.assertTrue(interrupted.wait(3))
                    self.assertEqual(arm.call_count, expected)

    def test_monitor_interrupts_active_turn_and_joins_before_return(self):
        self.agent.session_id = "session-1"
        interrupted = threading.Event()
        self.agent.interrupt.side_effect = lambda *args, **kwargs: interrupted.set()
        with patch.dict(os.environ, {"CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL": "1",
                                     "CLAWMANAGER_GATEWAY_GENERATION": "3"}):
            with patch.object(catalog, "check_task", side_effect=[None, catalog.ManagedCatalogUnavailable("revoked")]) as check:
                with catalog.monitor_turn(self.agent, "turn-1"):
                    self.assertTrue(interrupted.wait(3), "active turn was not interrupted")
                self.assertEqual(check.call_count, 2)
        self.assertFalse(any(t.name == "clawmanager-model-access" for t in threading.enumerate()))
        self.assertTrue(self.agent.interrupt.call_args.kwargs["hard_cancel"])

    def test_monitor_initial_failure_never_starts_turn(self):
        self.agent.session_id = "session-1"
        with patch.dict(os.environ, {"CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL": "1",
                                     "CLAWMANAGER_GATEWAY_GENERATION": "3"}):
            with patch.object(catalog, "check_task", side_effect=catalog.ManagedCatalogUnavailable("unavailable")):
                with self.assertRaises(catalog.ManagedCatalogUnavailable):
                    with catalog.monitor_turn(self.agent, "turn-1"):
                        self.fail("uncertain turn started")

    def test_disabled_monitor_does_not_query_or_interrupt(self):
        with patch.object(catalog, "check_task") as check:
            with catalog.monitor_turn(self.agent, "turn-1"):
                pass
            check.assert_not_called()
        self.agent.interrupt.assert_not_called()

    def test_normal_return_rechecks_before_ordinary_finish(self):
        self.agent.session_id = "session-1"
        with patch.dict(os.environ, {"CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL": "1",
                                     "CLAWMANAGER_GATEWAY_GENERATION": "3"}):
            calls = []
            with patch.object(catalog, "check_task", side_effect=lambda *args: calls.append("check")), patch.object(catalog, "finish_task", side_effect=lambda *args: calls.append("finish")):
                with catalog.monitor_turn(self.agent, "turn-1"):
                    calls.append("body")
            self.assertEqual(calls, ["check", "body", "check", "finish"])

    def test_exception_or_last_moment_revocation_never_finishes(self):
        self.agent.session_id = "session-1"
        with patch.dict(os.environ, {"CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL": "1",
                                     "CLAWMANAGER_GATEWAY_GENERATION": "3"}):
            with patch.object(catalog, "check_task"), patch.object(catalog, "finish_task") as finish:
                with self.assertRaisesRegex(RuntimeError, "operation failed"):
                    with catalog.monitor_turn(self.agent, "turn-1"):
                        raise RuntimeError("operation failed")
                finish.assert_not_called()
            with patch.object(catalog, "check_task", side_effect=[None, catalog.ManagedCatalogUnavailable("revoked")]), patch.object(catalog, "finish_task") as finish:
                with self.assertRaises(catalog.ManagedCatalogUnavailable):
                    with catalog.monitor_turn(self.agent, "turn-1"):
                        pass
                finish.assert_not_called()

    def test_finish_never_sends_cancellation_acknowledgement(self):
        self.agent.session_id = "session-1"
        with patch.dict(os.environ, {"CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL": "1",
                                     "CLAWMANAGER_GATEWAY_GENERATION": "3"}):
            opener = MagicMock()
            opener.open.return_value.__enter__.return_value.status = 200
            with patch.object(catalog.urllib.request, "build_opener", return_value=opener):
                catalog.finish_task(self.agent, "turn-1")
                request = opener.open.call_args.args[0]
                self.assertTrue(request.full_url.endswith("/runtime/tasks/finish"))
                self.assertIs(json.loads(request.data)["cancelled"], False)

    def test_turn_monitor_pins_owner_scope_across_native_provider_changes(self):
        self.agent.session_id = "original-session"
        with patch.dict(os.environ, {"CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL": "1",
                                     "CLAWMANAGER_GATEWAY_GENERATION": "3"}):
            opener = MagicMock()
            opener.open.return_value.__enter__.return_value.status = 200
            with patch.object(catalog.urllib.request, "build_opener", return_value=opener):
                with catalog.monitor_turn(self.agent, "original-turn"):
                    self.agent.base_url = "https://foreign.test"
                    self.agent.api_key = "foreign-key"
                    self.agent.session_id = "next-session"
                    with patch.dict(os.environ, {"CLAWMANAGER_LLM_BASE_URL": "https://changed.test",
                                                 "CLAWMANAGER_LLM_API_KEY": "changed-key",
                                                 "CLAWMANAGER_GATEWAY_GENERATION": "4"}):
                        # Even a pending watcher uses the original immutable scope.
                        scope = catalog._BoundTask(ENV["CLAWMANAGER_LLM_BASE_URL"], ENV["CLAWMANAGER_LLM_API_KEY"],
                            "original-session", "original-turn", "3")
                        catalog.check_task(scope, "original-turn")
                        with self.assertRaises(catalog.ManagedCatalogUnavailable):
                            catalog.check_task(scope, "different-turn")
                for call in opener.open.call_args_list:
                    request = call.args[0]
                    self.assertTrue(request.full_url.startswith(ENV["CLAWMANAGER_LLM_BASE_URL"] + "/"))
                    self.assertEqual(request.get_header("Authorization"), "Bearer " + ENV["CLAWMANAGER_LLM_API_KEY"])
                    payload = json.loads(request.data)
                    self.assertEqual(payload["session_id"], "original-session")
                    self.assertEqual(payload["task_id"], "original-turn")
                    self.assertEqual(payload["generation"], 3)

    def test_native_task_headers_are_opt_in_and_cannot_be_spoofed(self):
        self.agent.session_id = "session-1"
        original = {"extra_headers": {"x-clawmanager-task-id": "spoof", "other": "kept"}}
        self.assertIs(catalog.apply_task_headers(self.agent, "turn-1", original), original)
        with patch.dict(os.environ, {"CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL": "1",
                                     "CLAWMANAGER_GATEWAY_GENERATION": "3"}):
            result = catalog.apply_task_headers(self.agent, "turn-1", original)
            self.assertEqual(result["extra_headers"]["X-ClawManager-Task-ID"], "turn-1")
            self.assertNotIn("x-clawmanager-task-id", result["extra_headers"])
            self.assertEqual(original["extra_headers"]["x-clawmanager-task-id"], "spoof")
            for task in (None, "", "bad\nidentity"):
                with self.assertRaises(catalog.ManagedCatalogUnavailable):
                    catalog.task_headers(self.agent, task)
            with patch.dict(os.environ, {"CLAWMANAGER_GATEWAY_GENERATION": ""}):
                with self.assertRaises(catalog.ManagedCatalogUnavailable):
                    catalog.task_headers(self.agent, "turn-1")

    def test_auto_checks_exact_task_before_tool_not_just_auto_catalog_row(self):
        self.agent.model = "auto"
        self.agent.session_id = "session-1"
        with patch.dict(os.environ, {"CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL": "1",
                                     "CLAWMANAGER_GATEWAY_GENERATION": "3"}):
            opener = MagicMock()
            response = opener.open.return_value.__enter__.return_value
            response.status = 200
            with patch.object(catalog.urllib.request, "build_opener", return_value=opener):
                self.assertIsNone(catalog.managed_tool_block(self.agent, "turn-1"))
                request = opener.open.call_args.args[0]
                self.assertEqual(json.loads(request.data), {"session_id": "session-1", "task_id": "turn-1", "generation": 3})
                opener.open.side_effect = TimeoutError("private")
                self.assertIsNotNone(catalog.managed_tool_block(self.agent, "turn-1"))
                self.agent.interrupt.assert_called_once()

    def test_catalog_failure_and_ambiguous_or_missing_identity_fail_closed(self):
        for items in ([], [MODEL, dict(MODEL)], [AUTO]):
            self.fetch.return_value = items
            self.assertIsNotNone(catalog.managed_tool_block(self.agent))
        self.fetch.side_effect = catalog.ManagedCatalogUnavailable("private upstream detail")
        message = catalog.managed_tool_block(self.agent)
        self.assertIn("授权目录暂不可用", message)
        self.assertNotIn("private", message)

    def test_native_interrupt_failure_still_blocks_dispatch(self):
        self.fetch.return_value = []
        self.agent.interrupt.side_effect = RuntimeError("native stop failed")
        self.assertIsNotNone(catalog.managed_tool_block(self.agent))

    def test_foreign_provider_legacy_and_team_are_untouched(self):
        for changes in ({"api_key": "foreign-key"}, {"base_url": "https://foreign.test"}):
            agent = types.SimpleNamespace(**{**vars(self.agent), **changes})
            self.assertIsNone(catalog.managed_tool_block(agent))
        for env in ({catalog.PROTOCOL: ""}, {"CLAWMANAGER_TEAM_ENABLED": "true"}):
            with patch.dict(os.environ, env):
                self.assertIsNone(catalog.managed_tool_block(self.agent))
        self.fetch.assert_not_called()
        self.agent.interrupt.assert_not_called()

    def test_auto_catalog_entry_is_not_claimed_as_resolved_model_authorization(self):
        self.agent.model = "auto"
        self.assertIsNone(catalog.managed_tool_block(self.agent))
        # This fence cannot identify the deployment chosen by Auto. The native
        # task identity/Bind protocol remains a separate required delivery item.
        self.agent.interrupt.assert_not_called()

    @unittest.skipUnless(NATIVE_TOOL, "provide --native-tool-source for pinned native dispatch")
    def test_pinned_native_dispatch_and_lite_patch_composition(self):
        import apply_lite_tool_executor as lite
        source = NATIVE_TOOL.read_text(encoding="utf-8")
        rendered = installer.render_tools(source)
        self.assertEqual(installer.render_tools(rendered), rendered)
        with self.assertRaises(ValueError):
            installer.render_tools(source + "\n# unknown change\n")
        package = types.ModuleType("hermes_cli")
        middleware = types.ModuleType("hermes_cli.middleware")
        middleware.apply_tool_request_middleware = lambda name, args, **kw: types.SimpleNamespace(payload=args, trace=[])
        middleware.run_tool_execution_middleware = lambda name, args, callback, **kw: callback(args)
        native_agent = types.ModuleType("agent")
        native_agent.relay_tools = types.SimpleNamespace(execute=lambda name, args, callback, **kw: (callback(args), args))
        variants = [rendered]
        if hashlib.sha256(source.encode()).hexdigest() == lite.EXPECTED:
            variants.append(lite.patched_source(rendered.encode()))
        for native in variants:
            tree = ast.parse(native)
            function = next(n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == "_run_agent_tool_execution_middleware")
            module = ast.Module(body=[ast.ImportFrom(module="__future__", names=[ast.alias(name="annotations")], level=0), function], type_ignores=[])
            ast.fix_missing_locations(module)
            namespace = {"threading": threading, "json": json, "_ManagedToolResult": lambda **kw: types.SimpleNamespace(**kw),
                         "_emit_terminal_post_tool_call": Mock()}
            package.lite_non_native = types.SimpleNamespace(NATIVE_TOOLS=set())
            with patch.dict(sys.modules, {"hermes_cli": package, "hermes_cli.managed_model_catalog": catalog,
                "hermes_cli.middleware": middleware, "agent": native_agent}):
                exec(compile(module, "pinned-tool-dispatch", "exec"), namespace)
                execute = Mock(side_effect=AssertionError("revoked tool must not execute"))
                begin = Mock()
                self.fetch.return_value = []
                result = namespace[function.name](self.agent, function_name="terminal",
                    function_args={"command": "private-command"}, effective_task_id="turn-1",
                    tool_call_id="tool-1", execute=execute, begin_execution=begin)
                self.assertTrue(result.blocked)
                execute.assert_not_called()
                begin.assert_called_once_with(None)
                self.assertIn("当前模型不可用", json.loads(result.result)["error"])
                self.assertNotIn("private-command", result.result)


class InstallerTests(unittest.TestCase):
    def test_unknown_source_is_refused(self):
        with self.assertRaisesRegex(ValueError, "exact locked"):
            installer.render(FIXTURE)

    def test_hook_invokes_adapter_and_reapply_is_idempotent(self):
        # Synthetic fixture verifies transform/behavior, not upstream acceptance.
        with patch.object(installer, "EXPECTED", hashlib.sha256(FIXTURE.encode()).hexdigest()):
            rendered = installer.render(FIXTURE)
            self.assertEqual(installer.render(rendered), rendered)
        namespace = {"resolve_requested_provider": lambda v: v, "_get_named_custom_provider": lambda v: None,
                     "auth_mod": types.SimpleNamespace(resolve_provider=lambda v: None), "os": os, "_provs_cfg": None}
        package = types.ModuleType("hermes_cli")
        with patch.dict(sys.modules, {"hermes_cli": package, "hermes_cli.managed_model_catalog": catalog}):
            exec(compile(rendered, "native-resolver-fixture", "exec"), namespace)
            with patch.object(catalog, "resolve_managed_runtime", return_value={"provider": "managed"}) as resolve:
                self.assertEqual(namespace["resolve_runtime_provider"](requested="old", target_model="old-model"), {"provider": "managed"})
                self.assertEqual(resolve.call_args.kwargs["target_model"], "old-model")
            with patch.object(catalog, "resolve_managed_runtime", return_value=None):
                self.assertEqual(namespace["resolve_runtime_provider"](), {"provider": "native"})
            with patch.object(catalog, "resolve_managed_runtime", side_effect=catalog.ManagedModelUnavailable("revoked")):
                with self.assertRaises(catalog.ManagedModelUnavailable):
                    namespace["resolve_runtime_provider"]()

    @unittest.skipUnless(NATIVE, "provide --native-source for locked upstream acceptance")
    def test_locked_upstream_source(self):
        source = NATIVE.read_text(encoding="utf-8")
        rendered = installer.render(source)
        self.assertEqual(installer.render(rendered), rendered)
        self.assertEqual(rendered.count(installer.HOOK), 1)
        compile(rendered, "locked-native-resolver", "exec")
        # Execute the actual pinned function with only its external dependencies
        # replaced. This checks real control flow, not just a matching string.
        tree = ast.parse(rendered)
        function = next(n for n in tree.body if isinstance(n, ast.FunctionDef) and n.name == "resolve_runtime_provider")
        module = ast.Module(body=[ast.ImportFrom(module="__future__", names=[ast.alias(name="annotations")], level=0), function], type_ignores=[])
        ast.fix_missing_locations(module)
        native_config = types.ModuleType("hermes_cli.config")
        native_config.load_config = lambda: {"providers": {"clawmanager": {"enabled": True}}}
        native_config.is_provider_enabled = lambda entry: entry.get("enabled", True)
        package = types.ModuleType("hermes_cli")
        namespace = {"resolve_requested_provider": lambda v: v, "_get_named_custom_provider": lambda v: None,
                     "auth_mod": types.SimpleNamespace(resolve_provider=lambda v: None), "os": os,
                     "_normalize_custom_provider_name": lambda v: v.strip().lower().replace(" ", "-"),
                     "custom_provider_aliases": lambda name, key: {key, "custom:" + key}}
        with patch.dict(sys.modules, {"hermes_cli": package, "hermes_cli.config": native_config,
                                    "hermes_cli.managed_model_catalog": catalog}), patch.dict(os.environ, ENV, clear=True):
            exec(compile(module, "pinned-native-resolver", "exec"), namespace)
            with patch.object(catalog, "load_catalog", return_value=[AUTO, MODEL]):
                result = namespace["resolve_runtime_provider"](requested="clawmanager-old-name", target_model="old name")
                self.assertEqual(result["source"], "clawmanager-runtime-catalog-v1")
            with patch.object(catalog, "load_catalog", return_value=[]):
                with self.assertRaises(catalog.ManagedModelUnavailable):
                    namespace["resolve_runtime_provider"](requested="clawmanager-old-name", target_model="old name")
            native_config.load_config = lambda: {"providers": {"clawmanager": {"enabled": False}}}
            with patch.object(catalog, "resolve_managed_runtime") as resolve:
                with self.assertRaisesRegex(ValueError, "disabled"):
                    namespace["resolve_runtime_provider"](requested="clawmanager", target_model="old name")
                resolve.assert_not_called()
                with self.assertRaisesRegex(ValueError, "disabled"):
                    namespace["resolve_runtime_provider"](requested="custom:clawmanager", target_model="old name")
                resolve.assert_not_called()
        # The existing Lite pool guard remains intact and accepted.
        before = "    pool_key = get_custom_provider_pool_key(base_url, provider_name=provider_name)"
        self.assertEqual(source.count(before), 1)
        lite = source.replace(before, installer.POOL_PREFIX + before)
        patched_lite = installer.render(lite)
        self.assertIn(installer.POOL_PREFIX, patched_lite)
        self.assertEqual(installer.render(patched_lite), patched_lite)


if __name__ == "__main__":
    unittest.main()
