import importlib.util
import ast
import asyncio
import io
import json
import os
from pathlib import Path
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch
from urllib.parse import urlencode

ROOT = Path(__file__).resolve().parents[1]
PATCHES = ROOT / "patches/hermes-agent"


def module(name, path, enabled="true"):
    with patch.dict(os.environ, {"CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED": enabled}):
        spec = importlib.util.spec_from_file_location(name, path)
        loaded = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(loaded)
        return loaded


policy = module("lite_non_native_test", PATCHES / "lite_non_native.py")
patcher = module("apply_lite_non_native_test", PATCHES / "apply_lite_non_native.py")


def agent(names=("terminal", "clarify"), selected=("terminal", "clarify")):
    return SimpleNamespace(tools=[{"type": "function", "function": {"name": name}} for name in names],
                           valid_tool_names=set(names), enabled_toolsets=list(selected))


class NonNativePolicyTests(unittest.TestCase):
    def test_tools_for_all_and_explicit_selection_exclude_native(self):
        offered = policy.NATIVE_TOOLS | {"terminal", "clarify", "read_file", "todo"}
        self.assertEqual({"terminal", "clarify", "read_file", "todo"}, policy.filter_tool_names(offered))
        selected = policy.toolset_filter(lambda: ["terminal", "clarify", "desktop_ui", "project", "computer_use"])
        self.assertEqual(["terminal", "clarify"], selected())
        self.assertEqual(set(), policy.toolset_filter(lambda: {"desktop_ui", "project"})())
        self.assertIsNone(policy.toolset_filter(lambda: None)())

    def test_native_calls_never_reach_execution_or_reflect_arguments(self):
        def forbidden(*args, **kwargs):
            self.fail("native tool reached upstream execution")
        guarded = policy.tool_dispatch(forbidden)
        for name in policy.NATIVE_TOOLS:
            with self.subTest(name=name):
                result = guarded(name, {"password": "private-do-not-reflect"})
                self.assertEqual({"error": "unsupported_native_desktop_tool"}, json.loads(result))
                self.assertNotIn("private", result)

    def test_terminal_and_clarify_keep_real_arguments(self):
        called = []
        guarded = policy.tool_dispatch(lambda name, args: called.append((name, args)) or "ok")
        for name in ("terminal", "clarify"):
            self.assertEqual("ok", guarded(name, {"value": "controlled"}))
        self.assertEqual([("terminal", {"value": "controlled"}), ("clarify", {"value": "controlled"})], called)

    def test_tool_dispatch_accepts_upstream_keyword_invocation(self):
        self.assertEqual({"error": "unsupported_native_desktop_tool"}, json.loads(
            policy.tool_dispatch(lambda **kwargs: self.fail("executed"))(function_name="read_terminal", function_args={})))

    def test_actual_schema_and_dispatch_names_must_both_be_safe(self):
        for kind in ("schema", "dispatch"):
            a = agent()
            if kind == "schema":
                a.tools.append({"function": {"name": "read_terminal"}})
            else:
                a.valid_tool_names.add("read_terminal")
            with self.subTest(kind=kind), self.assertRaisesRegex(policy.NonNativeSessionError, "incompatible_lite_live_session"):
                policy.seal_agent(a)

    def test_metadata_cannot_make_legacy_live_agent_safe(self):
        a = agent(names=("read_terminal",), selected=("desktop_ui",))
        history = [{"role": "user", "content": "preserve this history"}]
        record = {"agent": a, "source": "web", "running": True, "history": history, "transport": object()}
        before = dict(record)
        with self.assertRaises(policy.NonNativeSessionError):
            policy.require_compatible_session(record)
        self.assertEqual(before, record)
        self.assertIs(history, record["history"])
        self.assertEqual({"read_terminal"}, a.valid_tool_names)

    def test_even_empty_legacy_agent_requires_verified_construction(self):
        with self.assertRaises(policy.NonNativeSessionError):
            policy.require_compatible_session({"agent": agent(), "source": "web"})

    def test_safe_warm_reconnect_preserves_agent_and_history(self):
        a = policy.seal_agent(agent())
        record = {"agent": a, "source": "desktop", "running": True, "history": ["history"]}
        policy.require_compatible_session(record)
        self.assertIs(a, record["agent"])
        self.assertEqual("desktop", record["source"])
        self.assertTrue(record["running"])
        self.assertEqual(["history"], record["history"])

    def test_late_registration_invalidates_warm_reuse(self):
        a = policy.seal_agent(agent())
        a.valid_tool_names.add("desktop_preview")
        with self.assertRaises(policy.NonNativeSessionError):
            policy.require_compatible_session({"agent": a})

    def test_deferred_cold_resume_does_not_require_metadata_rewrite(self):
        record = {"agent": None, "source": "desktop", "history": ["preserved"]}
        policy.require_compatible_session(record)
        self.assertEqual("desktop", record["source"])

    def test_concurrent_winners_and_parked_records_are_checked(self):
        unsafe = {"session_key": "stored", "agent": agent(("read_terminal",), ("desktop_ui",)), "running": True}
        good = {"session_key": "other", "agent": policy.seal_agent(agent())}
        errors = []
        start = threading.Barrier(3)
        def resume():
            start.wait()
            try:
                policy.require_compatible_runtimes([("old", unsafe), ("safe", good)], "stored")
            except policy.NonNativeSessionError:
                errors.append(True)
        threads = [threading.Thread(target=resume) for _ in range(2)]
        for thread in threads:
            thread.start()
        start.wait()
        for thread in threads:
            thread.join(timeout=2)
            self.assertFalse(thread.is_alive())
        self.assertEqual(2, len(errors))
        self.assertTrue(unsafe["running"])
        self.assertNotIn("_finalized", unsafe)

    def test_process_policy_does_not_change_with_later_environment(self):
        with patch.dict(os.environ, {"CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED": "false"}):
            self.assertEqual(set(), policy.filter_tool_names({"read_terminal"}))

    def test_lite_artifact_policy_cannot_be_disabled_before_import(self):
        for value in ("false", "", "0", "off"):
            with self.subTest(value=value):
                frozen = module("lite_non_native_fixed", PATCHES / "lite_non_native.py", value)
                self.assertTrue(frozen.ENABLED)
                self.assertEqual(set(), frozen.filter_tool_names({"read_terminal"}))
                with self.assertRaises(frozen.NonNativeSessionError):
                    frozen.seal_agent(agent(("read_terminal",), ("desktop_ui",)))
                self.assertEqual({"error": "unsupported_native_desktop_tool"}, json.loads(
                    frozen.tool_dispatch(lambda *args: self.fail("executed"))("read_terminal", {})))
                self.assertEqual((-32601, "unsupported_lite_web_rpc"), frozen.rpc_denial("shell.exec", {}, None))


class RPCPolicyTests(unittest.TestCase):
    def test_runtime_ui_requests_are_supported_without_method_whitelist(self):
        params = {
            "ping": {}, "session.create": {"source": "web", "profile": "bot_alpha", "cwd": "."}, "session.list": {"limit": 100, "profile": "bot_alpha"},
            "setup.status": {}, "setup.runtime_check": {}, "model.options": {"explicit_only": True},
            "config.set": {"session_id": "session-1", "key": "model", "value": "m --provider managed --session"},
            "approval.pending": {"session_id": "session-1"}, "approval.received": {"session_id": "session-1", "request_id": "approval-1"},
            "session.resume": {"session_id": "session-1", "source": "web", "lazy": True, "defer_history": True, "omit_messages": True},
            "session.events.since": {"session_id": "session-1", "last_seen": 0},
            "prompt.submit": {"session_id": "session-1", "text": "local stub"},
            "approval.respond": {"session_id": "session-1", "request_id": "approval-1", "choice": "once"},
            "clarify.respond": {"session_id": "session-1", "request_id": "clarify-1", "answer": "yes"},
            "profiles.list": {"include_sessions": True}, "profiles.describe": {"name": "bot_alpha"},
            "profiles.create": {"name": "bot_alpha", "clone_from": None, "no_skills": False, "share_auth": True},
            "profiles.configure": {"name": "bot_alpha", "model": "m", "provider": "managed", "disabled_skills": [], "ui_meta": {}},
            "profiles.set_asset": {"name": "bot_alpha", "asset": "avatar", "clear": True},
            "profiles.get_asset": {"name": "bot_alpha", "asset": "avatar"}, "mcp.catalog": {},
            "projects.list": {"profile": "bot_alpha"}, "projects.tree": {"profile": "bot_alpha", "preview_limit": 3},
            "projects.create": {"name": "Demo project", "folders": ["."], "primary_path": ".", "profile": "bot_alpha", "use": True},
            "projects.update": {"id": "project-1", "name": "Renamed"},
            "projects.add_folder": {"id": "project-1", "path": ".", "label": "workspace", "is_primary": False},
            "projects.delete": {"id": "project-1", "profile": "bot_alpha"},
        }
        for method in ("session.status", "session.history", "session.interrupt", "session.activate", "session.usage", "session.close"):
            params[method] = {"session_id": "session-1"}
        for method, fields in params.items():
            with self.subTest(method=method):
                self.assertIsNone(policy.rpc_denial(method, fields, None))
        for method, fields in (("fs.read", {"path": "."}), ("git.status", {"path": "."}), ("mcp.servers.list", {}), ("future.desktop.ui.read", {"workspace": "."})):
            with self.subTest(method=method):
                self.assertIsNone(policy.rpc_denial(method, fields, None))

    def test_no_native_rpc_families(self):
        for method in ("shell.exec", "cli.exec", "browser.manage", "desktop.respond", "terminal.start", "tools.call"):
            with self.subTest(method=method):
                self.assertEqual((-32601, "unsupported_lite_web_rpc"), policy.rpc_denial(method, {}, None))
        for fields in ({"cwd": "/"}, {"profile": "../other"}, {"source": "internal"}):
            self.assertEqual((-32602, "unsupported_lite_web_parameters"), policy.rpc_denial("session.create", fields, None))
        self.assertIsNone(policy.rpc_denial("session.create", {"model": "other", "future_option": {"enabled": True}}, None))

    def test_rpc_types_and_bounds(self):
        for method, fields in [
            ("session.resume", {"session_id": "../other"}),
            ("profiles.describe", {"name": "../other"}),
            ("projects.create", {"name": "demo", "folders": ["/etc"]}),
            ("projects.create", {"name": "demo", "folders": ["../outside"]}),
        ]:
            with self.subTest(method=method, fields=fields):
                self.assertEqual((-32602, "unsupported_lite_web_parameters"), policy.rpc_denial(method, fields, None))

    def test_only_verified_transport_scope_allows_classic_internal_rpc(self):
        trusted = SimpleNamespace(_ws=SimpleNamespace(scope={"hermes_lite_internal": True}))
        untrusted = SimpleNamespace(auth_identity={"source": "internal"}, _ws=SimpleNamespace(scope={}))
        self.assertIsNone(policy.rpc_denial("cli.exec", {}, trusted))
        self.assertIsNone(policy.rpc_denial("cli.exec", {}, None, stdio=True))
        self.assertEqual((-32601, "unsupported_lite_web_rpc"), policy.rpc_denial("cli.exec", {}, untrusted))

    def test_desktop_create_and_live_options_use_exact_scope_and_types(self):
        self.assertIsNone(policy.rpc_denial("session.create", {"source": "web", "cols": 96, "model": "m", "provider": "managed", "reasoning_effort": "high", "fast": False}, None))
        self.assertIsNone(policy.rpc_denial("prompt.submit", {"session_id": "s", "text": "next", "queued": True, "interrupted": False}, None))
        for key, values in {"reasoning": policy.REASONING_EFFORTS, "fast": {"fast", "normal"}}.items():
            for value in values:
                self.assertIsNone(policy.rpc_denial("config.set", {"session_id": "s", "key": key, "value": value}, None))
        for fields in [
            {"key": "reasoning", "value": "high"},
            {"session_id": "s", "key": "reasoning", "value": "show"},
            {"session_id": "s", "key": "reasoning", "value": "high", "scope": "global"},
            {"session_id": "s", "key": "fast", "value": "toggle"},
            {"session_id": "s", "key": "fast", "value": True},
            {"session_id": "s", "key": "model", "value": "m --provider managed --global"},
        ]:
            self.assertIsNone(policy.rpc_denial("config.set", fields, None), fields)
        for fields in ({"cols": True}, {"cols": 501}, {"model": "m"}, {"model": "m", "provider": "p", "fast": "false"}, {"reasoning_effort": "show"}):
            self.assertIsNone(policy.rpc_denial("session.create", fields, None), fields)

    def test_runtime_owns_model_and_provider_validation(self):
        payload = {"provider": "managed", "model": "default", "providers": [{"slug": "managed", "authenticated": True, "models": ["m"]}, {"slug": "external", "authenticated": False, "models": ["evil"]}]}
        inventory = SimpleNamespace(build_model_options_payload=lambda *args, **kwargs: payload)
        namespace = {"_model_picker_context": lambda _: object()}
        with patch.dict("sys.modules", {"hermes_cli": SimpleNamespace(), "hermes_cli.inventory": inventory}):
            for fields in ({"model": "m", "provider": "managed"}, {"model": "default", "provider": "managed"}, {"model": "evil", "provider": "external"}, {"model": "new", "provider": "managed"}):
                self.assertIsNone(policy.rpc_prepare("session.create", fields, namespace, None))
            self.assertIsNone(policy.rpc_prepare("config.set", {"key": "model", "value": "m --provider managed --session"}, namespace, None))
            self.assertIsNone(policy.rpc_prepare("setup.runtime_check", {"provider": "managed"}, namespace, None))
        self.assertIsNone(policy.rpc_prepare("session.create", {"model": "m", "provider": "managed"}, namespace, None))

    def test_config_scope_serializes_close_without_holding_registry_lock(self):
        entered, release, closed = threading.Event(), threading.Event(), threading.Event()
        live = {"agent": policy.seal_agent(agent())}
        namespace = {"current_transport": lambda: None, "_stdio_transport": object(), "_session_resume_lock": threading.Lock(), "_sessions_lock": threading.RLock(), "_sessions": {"s": live}, "_err": lambda rid, code, message: {"error": {"code": code, "message": message}}, "entered": entered, "release": release}
        exec('def change(rid, params):\n    entered.set()\n    if not release.wait(2): raise RuntimeError("blocked")\n    _sessions[params["session_id"]]["changed"] = True\n    return {"result": True}', namespace)
        change = policy.session_config(namespace["change"])
        result = []
        worker = threading.Thread(target=lambda: result.append(change(1, {"session_id": "s"})))
        worker.start()
        self.assertTrue(entered.wait(2))
        def close():
            with namespace["_session_resume_lock"]:
                with namespace["_sessions_lock"]:
                    namespace["_sessions"].pop("s")
                closed.set()
        closer = threading.Thread(target=close)
        closer.start()
        self.assertTrue(namespace["_sessions_lock"].acquire(timeout=1))
        namespace["_sessions_lock"].release()
        self.assertFalse(closed.is_set())
        release.set()
        worker.join(2)
        closer.join(2)
        self.assertFalse(worker.is_alive())
        self.assertFalse(closer.is_alive())
        self.assertEqual([{"result": True}], result)
        self.assertTrue(live["changed"])
        self.assertEqual(4001, change(2, {"session_id": "s"})["error"]["code"])
        namespace["_sessions"]["s"] = {"agent": None}
        self.assertEqual(5032, change(3, {"session_id": "s"})["error"]["code"])

    def test_readiness_and_pending_approval_preserve_runtime_shape(self):
        ns = {"_err": lambda rid, code, message: {"id": rid, "error": {"code": code, "message": message}}, "_approval_request_payload": lambda _: {"request_id": "r", "command": "safe command", "choices": ["once", "session", "always", "deny"], "private": "secret"}}
        response = policy.rpc_result("setup.runtime_check", {"id": 1, "result": {"ok": False, "error": "private-token", "source": "credential-file"}}, ns, None)
        self.assertEqual({"id": 1, "result": {"ok": False, "error": "private-token", "source": "credential-file"}}, response)
        response = policy.rpc_result("approval.pending", {"id": 2, "result": {"approvals": [{"command": "unredacted secret"}]}}, ns, None)
        self.assertIn("unredacted secret", json.dumps(response))


class HydrationLifecycleTests(unittest.TestCase):
    def test_actual_hydration_failure_cannot_remove_configured_session(self):
        # Use the actual built/installed handler, not a rewritten lookalike.
        # Host integration runs point this at the exact patched upstream tree.
        source_root = Path(os.environ.get("HERMES_LITE_TEST_PATCHED_ROOT", "/opt/hermes-agent"))
        source_file = source_root / "tui_gateway/server.py"
        if not source_file.is_file():
            self.skipTest("requires installed candidate or HERMES_LITE_TEST_PATCHED_ROOT")
        tree = ast.parse(source_file.read_text(encoding="utf-8"))
        hydration = next(node for node in tree.body if isinstance(node, ast.FunctionDef)
                         and node.name == "_schedule_resume_hydration")
        ready, entered, failed, popped = (threading.Event() for _ in range(4))
        threads = []

        class Sessions(dict):
            def pop(self, *args):
                value = super().pop(*args)
                popped.set()
                return value

        live = {"agent": None, "resume_hydrating": True, "history_lock": threading.Lock(),
                "resume_history_ready": threading.Event(), "agent_ready": threading.Event()}
        sessions = Sessions(s=live)

        def start_thread(*args, **kwargs):
            thread = threading.Thread(*args, **kwargs)
            threads.append(thread)
            return thread

        def late_failure(*_args):
            # resume_history_ready has awakened the independent builder. Its
            # sealed agent may finish before this late hydration failure.
            self.assertTrue(live["resume_history_ready"].is_set())
            live["agent"] = policy.seal_agent(agent())
            ready.set()
            if not entered.wait(2):
                raise AssertionError("config handler did not start")
            raise RuntimeError("synthetic late hydration failure")

        def emit(kind, _sid, _payload=None):
            if kind == "error":
                failed.set()

        namespace = {"threading": SimpleNamespace(Thread=start_thread), "_sessions": sessions,
                     "_sessions_lock": threading.RLock(), "_session_resume_lock": threading.Lock(),
                     "current_transport": lambda: None, "_stdio_transport": object(),
                     "_err": lambda rid, code, message: {"error": {"code": code}},
                     "sanitize_replay_history": lambda value: value, "_todo_state_from_history": lambda _: None,
                     "_start_agent_build": lambda *_: None, "_maybe_schedule_auto_continue": late_failure,
                     "_emit": emit, "entered": entered, "failed": failed, "popped": popped}
        exec(compile(ast.Module(body=[hydration], type_ignores=[]), str(source_file), "exec"), namespace)
        exec('def config(rid, params):\n'
             '    entered.set()\n'
             '    if not failed.wait(2): raise AssertionError("hydration did not fail")\n'
             '    popped.wait(0.5)\n'
             '    return {"scope": "session" if _sessions.get(params["session_id"]) else "global"}', namespace)
        database = SimpleNamespace(reopen_session=lambda _: None,
                                   get_resume_conversations=lambda _: ([], []),
                                   get_ancestor_display_prefix=lambda _: [])
        namespace["_schedule_resume_hydration"]("s", "stored", database)
        try:
            self.assertTrue(ready.wait(2))
            result = policy.session_config(namespace["config"])(1, {"session_id": "s"})
            self.assertEqual({"scope": "session"}, result)
        finally:
            entered.set()
            for thread in threads:
                thread.join(2)
                self.assertFalse(thread.is_alive(), "hydration deadlocked with config")
        self.assertTrue(popped.is_set(), "failed hydration did not release its record after config")


class BackgroundTurnLifecycleTests(unittest.TestCase):
    """Execute the real patched orphan lifecycle without model/network dependencies."""
    def setUp(self):
        source = Path(os.environ.get("HERMES_LITE_TEST_PATCHED_ROOT", "/opt/hermes-agent")) / "tui_gateway/server.py"
        if not source.is_file():
            self.skipTest("requires the pinned patched Hermes source")
        names = {"_ws_session_is_detached", "_schedule_ws_orphan_reap", "_cancel_ws_orphan_reap",
                 "_close_sessions_for_transport", "_live_session_payload"}
        functions = [n for n in ast.parse(source.read_text()).body if isinstance(n, ast.FunctionDef) and n.name in names]
        self.assertEqual(names, {n.name for n in functions})
        timers = self.timers = []
        class Timer:
            def __init__(self, delay, callback):
                self.delay, self.callback, self.cancelled = delay, callback, False
                timers.append(self)
            def start(self):
                pass
            def cancel(self):
                self.cancelled = True
        self.transport = object()
        self.agent = policy.seal_agent(agent())
        self.session = {"agent": self.agent, "transport": self.transport, "running": True,
                        "history_lock": threading.RLock(), "history": [{"role": "user", "content": "work"}],
                        "inflight_turn": {"started_at": 1.0}, "session_key": "stored", "source": "web"}
        self.interrupted, self.closed = [], []
        sessions = {"s": self.session}
        self.ns = {"threading": SimpleNamespace(Timer=Timer), "time": __import__("time"), "Transport": object,
                   "_lite_non_native": policy, "_sessions": sessions, "_sessions_lock": threading.RLock(),
                   "_session_resume_lock": threading.Lock(), "_detached_ws_transport": object(),
                   "_WS_ORPHAN_REAP_GRACE_S": 20, "_WS_ORPHAN_INTERRUPT_REAP_POLL_S": 1,
                   "_WS_ORPHAN_INTERRUPT_REAP_MAX_POLLS": 60, "_pending_ws_reaps": {},
                   "_session_has_active_delegations": lambda *_: False,
                   "_pop_session_by_id": lambda sid: sessions.pop(sid, None),
                   "_interrupt_session_turn": lambda *args, **kw: self.interrupted.append(args),
                   "_teardown_popped_session": lambda session, **kw: self.closed.append(session),
                   "_transport_is_dead": lambda _: False,
                   "logger": SimpleNamespace(info=lambda *_: None, error=lambda *_: None, exception=lambda *_: None),
                   "_inflight_snapshot": lambda _: {"assistant": "partial progress"},
                   "_queued_prompt_snapshot": lambda _: None, "_fallback_session_info": lambda _: {},
                   "_session_lookup_key": lambda session, **kw: session["session_key"],
                   "_session_live_status": lambda *_: "running", "_pending_approval_request_payload": lambda _: None,
                   "_pending_clarify_request_payload": lambda _: None, "_attach_todo_state": lambda payload, _: payload}
        exec(compile(ast.Module(body=functions, type_ignores=[]), str(source), "exec"), self.ns)

    def test_leaving_keeps_turn_alive_then_reentry_restores_progress(self):
        self.assertEqual((0, 1), self.ns["_close_sessions_for_transport"](self.transport))
        for _ in range(65):  # Also exceed the former forced-interrupt polling budget.
            self.timers[-1].callback()
        self.assertEqual([], self.interrupted, "navigation must not act as a user interrupt")
        self.assertEqual([], self.closed)
        self.assertIs(self.session, self.ns["_sessions"]["s"])
        pending = self.timers[-1]
        replacement = object()
        payload = self.ns["_live_session_payload"]("s", self.session, transport=replacement, omit_messages=True)
        self.assertTrue(pending.cancelled)
        self.assertIs(self.agent, self.session["agent"])
        self.assertIs(replacement, self.session["transport"])
        self.assertTrue(payload["running"])
        self.assertEqual({"assistant": "partial progress"}, payload["inflight"])
        self.assertEqual(1, payload["message_count"])

    def test_detached_completed_turn_is_still_reaped(self):
        self.ns["_close_sessions_for_transport"](self.transport)
        self.timers[-1].callback()
        self.assertEqual([], self.interrupted)
        self.session["running"] = False  # Normal model/turn finalization owns completion.
        self.timers[-1].callback()
        self.assertEqual([], self.interrupted)
        self.assertEqual([self.session], self.closed)
        self.assertNotIn("s", self.ns["_sessions"])


    def test_queued_turn_survives_the_completion_to_dispatch_handoff(self):
        self.ns["_close_sessions_for_transport"](self.transport)
        self.session["running"] = False
        for key in ("queued_prompt", "queued_prompts"):
            with self.subTest(key=key):
                self.session[key] = {"text": "accepted next turn"} if key == "queued_prompt" else [{"text": "accepted next turn"}]
                self.timers[-1].callback()
                self.assertIs(self.session, self.ns["_sessions"].get("s"))
                self.assertEqual([], self.closed)
                self.session.pop(key)

    def test_non_lite_disconnect_policy_remains_unchanged(self):
        self.ns["_close_sessions_for_transport"](self.transport)
        with patch.object(policy, "ENABLED", False):
            self.timers[-1].callback()
        self.assertEqual(1, len(self.interrupted))



class PatchIntegrityTests(unittest.TestCase):
    def test_sha_mismatch_is_rejected_before_writing(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            for relative in patcher.EXPECTED:
                path = root / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("original untouched")
            with self.assertRaisesRegex(ValueError, "exact locked upstream"):
                patcher.apply(root)
            for relative in patcher.EXPECTED:
                self.assertEqual("original untouched", (root / relative).read_text())

    def test_missing_and_duplicate_anchors_fail_closed(self):
        for source in ("missing", "anchor anchor"):
            with self.assertRaisesRegex(ValueError, "anchor changed"):
                patcher.replace_once(source, "anchor", "replacement")


class DeploymentEnvironmentTests(unittest.TestCase):
    def test_workspace_cannot_change_or_create_managed_keys(self):
        initial = {"HOME": "/fixture/home", "HERMES_HOME": "/fixture/home/.hermes", "PORT": "20001",
                   "CLAWMANAGER_CONTROL_UI_ORIGIN": "https://internal.example",
                   "CLAWMANAGER_TRUSTED_PROXY_CIDRS": "10.0.0.0/24",
                   "HERMES_DASHBOARD_BASIC_AUTH_PASSWORD": "initial-fixture-only"}
        with patch.dict(os.environ, initial, clear=True):
            guard = module("lite_environment_test", PATCHES / "lite_environment.py")
            fixture = "\n".join(key + "=workspace-override" for key in initial)
            fixture += "\nCLAWMANAGER_FORGED=bad\nPYTHONPATH=/unsafe\nFIXTURE_ROOT=/user\nFIXTURE_NOTE=${FIXTURE_ROOT}/child\n"
            self.assertTrue(guard.load_dotenv(stream=io.StringIO(fixture), override=True))
            for key, value in initial.items():
                self.assertEqual(value, os.environ[key])
            self.assertNotIn("CLAWMANAGER_FORGED", os.environ)
            self.assertNotIn("PYTHONPATH", os.environ)
            self.assertEqual("/user/child", os.environ["FIXTURE_NOTE"])
            guard.load_dotenv(stream=io.StringIO("FIXTURE_NOTE=new\n"), override=False)
            self.assertEqual("/user/child", os.environ["FIXTURE_NOTE"])

    def test_reload_and_error_preserve_initial_managed_values(self):
        with patch.dict(os.environ, {"HOME": "/fixture/home", "CLAWMANAGER_INSTANCE_ID": "123"}, clear=True):
            guard = module("lite_environment_reload_test", PATCHES / "lite_environment.py")
            @guard.preserve_environment
            def reload():
                os.environ["HOME"] = "/changed"
                os.environ["CLAWMANAGER_FORGED"] = "bad"
                os.environ["USER_OPTION"] = "retained"
                raise ValueError("controlled failure")
            with self.assertRaises(ValueError):
                reload()
            self.assertEqual("/fixture/home", os.environ["HOME"])
            self.assertNotIn("CLAWMANAGER_FORGED", os.environ)
            self.assertEqual("retained", os.environ["USER_OPTION"])


class PublicProtocolTests(unittest.TestCase):
    def test_accept_only_selects_offered_public_protocol(self):
        boundary_module = module("lite_boundary_protocol_test", PATCHES / "lite_gateway_boundary.py")
        for path in ("/api/ws", "/api/events", "/api/pty"):
            for offered, selected in ((b"hermes-gateway-v1, hermes-gateway-ticket.fixture-only", "hermes-gateway-v1"),
                                      (b"hermes-gateway-ticket.fixture-only", None), (b"", None)):
                with self.subTest(path=path, offered=offered):
                    sent = []
                    async def application(scope, receive, send):
                        await send({"type": "websocket.accept", "subprotocol": "hermes-gateway-ticket.fixture-only"})
                    async def send(event):
                        sent.append(event)
                    with patch.dict(os.environ, {"CLAWMANAGER_CONTROL_UI_ORIGIN": "https://internal.example", "CLAWMANAGER_TRUSTED_PROXY_CIDRS": "10.0.0.0/24"}):
                        boundary = boundary_module.LiteGatewayBoundary(application)
                    scope = {"type": "websocket", "path": path, "client": ("10.0.0.5", 12345),
                             "headers": [(b"origin", b"https://internal.example"), (b"sec-websocket-protocol", offered)]}
                    asyncio.run(boundary(scope, None, send))
                    self.assertEqual([{"type": "websocket.accept", "subprotocol": selected}], sent)
                    self.assertNotIn("fixture-only", json.dumps(sent))


class WorkspaceBoundaryTests(unittest.TestCase):
    def setUp(self):
        self.boundary_module = module("lite_boundary_workspace_test", PATCHES / "lite_gateway_boundary.py")
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name) / "instance"
        self.root.mkdir()
        self.env = {
            "CLAWMANAGER_CONTROL_UI_ORIGIN": "https://internal.example",
            "CLAWMANAGER_TRUSTED_PROXY_CIDRS": "10.0.0.0/24",
            "CLAWMANAGER_WORKSPACE_PATH": str(self.root),
        }

    def tearDown(self):
        self.temp.cleanup()

    def scope(self, method="GET", query=""):
        return {
            "type": "http", "method": method, "path": "/api/fs/list",
            "query_string": query.encode(), "client": ("10.0.0.5", 12345),
            "headers": [(b"origin", b"https://internal.example")],
        }

    @staticmethod
    def wire_path(value):
        return str(value).replace("\\", "/")

    def test_query_paths_are_limited_to_current_instance_workspace(self):
        inside = self.root / "project" / "file.txt"
        outside = Path(self.temp.name) / "other" / "file.txt"
        with patch.dict(os.environ, self.env, clear=True):
            boundary = self.boundary_module.LiteGatewayBoundary(lambda *_: None)
            self.assertIsNone(boundary.denial(self.scope(query=urlencode({"path": self.wire_path(inside)}))))
            self.assertEqual("workspace_path_rejected", boundary.denial(
                self.scope(query=urlencode({"path": self.wire_path(outside)}))))
            self.assertEqual("workspace_path_rejected", boundary.denial(
                self.scope(query=urlencode({"path": "../sibling"}))))

    def test_realpath_rejects_workspace_symlink_escape(self):
        outside = Path(self.temp.name) / "outside"
        outside.mkdir()
        link = self.root / "escape"
        try:
            link.symlink_to(outside, target_is_directory=True)
        except OSError as error:
            self.skipTest(f"symlinks unavailable: {error}")
        with patch.dict(os.environ, self.env, clear=True):
            self.assertFalse(self.boundary_module.workspace_path_allowed(str(link / "secret.txt")))

    def test_json_body_is_checked_and_replayed_without_shape_changes(self):
        payload = {"project": {"path": self.wire_path(self.root / "project")}, "options": {"custom": True}}
        body = json.dumps(payload).encode()
        received = []
        sent = []

        async def application(scope, receive, send):
            received.append(await receive())
            await send({"type": "http.response.start", "status": 204, "headers": []})

        async def receive():
            return {"type": "http.request", "body": body, "more_body": False}

        async def send(event):
            sent.append(event)

        scope = self.scope(method="POST")
        scope["headers"].append((b"content-type", b"application/json"))
        with patch.dict(os.environ, self.env, clear=True):
            boundary = self.boundary_module.LiteGatewayBoundary(application)
            asyncio.run(boundary(scope, receive, send))
        self.assertEqual([{"type": "http.request", "body": body, "more_body": False}], received)
        self.assertEqual(204, sent[0]["status"])

    def test_json_body_rejects_nested_workspace_escape(self):
        body = json.dumps({"project": {"path": self.wire_path(Path(self.temp.name) / "other")}}).encode()
        sent = []

        async def forbidden(*_):
            self.fail("rejected body reached the Runtime API")

        async def receive():
            return {"type": "http.request", "body": body, "more_body": False}

        async def send(event):
            sent.append(event)

        scope = self.scope(method="POST")
        scope["headers"].append((b"content-type", b"application/json"))
        with patch.dict(os.environ, self.env, clear=True):
            boundary = self.boundary_module.LiteGatewayBoundary(forbidden)
            asyncio.run(boundary(scope, receive, send))
        self.assertEqual(403, sent[0]["status"])
        self.assertIn(b"workspace_path_rejected", sent[1]["body"])


if __name__ == "__main__":
    unittest.main()
