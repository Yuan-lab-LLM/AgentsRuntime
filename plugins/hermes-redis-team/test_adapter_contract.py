import asyncio
import importlib.util
import json
import os
import sys
import tempfile
import types
import unittest
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
from unittest import mock


def install_gateway_stubs():
    gateway = types.ModuleType("gateway")
    config = types.ModuleType("gateway.config")
    platforms = types.ModuleType("gateway.platforms")
    base = types.ModuleType("gateway.platforms.base")
    session = types.ModuleType("gateway.session")

    class Platform(str):
        pass

    class PlatformConfig:
        extra = {}

    class BasePlatformAdapter:
        def __init__(self, config=None, platform=None):
            self.config = config
            self.platform = platform
            self.is_connected = False

        def _mark_connected(self):
            self.is_connected = True

        def _mark_disconnected(self):
            self.is_connected = False

        def _set_fatal_error(self, *_args, **_kwargs):
            pass

    class MessageEvent:
        def __init__(self, **kwargs):
            self.__dict__.update(kwargs)

    class MessageType:
        TEXT = "text"

    class ProcessingOutcome:
        SUCCESS = "success"
        CANCELLED = "cancelled"

    class SendResult:
        def __init__(self, **kwargs):
            self.__dict__.update(kwargs)

    class SessionSource:
        def __init__(self, **kwargs):
            self.__dict__.update(kwargs)

    config.Platform = Platform
    config.PlatformConfig = PlatformConfig
    base.BasePlatformAdapter = BasePlatformAdapter
    base.MessageEvent = MessageEvent
    base.MessageType = MessageType
    base.ProcessingOutcome = ProcessingOutcome
    base.SendResult = SendResult
    session.SessionSource = SessionSource
    sys.modules.update(
        {
            "gateway": gateway,
            "gateway.config": config,
            "gateway.platforms": platforms,
            "gateway.platforms.base": base,
            "gateway.session": session,
        }
    )


install_gateway_stubs()
spec = importlib.util.spec_from_file_location("hermes_redis_team_adapter", Path(__file__).with_name("adapter.py"))
adapter = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = adapter
spec.loader.exec_module(adapter)


class HermesRedisTeamContractTests(unittest.TestCase):
    def test_platform_hint_uses_assignment_validation_ownership_without_blocking(self):
        source = Path(adapter.__file__).read_text(encoding="utf-8")
        self.assertIn("Validation is", source)
        self.assertIn("assignment-specific", source)
        self.assertIn("production-only assignments", source)
        self.assertIn("review, or evidence work", source)
        self.assertIn("never block delivery", source)
        self.assertIn("call team_complete_task", source)

    def test_assignment_validation_guidance_is_contract_driven_and_role_agnostic(self):
        developer = adapter.RedisTeamSettings(
            enabled=True,
            redis_url="redis://example.invalid:6379/0",
            team_id="42",
            member_id="developer",
            role="developer",
        )
        production = adapter._assignment_validation_guidance(developer, {"reviewRequired": True})
        self.assertIn("production-only", production)
        self.assertIn("without running syntax checks, tests, Browser acceptance", production)
        validator = adapter._assignment_validation_guidance(
            developer,
            {"validationAssignment": True, "validationTargetAssignmentId": "dev-1"},
        )
        self.assertIn("test/review/evidence work", validator)
        self.assertIn("regardless of role name", validator)
        reviewer = adapter.RedisTeamSettings(
            enabled=True,
            redis_url="redis://example.invalid:6379/0",
            team_id="42",
            member_id="reviewer",
            role="reviewer",
        )
        reviewer_production = adapter._assignment_validation_guidance(reviewer, {"reviewRequired": True})
        self.assertIn("production-only", reviewer_production)
        self.assertIn("without running syntax checks, tests, Browser acceptance", reviewer_production)

    def test_team_send_preserves_assignment_validation_contract(self):
        async def run_test():
            with tempfile.TemporaryDirectory() as tmp:
                settings = self.settings(Path(tmp))
                adapter.ensure_team_dirs(settings)
                adapter._persist_active_envelope(
                    settings,
                    {"taskId": "team-42-task-1", "rootTaskId": "team-42-task-1", "rootMessageId": "root-1"},
                )

                class FakeRedis:
                    def __init__(self, *_args, **_kwargs):
                        pass

                    async def connect(self):
                        return None

                    async def command(self, *_args):
                        return "1-0"

                    def close(self):
                        pass

                with mock.patch.object(adapter, "load_settings", return_value=settings), mock.patch.object(
                    adapter, "AsyncRedisClient", FakeRedis
                ):
                    raw = await adapter._tool_team_send(
                        {
                            "to": "auditor",
                            "text": "Validate revision 2.",
                            "assignmentId": "review-2",
                            "validationAssignment": True,
                            "validationTargetAssignmentId": "dev-2",
                            "validationTargetRevision": 2,
                            "dependsOn": ["dev-2"],
                        }
                    )
                sent = json.loads(raw)["sent"]
                self.assertTrue(sent["validationAssignment"])
                self.assertEqual(sent["validationTargetAssignmentId"], "dev-2")
                self.assertEqual(sent["validationTargetRevision"], 2)
                self.assertEqual(sent["dependsOn"], ["dev-2"])

        asyncio.run(run_test())

    def test_team_send_accepts_message_alias_and_rejects_conflicting_text(self):
        async def run_test():
            with tempfile.TemporaryDirectory() as tmp:
                settings = self.settings(Path(tmp))
                adapter.ensure_team_dirs(settings)
                adapter._persist_active_envelope(
                    settings,
                    {"taskId": "team-42-task-1", "rootTaskId": "team-42-task-1"},
                )

                class FakeRedis:
                    def __init__(self, *_args, **_kwargs):
                        pass

                    async def connect(self):
                        return None

                    async def command(self, *_args):
                        return "1-0"

                    def close(self):
                        pass

                with mock.patch.object(adapter, "load_settings", return_value=settings), mock.patch.object(
                    adapter, "AsyncRedisClient", FakeRedis
                ):
                    accepted = json.loads(await adapter._tool_team_send({
                        "recipient": "auditor",
                        "message": "Review the current result.",
                        "assignmentId": "review-1",
                    }))
                    conflict = json.loads(await adapter._tool_team_send({
                        "to": "auditor",
                        "text": "one",
                        "prompt": "two",
                    }))
                self.assertTrue(accepted["ok"])
                self.assertEqual(accepted["sent"]["text"], "Review the current result.")
                self.assertFalse(conflict["ok"])
                self.assertTrue(conflict["retryable"])
                self.assertEqual(conflict["code"], "conflicting_team_message")

        asyncio.run(run_test())

    def test_business_delivery_uses_complete_ledger_without_trusting_agent_revision(self):
        settings = adapter.RedisTeamSettings(
            enabled=True,
            redis_url="redis://example.invalid:6379/0",
            team_id="42",
            member_id="leader",
            role="leader",
        )
        state = {
            "assignmentLedgerComplete": True,
            "snapshotSchemaVersion": 2,
            "ledgerVersion": 12,
            "assignments": {
                "dev-page": {
                    "assignmentId": "dev-page",
                    "ownerMemberKey": "developer",
                    "revision": 1,
                    "status": "succeeded",
                    "nextRevisionAllowed": False,
                    "nextRevision": 2,
                }
            },
        }
        follow_up = adapter._hermes_business_delivery_from_ledger(
            settings,
            {"to": "developer", "assignmentId": "dev-page", "intent": "assignment"},
            state,
            explicit_assignment_id=True,
        )
        self.assertEqual(follow_up["kind"], "ambiguous")
        self.assertEqual(follow_up["revision"], 1)

        next_stage = adapter._hermes_business_delivery_from_ledger(
            settings,
            {"to": "developer", "assignmentId": "dev-export", "intent": "assignment"},
            state,
            explicit_assignment_id=True,
        )
        self.assertEqual(next_stage["kind"], "assignment")
        self.assertEqual(next_stage["revision"], 1)

        no_identity = adapter._hermes_business_delivery_from_ledger(
            settings,
            {"to": "developer", "assignmentId": "generated", "intent": "send"},
            state,
            explicit_assignment_id=False,
        )
        self.assertEqual(no_identity["kind"], "ambiguous")

        state["assignments"]["dev-page"].update({
            "status": "failed",
            "nextRevisionAllowed": True,
            "nextRevision": 2,
        })
        recovery = adapter._hermes_business_delivery_from_ledger(
            settings,
            {"to": "developer", "assignmentId": "dev-page", "intent": "send"},
            state,
            explicit_assignment_id=True,
        )
        self.assertEqual(recovery["kind"], "assignment")
        self.assertEqual(recovery["revision"], 2)
        self.assertTrue(recovery["authorized"])

    def test_roster_target_resolution_uses_unique_information_and_never_fuzzy_routes(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            Path(tmp, "team.json").write_text(json.dumps({
                "members": [
                    {"memberId": "leader", "displayName": "Lead"},
                    {"memberId": "developer", "displayName": "Builder"},
                ]
            }), encoding="utf-8")
            resolved = adapter._resolve_roster_target(settings, "identity={leader}")
            self.assertEqual(resolved["memberId"], "leader")
            ambiguous = adapter._resolve_roster_target(settings, "developer should report to leader")
            self.assertEqual(ambiguous["memberId"], "")
            self.assertEqual(set(ambiguous["candidates"]), {"developer", "leader"})
            typo = adapter._resolve_roster_target(settings, "leadr")
            self.assertEqual(typo["memberId"], "")
            self.assertEqual(typo["suggestions"], ["leader"])

    def test_live_runtime_attempt_downgrades_stale_ledger_recovery_to_context(self):
        settings = adapter.RedisTeamSettings(
            enabled=True,
            redis_url="redis://example.invalid:6379/0",
            team_id="42",
            member_id="leader",
            role="leader",
        )
        state = {"assignments": {"dev-page": {
            "assignmentId": "dev-page",
            "ownerMemberKey": "developer",
            "revision": 1,
            "status": "failed",
            "nextRevisionAllowed": True,
            "nextRevision": 2,
        }}}
        decision = adapter._hermes_business_delivery_from_ledger(
            settings,
            {"to": "developer", "assignmentId": "dev-page", "rootTaskId": "team-42-task-1", "intent": "send"},
            state,
            explicit_assignment_id=True,
            target_status={
                "runtimeStatus": "awaiting_completion_receipt",
                "availability": "busy",
                "lastSeenAt": adapter._now_iso(),
                "currentAssignmentId": "dev-page",
                "currentRevision": 1,
            },
        )
        self.assertEqual(decision["kind"], "context")
        self.assertEqual(decision["reason"], "runtime_attempt_still_active")
        self.assertEqual(decision["revision"], 1)

    def settings(self, root):
        return adapter.RedisTeamSettings(
            enabled=True,
            redis_url="redis://example.invalid:6379/0",
            team_id="42",
            member_id="developer",
            role="developer",
            shared_dir=str(root),
            preview_origin="http://clawmanager-egress-proxy.system.svc.cluster.local:3128",
            team_token="test-token",
        )

    def test_protocol_matches_current_worker_contract(self):
        self.assertEqual(adapter.PROTOCOL_VERSION, 4)
        self.assertIn("completion_ack_v1", adapter.PROTOCOL_CAPABILITIES)
        self.assertNotIn("automatic_turn_completion_v2", adapter.PROTOCOL_CAPABILITIES)
        self.assertIn("explicit_completion_receipt_v1", adapter.PROTOCOL_CAPABILITIES)
        self.assertIn("turn_end_monitor_v1", adapter.PROTOCOL_CAPABILITIES)
        self.assertIn("turn_outcome_v1", adapter.PROTOCOL_CAPABILITIES)
        self.assertIn("immediate_recheck_v1", adapter.PROTOCOL_CAPABILITIES)
        self.assertIn("assignment_lifecycle_v1", adapter.PROTOCOL_CAPABILITIES)
        self.assertIn("assignment_activity_v2", adapter.PROTOCOL_CAPABILITIES)
        self.assertIn("terminal_tool_stop_v1", adapter.PROTOCOL_CAPABILITIES)
        self.assertIn("team_artifact_preview_v1", adapter.PROTOCOL_CAPABILITIES)
        self.assertIn("team_artifact_preview_v2", adapter.PROTOCOL_CAPABILITIES)

    def test_register_installs_fail_open_runtime_observation_hooks(self):
        class Context:
            def __init__(self):
                self.hooks = {}

            def register_hook(self, name, callback):
                self.hooks[name] = callback

            def register_tool(self, **_kwargs):
                pass

            def register_platform(self, **_kwargs):
                pass

        context = Context()
        adapter.register(context)
        self.assertEqual(
            set(context.hooks),
            {"pre_api_request", "post_api_request", "pre_tool_call", "post_tool_call"},
        )

    def test_team_tool_schema_failure_is_retained_by_post_tool_hook(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            adapter.ensure_team_dirs(settings)
            adapter._begin_turn_observation(
                settings,
                {
                    "messageId": "schema-failure-turn",
                    "rootTaskId": "team-42-task-1",
                    "assignmentId": "dev-1",
                    "revision": 1,
                },
                context_only=False,
            )
            with mock.patch.object(adapter, "load_settings", return_value=settings):
                adapter._hook_pre_tool_call(
                    tool_name="team_send",
                    task_id="team-42-task-1",
                    turn_id="turn-1",
                )
                adapter._hook_post_tool_call(
                    tool_name="team_send",
                    task_id="team-42-task-1",
                    turn_id="turn-1",
                    status="error",
                    error_message="tool schema rejected arguments",
                    result=json.dumps({
                        "ok": False,
                        "retryable": True,
                        "error": "tool schema rejected arguments",
                    }),
                )
            observation = adapter._read_json(adapter._turn_observation_path(settings))
            self.assertEqual(observation["lastTool"]["toolName"], "team_send")
            self.assertTrue(observation["lastTool"]["failed"])
            self.assertTrue(observation["lastTool"]["retryable"])

    def test_artifact_read_is_paginated_and_observed(self):
        async def run_test():
            with tempfile.TemporaryDirectory() as tmp:
                settings = self.settings(Path(tmp))
                adapter.ensure_team_dirs(settings)
                target = settings.shared_path / "artifacts" / "large.txt"
                target.write_text("abcdefghij", encoding="utf-8")
                adapter._begin_turn_observation(
                    settings,
                    {
                        "messageId": "artifact-turn",
                        "rootTaskId": "team-42-task-1",
                        "assignmentId": "dev-1",
                        "revision": 1,
                    },
                    context_only=False,
                )
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    first = json.loads(await adapter._tool_team_artifact_read({
                        "scope": "team",
                        "path": "/team/artifacts/large.txt",
                        "maxBytes": 4,
                    }))
                    second = json.loads(await adapter._tool_team_artifact_read({
                        "scope": "team",
                        "path": "/team/artifacts/large.txt",
                        "offset": first["artifact"]["nextOffset"],
                        "maxBytes": 6,
                    }))
                self.assertEqual(first["artifact"]["content"], "abcd")
                self.assertTrue(first["artifact"]["truncated"])
                self.assertEqual(first["artifact"]["nextOffset"], 4)
                self.assertEqual(second["artifact"]["content"], "efghij")
                self.assertFalse(second["artifact"]["truncated"])
                observation = adapter._read_json(adapter._turn_observation_path(settings))
                self.assertEqual(observation["lastToolName"], "team_artifact_read")
                self.assertIsNone(observation["lastTool"])

        asyncio.run(run_test())

    def test_progress_blocked_is_nonterminal_waiting(self):
        async def run_test():
            with tempfile.TemporaryDirectory() as tmp:
                settings = self.settings(Path(tmp))
                adapter.ensure_team_dirs(settings)
                adapter._persist_active_envelope(settings, {
                    "messageId": "wait-turn",
                    "taskId": "team-42-task-1",
                    "rootTaskId": "team-42-task-1",
                    "assignmentId": "dev-1",
                    "revision": 1,
                })
                published = []

                async def capture(_settings, event, payload):
                    published.append((event, payload))

                with mock.patch.object(adapter, "load_settings", return_value=settings), mock.patch.object(
                    adapter, "_publish_event", capture
                ):
                    result = json.loads(await adapter._tool_team_update_progress({
                        "status": "blocked",
                        "summary": "Waiting for dependency dev-0",
                    }))
                self.assertTrue(result["ok"])
                self.assertEqual(result["status"]["availability"], "busy")
                self.assertEqual(result["status"]["runtimeStatus"], "waiting")
                self.assertEqual(published[0][1]["status"], "waiting")
                self.assertEqual(published[0][1]["runtimeStatus"], "waiting")

        asyncio.run(run_test())

    def test_managed_startup_identity_loads_from_environment(self):
        with mock.patch.dict(
            os.environ,
            {
                "CLAWMANAGER_TEAM_ENABLED": "true",
                "CLAWMANAGER_TEAM_REDIS_URL": "redis://example.invalid:6379/0",
                "CLAWMANAGER_TEAM_ID": "119",
                "CLAWMANAGER_TEAM_MEMBER_ID": "developer",
                "CLAWMANAGER_INSTANCE_ID": "397",
                "CLAWMANAGER_GATEWAY_GENERATION": "12",
            },
            clear=True,
        ):
            settings = adapter.load_settings(None)
        self.assertEqual(settings.instance_id, 397)
        self.assertEqual(settings.generation, 12)

    def test_existing_cooperative_directories_are_never_chmoded(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / "team"
            directories = [
                root,
                *(root / child for child in ("inbox", "status", "tasks", "results", "artifacts", "tmp", ".hermes-redis-team")),
            ]
            for directory in directories:
                directory.mkdir(parents=True, exist_ok=True)
            settings = self.settings(root)
            with mock.patch.object(Path, "chmod", side_effect=AssertionError("existing shared directory chmod attempted")):
                adapter.ensure_team_dirs(settings)

    def test_atomic_write_does_not_chmod_existing_parent(self):
        with tempfile.TemporaryDirectory() as tmp:
            parent = Path(tmp) / "team" / "status"
            parent.mkdir(parents=True)
            target = parent / "developer.json"
            original_chmod = Path.chmod

            def reject_parent_chmod(path, mode, *args, **kwargs):
                if path == parent:
                    raise AssertionError("existing shared parent chmod attempted")
                return original_chmod(path, mode, *args, **kwargs)

            with mock.patch.object(Path, "chmod", autospec=True, side_effect=reject_parent_chmod):
                adapter._atomic_write_json(target, {"ok": True})
            self.assertEqual(json.loads(target.read_text(encoding="utf-8")), {"ok": True})

    def test_concurrent_members_can_create_the_same_shared_directory(self):
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / "team" / "artifacts" / "team-42-task-7" / "members"
            with ThreadPoolExecutor(max_workers=8) as pool:
                futures = [pool.submit(adapter._ensure_shared_directory, target) for _ in range(24)]
                for future in futures:
                    future.result()
            self.assertTrue(target.is_dir())

    def test_shared_directory_rejects_files_and_symlinks(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            file_path = root / "not-a-directory"
            file_path.write_text("x", encoding="utf-8")
            with self.assertRaisesRegex(PermissionError, "not a directory"):
                adapter._ensure_shared_directory(file_path)

            link_path = root / "linked-directory"
            target = root / "target"
            target.mkdir()
            try:
                link_path.symlink_to(target, target_is_directory=True)
            except OSError as exc:
                self.skipTest(f"symlink creation is unavailable: {exc}")
            with self.assertRaisesRegex(PermissionError, "symbolic links"):
                adapter._ensure_shared_directory(link_path)

    def test_existing_shared_directory_must_be_cooperatively_accessible(self):
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / "team"
            target.mkdir()
            with (
                mock.patch.object(adapter, "_effective_access", return_value=False),
                self.assertRaisesRegex(PermissionError, "lacks read/write/execute access"),
            ):
                adapter._ensure_shared_directory(target)

    def test_unusable_shared_workspace_publishes_non_retryable_failure(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            shared_file = root / "team"
            shared_file.write_text("not a directory", encoding="utf-8")
            ready_file = root / "private" / "redis-team.ready.json"
            settings = adapter.RedisTeamSettings(
                enabled=True,
                redis_url="redis://example.invalid:6379/0",
                team_id="118",
                member_id="developer",
                role="developer",
                shared_dir=str(shared_file),
                ready_file=str(ready_file),
                instance_id=397,
                generation=11,
            )

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                    self.assertFalse(await instance.connect())
                    await instance.disconnect()

            asyncio.run(run_test())
            failure_file = adapter._startup_failure_path(ready_file)
            self.assertTrue(failure_file.is_file())
            failure = json.loads(failure_file.read_text(encoding="utf-8"))
            self.assertEqual(failure["state"], "failed")
            self.assertEqual(failure["instanceId"], 397)
            self.assertEqual(failure["generation"], 11)
            self.assertEqual(failure["error"]["code"], "shared_workspace_unusable")
            self.assertFalse(failure["error"]["retryable"])
            self.assertFalse(ready_file.exists())

    def test_consumer_readiness_requires_group_and_initial_presence(self):
        with tempfile.TemporaryDirectory() as tmp:
            ready_file = Path(tmp) / "private" / "redis-team.ready.json"
            settings = adapter.RedisTeamSettings(
                enabled=True,
                redis_url="redis://example.invalid:6379/0",
                team_id="117",
                member_id="developer",
                role="developer",
                shared_dir=str(Path(tmp) / "team"),
                ready_file=str(ready_file),
                instance_id=394,
                generation=9,
            )
            commands = []

            class FakeRedis:
                def __init__(self, _redis_url):
                    pass

                async def connect(self):
                    pass

                async def command(self, *args):
                    commands.append(args)
                    return "OK"

                def close(self):
                    pass

            async def run_test():
                with (
                    mock.patch.object(adapter, "load_settings", return_value=settings),
                    mock.patch.object(adapter, "AsyncRedisClient", FakeRedis),
                ):
                    failure_file = adapter._startup_failure_path(ready_file)
                    failure_file.parent.mkdir(parents=True, exist_ok=True)
                    failure_file.write_text('{"state":"failed"}\n', encoding="utf-8")
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                    self.assertTrue(await instance.connect())
                    self.assertTrue(ready_file.is_file())
                    self.assertFalse(failure_file.exists())
                    ready = json.loads(ready_file.read_text(encoding="utf-8"))
                    self.assertTrue(ready["ready"])
                    self.assertEqual(ready["state"], "ready")
                    self.assertEqual(ready["teamId"], "117")
                    self.assertEqual(ready["memberId"], "developer")
                    self.assertEqual(ready["instanceId"], 394)
                    self.assertEqual(ready["generation"], 9)
                    xgroup_index = next(index for index, command in enumerate(commands) if command[:2] == ("XGROUP", "CREATE"))
                    presence_index = next(index for index, command in enumerate(commands) if command[0] == "HSET")
                    self.assertLess(xgroup_index, presence_index)
                    await instance.disconnect()
                    self.assertFalse(ready_file.exists())

            asyncio.run(run_test())

    def test_control_plane_redis_keys_preserve_completion_identity(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            completion_id = "completion:42:team-42-task-7:developer:dev-1:r3"
            attempt_id = "attempt_123"
            self.assertEqual(
                adapter._completion_ack_key(settings, completion_id, attempt_id),
                "claw:team:42:completion-ack:"
                "completion:42:team-42-task-7:developer:dev-1:r3:attempt_123",
            )
            self.assertEqual(
                adapter.assignment_activity_key(
                    settings,
                    "team-42-task-7",
                    "dev-1",
                ),
                "claw:team:42:assignment-activity:team-42-task-7:dev-1",
            )

    def test_member_artifacts_require_active_assignment_and_cannot_escape(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            adapter.ensure_team_dirs(settings)
            with self.assertRaisesRegex(ValueError, "rootTaskId"):
                adapter._artifact_path(settings, {"path": "result.md"}, default_scope="member", write=True)

            adapter._persist_active_envelope(
                settings,
                {
                    "rootTaskId": "team-42-task-7",
                    "assignmentId": "dev-1",
                    "taskId": "team-42-task-7",
                },
            )
            target = adapter._artifact_path(
                settings,
                {"path": "result.md"},
                default_scope="member",
                write=True,
            )
            self.assertEqual(
                adapter.canonical_artifact_ref(settings, target),
                "/team/artifacts/team-42-task-7/members/developer/dev-1/result.md",
            )
            with self.assertRaisesRegex(ValueError, "traversal"):
                adapter._artifact_path(
                    settings,
                    {"path": "../other-team.txt"},
                    default_scope="member",
                    write=True,
                )

    def test_preview_uses_same_persistent_managed_url_contract_as_openclaw(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            target = Path(tmp) / "artifacts" / "team-42-task-7" / "index.html"
            target.parent.mkdir(parents=True)
            target.write_text("<h1>ok</h1>", encoding="utf-8")
            url = adapter._preview_url(settings, target)
            self.assertRegex(
                url,
                r"^http://clawmanager-egress-proxy\.system\.svc\.cluster\.local:3128/v2/interactive/42/",
            )
            self.assertNotIn("expires", url)
            self.assertNotIn("token", url)

    def test_reviewer_member_report_is_nonblocking_copied_to_canonical_review_root(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = adapter.RedisTeamSettings(
                enabled=True,
                redis_url="redis://example.invalid:6379/0",
                team_id="42",
                member_id="reviewer",
                role="reviewer",
                shared_dir=tmp,
            )
            envelope = {
                "rootTaskId": "team-42-task-7",
                "taskId": "team-42-task-7",
                "assignmentId": "review-1",
            }
            source = (
                Path(tmp)
                / "artifacts"
                / "team-42-task-7"
                / "members"
                / "reviewer"
                / "review-1"
                / "QA-REPORT.md"
            )
            source.parent.mkdir(parents=True)
            source.write_text("# QA Report\n\nPASS\n", encoding="utf-8")
            canonical = adapter._canonicalize_reviewer_completion_report(
                settings,
                envelope,
                f"Report: {adapter.canonical_artifact_ref(settings, source)}",
                [],
            )
            self.assertEqual(
                canonical,
                ["/team/results/team-42-task-7/reviews/review-1/QA-REPORT.md"],
            )
            self.assertEqual(
                (
                    Path(tmp)
                    / "results"
                    / "team-42-task-7"
                    / "reviews"
                    / "review-1"
                    / "QA-REPORT.md"
                ).read_text(encoding="utf-8"),
                "# QA Report\n\nPASS\n",
            )
            self.assertTrue(
                source.exists(),
                "the compatibility copy must preserve the original member report",
            )

    def test_validation_report_path_uses_active_assignment_and_stale_reads_recover_uniquely(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = adapter.RedisTeamSettings(
                enabled=True,
                redis_url="redis://example.invalid:6379/0",
                team_id="42",
                member_id="reviewer",
                role="reviewer",
                shared_dir=tmp,
            )
            adapter.ensure_team_dirs(settings)
            adapter._persist_active_envelope(
                settings,
                {
                    "rootTaskId": "team-42-task-7",
                    "taskId": "team-42-task-7",
                    "assignmentId": "review-r2",
                    "validationAssignment": True,
                    "validationTargetAssignmentId": "developer-r1",
                },
            )
            stale_args = {
                "scope": "member",
                "path": "/team/results/team-42-task-7/reviews/developer-r1/QA-REPORT.md",
            }
            effective = adapter._normalize_validation_artifact_write_args(settings, stale_args)
            self.assertEqual(effective["scope"], "team")
            self.assertEqual(
                effective["path"],
                "/team/results/team-42-task-7/reviews/review-r2/QA-REPORT.md",
            )
            target = adapter._artifact_path(settings, effective, default_scope="member", write=True)
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text("# QA\n\nPASS\n", encoding="utf-8")
            recovered = adapter._artifact_read_path_with_fallback(
                settings,
                {
                    "scope": "team",
                    "path": "/team/results/team-42-task-7/reviews/review-old/QA-REPORT.md",
                },
            )
            self.assertEqual(recovered, target)

            duplicate = Path(tmp) / "artifacts" / "team-42-task-7" / "copy" / "QA-REPORT.md"
            duplicate.parent.mkdir(parents=True)
            duplicate.write_text("different report", encoding="utf-8")
            ambiguous = adapter._artifact_read_path_with_fallback(
                settings,
                {
                    "scope": "team",
                    "path": "/team/results/team-42-task-7/reviews/review-old/QA-REPORT.md",
                },
            )
            self.assertNotEqual(ambiguous, target, "ambiguous filenames must never be guessed")

    def test_worker_result_never_overwrites_team_final_result(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            payload = adapter.write_task_result(
                settings,
                "team-42-task-7",
                envelope={
                    "rootTaskId": "team-42-task-7",
                    "assignmentId": "dev-1",
                },
                status="succeeded",
                summary="implementation complete",
                result_markdown="The implementation and static checks are complete.",
            )
            self.assertEqual(payload["artifactRefs"], [])
            self.assertFalse(
                (Path(tmp) / "results" / "team-42-task-7" / "result.md").exists()
            )
            completion_files = list(
                (Path(tmp) / ".hermes-redis-team" / "completions").glob("*.json")
            )
            self.assertEqual(len(completion_files), 1)

    def test_failed_worker_result_gets_assignment_scoped_evidence(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            payload = adapter.write_task_result(
                settings,
                "team-42-task-7",
                envelope={
                    "rootTaskId": "team-42-task-7",
                    "assignmentId": "dev-1",
                },
                status="failed",
                summary="implementation failed",
                result_markdown="The implementation failed because the required input was unavailable.",
            )
            self.assertEqual(
                payload["artifactRefs"],
                [
                    "/team/artifacts/team-42-task-7/members/"
                    "developer/dev-1/failure-result.md"
                ],
            )
            self.assertFalse(
                (Path(tmp) / "results" / "team-42-task-7" / "result.md").exists()
            )

    def test_completion_status_is_bounded_before_redis_publish(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            with self.assertRaisesRegex(ValueError, "completion status"):
                asyncio.run(
                    adapter._propose_completion(
                        settings,
                        {
                            "taskId": "team-42-task-7",
                            "rootTaskId": "team-42-task-7",
                            "assignmentId": "dev-1",
                        },
                        status="finished",
                        summary="invalid status",
                        result_markdown="invalid status must not publish",
                        explicit=True,
                    )
                )

    def test_automatic_result_uses_the_strict_v4_completion_envelope(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            captured = {}

            class FakeRedis:
                def __init__(self, _redis_url):
                    pass

                async def connect(self):
                    pass

                def close(self):
                    pass

            async def fake_publish(_redis, _settings, _key, event):
                captured.update(event)
                return {"published": True, "streamId": "1-0"}

            async def fake_ack(_redis, _settings, _completion_id, _attempt_id):
                return {"decision": "accepted", "reason": "accepted"}

            envelope = {
                "messageId": "msg-1",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "assignmentId": "dev-1",
            }
            adapter._persist_active_envelope(settings, envelope)
            with (
                mock.patch.object(adapter, "AsyncRedisClient", FakeRedis),
                mock.patch.object(adapter, "_publish_once", fake_publish),
                mock.patch.object(adapter, "_completion_ack", fake_ack),
            ):
                asyncio.run(
                    adapter._propose_completion(
                        settings,
                        envelope,
                        status="succeeded",
                        summary="implementation complete",
                        result_markdown="The requested implementation is complete.",
                        explicit=True,
                    )
                )

            event = captured
            self.assertEqual(event["completionSource"], "team_complete_task")
            self.assertTrue(event["explicitCompletion"])
            self.assertNotIn("automaticTurnResult", event)
            self.assertTrue(event["assignmentResultOnly"])
            self.assertFalse(event["rootTaskTerminal"])

    def test_normalized_envelope_preserves_assignment_contract(self):
        value = adapter.normalize_envelope(
            {
                "v": 4,
                "messageId": "msg-1",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "rootMessageId": "root-msg",
                "assignmentId": "dev-1",
                "workId": "dev-1",
                "revision": 3,
                "requiresCompletion": False,
                "turnOutcomePolicy": {
                    "actionExpected": True,
                    "immediateRecoveryAllowed": False,
                },
            }
        )
        self.assertEqual(value["assignmentId"], "dev-1")
        self.assertEqual(value["rootMessageId"], "root-msg")
        self.assertEqual(value["revision"], 3)
        self.assertFalse(value["requiresCompletion"])
        self.assertEqual(value["turnOutcomePolicy"]["actionExpected"], True)
        self.assertEqual(value["turnOutcomePolicy"]["immediateRecoveryAllowed"], False)

    def test_completion_uses_active_envelope_when_agent_reports_wrong_task_id(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            active = {
                "messageId": "msg-active",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "rootMessageId": "root-msg",
                "assignmentId": "dev-1",
                "workId": "dev-1",
                "revision": 2,
            }
            adapter._persist_active_envelope(settings, active)
            captured = {}

            async def fake_propose(_settings, envelope, **_kwargs):
                captured.update(envelope)
                return {"decision": "accepted"}

            with (
                mock.patch.object(adapter, "load_settings", return_value=settings),
                mock.patch.object(adapter, "_propose_completion", fake_propose),
            ):
                result = asyncio.run(
                    adapter._tool_team_complete_task(
                        {"taskId": "team-999-task-999", "status": "succeeded", "summary": "Delivered"}
                    )
                )
            self.assertEqual(json.loads(result)["decision"], "accepted")
            self.assertEqual(captured["rootTaskId"], "team-42-task-7")
            self.assertEqual(captured["taskId"], "team-42-task-7")
            self.assertEqual(captured["assignmentId"], "dev-1")
            self.assertEqual(captured["reportedTaskId"], "team-999-task-999")

    def test_normalized_envelope_rejects_unstable_transport_identity(self):
        self.assertIsNone(adapter.normalize_envelope({"taskId": "team-42-task-7"}))
        self.assertIsNone(adapter.normalize_envelope({"messageId": "msg-1"}))
        self.assertIsNone(adapter.normalize_envelope({"rawPayload": "not-json", "redisId": "1-0"}))

    def test_stream_parser_treats_real_redis_empty_pending_shape_as_empty(self):
        self.assertEqual(adapter._parse_stream_response(None), [])
        self.assertEqual(adapter._parse_stream_response([]), [])
        self.assertEqual(adapter._parse_stream_response([["team-inbox", []]]), [])

    def test_formal_assignment_emits_received_and_started_before_model_work(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            commands = []
            envelope = {
                "messageId": "msg-lifecycle-1",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "rootMessageId": "root-message-7",
                "assignmentId": "dev-1",
                "workId": "dev-1",
                "teamId": "42",
                "from": "leader",
                "to": "developer",
                "text": "Implement the requested artifact.",
                "requiresCompletion": True,
                "redisId": "1-0",
            }

            class FakeRedis:
                async def command(self, *args):
                    commands.append(args)
                    if args[0] == "GET":
                        return None
                    if args[0] == "XADD":
                        return f"{len(commands)}-0"
                    return "OK"

                def close(self):
                    pass

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                instance._redis = FakeRedis()
                instance.handle_message = mock.AsyncMock()
                await instance._handle_redis_message(envelope)
                instance.handle_message.assert_awaited_once()
                self.assertIn(("team-42-task-7", "dev-1"), instance._active_assignments)

            asyncio.run(run_test())
            events = [
                json.loads(command[-1])
                for command in commands
                if command[0] == "XADD" and command[1] == adapter.events_key(settings)
            ]
            lifecycle = [event for event in events if event.get("event") in {"task_received", "task_started"}]
            self.assertEqual([event["event"] for event in lifecycle], ["task_received", "task_started"])
            self.assertEqual(lifecycle[0]["eventId"], "assignment-lifecycle:msg-lifecycle-1:task_received")
            self.assertEqual(lifecycle[1]["eventId"], "assignment-lifecycle:msg-lifecycle-1:task_started")
            self.assertTrue(all(event["visibleToChat"] is False for event in lifecycle))
            status = adapter.read_team_statuses(settings, settings.member_id)
            self.assertEqual(status["availability"], "busy")
            self.assertEqual(status["runtimeStatus"], "running")
            self.assertEqual(status["currentAssignmentId"], "dev-1")

    def test_context_only_turn_preserves_formal_assignment_and_emits_no_start_events(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            commands = []
            formal = {
                "messageId": "formal-1",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "assignmentId": "dev-1",
                "workId": "dev-1",
                "teamId": "42",
                "from": "leader",
                "to": "developer",
                "requiresCompletion": True,
            }
            context = {
                "messageId": "context-1",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "teamId": "42",
                "from": "leader",
                "to": "developer",
                "intent": "context",
                "text": "Additional context only.",
                "requiresCompletion": False,
                "redisId": "2-0",
            }

            class FakeRedis:
                async def command(self, *args):
                    commands.append(args)
                    if args[0] == "GET":
                        return None
                    return "OK"

                def close(self):
                    pass

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                instance._redis = FakeRedis()
                adapter._persist_active_envelope(settings, formal)
                instance._track_active_assignment(formal)
                dispatch = mock.AsyncMock()
                instance._dispatch_envelope = dispatch
                await instance._handle_redis_message(context)
                dispatch.assert_awaited_once_with(mock.ANY, context_only=True)
                self.assertEqual(adapter._load_active_envelope(settings)["messageId"], "formal-1")

            asyncio.run(run_test())
            events = [
                json.loads(command[-1])
                for command in commands
                if command[0] == "XADD" and command[1] == adapter.events_key(settings)
            ]
            self.assertFalse(any(event.get("event") in {"task_received", "task_started"} for event in events))

    def test_context_turn_observer_is_state_neutral_and_conflict_safe(self):
        envelope = {
            "messageId": "context-observe-1",
            "requiresCompletion": False,
            "intent": "member_result_confirmed",
            "turnOutcomePolicy": {
                "actionExpected": True,
                "immediateRecoveryAllowed": True,
            },
        }
        retryable = adapter._observe_team_turn_outcome(envelope, {}, {
            "lastTool": {
                "toolName": "team_send",
                "failed": True,
                "retryable": True,
                "succeeded": False,
                "code": "ambiguous_team_target",
            },
        })
        self.assertEqual(retryable["outcome"], "retryable_tool_gap")
        self.assertTrue(retryable["immediateRecoveryEligible"])
        conflict = adapter._observe_team_turn_outcome(envelope, {"explicitCompletionSubmitted": True}, {
            "lastTool": {
                "toolName": "team_send",
                "failed": True,
                "retryable": True,
                "succeeded": False,
            },
        })
        self.assertEqual(conflict["outcome"], "runtime_observation_unknown")
        self.assertFalse(conflict["immediateRecoveryEligible"])
        ordinary = adapter._observe_team_turn_outcome(
            {"messageId": "context-observe-2", "requiresCompletion": False, "intent": "context"},
            {},
            {},
        )
        self.assertEqual(ordinary["outcome"], "ordinary_open_turn")
        self.assertFalse(ordinary["immediateRecoveryEligible"])

    def test_turn_observer_distinguishes_leader_return_from_downstream_assignment(self):
        envelope = {
            "messageId": "worker-return-1",
            "requiresCompletion": True,
            "intent": "assignment",
            "turnOutcomePolicy": {
                "actionExpected": True,
                "immediateRecoveryAllowed": True,
            },
        }
        leader_return = adapter._observe_team_turn_outcome(envelope, {}, {
            "lastTool": {
                "toolName": "team_send",
                "failed": False,
                "retryable": False,
                "succeeded": True,
                "outboundObserved": True,
                "businessMutation": False,
                "businessDeliveryKind": "peer_request",
                "outboundTarget": "leader",
            },
        })
        self.assertEqual(leader_return["outcome"], "completion_receipt_gap")
        self.assertTrue(leader_return["immediateRecoveryEligible"])
        self.assertTrue(leader_return["hadOutboundAssignment"])
        self.assertFalse(leader_return["downstreamAssignmentStarted"])

        downstream = adapter._observe_team_turn_outcome(envelope, {}, {
            "lastTool": {
                "toolName": "team_send",
                "failed": False,
                "retryable": False,
                "succeeded": True,
                "outboundObserved": True,
                "businessMutation": True,
                "businessDeliveryKind": "assignment",
                "outboundTarget": "worker-2",
            },
        })
        self.assertEqual(downstream["outcome"], "legitimate_wait")
        self.assertFalse(downstream["immediateRecoveryEligible"])
        self.assertTrue(downstream["downstreamAssignmentStarted"])

    def test_context_processing_emits_actionable_hidden_turn_fact_without_business_state(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            commands = []
            formal = {
                "messageId": "formal-active",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "assignmentId": "leader-final-synthesis",
                "workId": "leader-final-synthesis",
                "requiresCompletion": True,
            }
            context = {
                "messageId": "context-actionable",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "assignmentId": "leader-final-synthesis",
                "workId": "leader-final-synthesis",
                "requiresCompletion": False,
                "intent": "member_result_confirmed",
                "turnOutcomePolicy": {
                    "actionExpected": True,
                    "immediateRecoveryAllowed": True,
                },
            }

            class FakeRedis:
                async def command(self, *args):
                    commands.append(args)
                    if args[0] == "XADD":
                        return "1-0"
                    return None

                def close(self):
                    pass

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                instance._redis = FakeRedis()
                adapter._persist_active_envelope(settings, formal)
                instance._track_active_assignment(formal)
                adapter._begin_turn_observation(settings, context, context_only=True)
                adapter._record_turn_tool_result(settings, "team_send", {
                    "ok": False,
                    "retryable": True,
                    "code": "ambiguous_team_target",
                    "error": "Recipient is ambiguous",
                    "candidates": ["developer", "reviewer"],
                })
                event = types.SimpleNamespace(
                    raw_message=context,
                    message_id=context["messageId"],
                    source=types.SimpleNamespace(chat_id=context["taskId"]),
                )
                await instance.on_processing_complete(event, adapter.ProcessingOutcome.SUCCESS)

            asyncio.run(run_test())
            events = [
                json.loads(command[-1])
                for command in commands
                if command[0] == "XADD" and command[1] == adapter.events_key(settings)
            ]
            observed = [event for event in events if event.get("eventKind") == "turn_finished_without_completion"]
            self.assertEqual(len(observed), 1)
            self.assertEqual(observed[0]["turnObservationOutcome"], "retryable_tool_gap")
            self.assertTrue(observed[0]["immediateRecoveryEligible"])
            self.assertEqual(observed[0]["stateEffect"], "none")
            self.assertFalse(observed[0]["rootTaskTerminal"])
            self.assertFalse(any(event.get("event") in {"task_received", "task_started", "task_completed"} for event in events))

    def test_terminal_root_rejects_late_assignment_before_model_dispatch(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            commands = []
            envelope = {
                "messageId": "late-assignment",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "assignmentId": "dev-1",
                "workId": "dev-1",
                "teamId": "42",
                "from": "leader",
                "to": "developer",
                "text": "This assignment arrived after root completion.",
                "requiresCompletion": True,
                "redisId": "3-0",
            }

            class FakeRedis:
                async def command(self, *args):
                    commands.append(args)
                    if args[0] == "GET":
                        if args[1] == adapter.root_workflow_state_key(settings, envelope["rootTaskId"]):
                            return json.dumps({"terminal": True, "status": "succeeded"})
                        return None
                    return "OK"

                def close(self):
                    pass

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                instance._redis = FakeRedis()
                dispatch = mock.AsyncMock()
                instance._dispatch_envelope = dispatch
                await instance._handle_redis_message(envelope)
                dispatch.assert_not_awaited()
                self.assertFalse(instance._active_assignments)

            asyncio.run(run_test())
            events = [
                json.loads(command[-1])
                for command in commands
                if command[0] == "XADD" and command[1] == adapter.events_key(settings)
            ]
            self.assertTrue(any(event.get("event") == "late_assignment_ignored" for event in events))
            self.assertFalse(any(event.get("event") in {"task_received", "task_started"} for event in events))

    def test_root_terminal_race_after_receive_clears_busy_assignment(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            commands = []
            envelope = {
                "messageId": "terminal-race",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "assignmentId": "dev-1",
                "workId": "dev-1",
                "teamId": "42",
                "from": "leader",
                "to": "developer",
                "text": "Assignment races with root completion.",
                "requiresCompletion": True,
                "redisId": "4-0",
            }

            class FakeRedis:
                async def command(self, *args):
                    commands.append(args)
                    if args[0] == "GET":
                        return None
                    return "OK"

                def close(self):
                    pass

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                instance._redis = FakeRedis()
                instance.handle_message = mock.AsyncMock()
                with mock.patch.object(
                    adapter,
                    "_root_task_is_terminal",
                    new=mock.AsyncMock(side_effect=[False, True]),
                ):
                    await instance._handle_redis_message(envelope)
                instance.handle_message.assert_not_awaited()
                self.assertFalse(instance._active_assignments)

            asyncio.run(run_test())
            status = adapter.read_team_statuses(settings, settings.member_id)
            self.assertEqual(status["availability"], "idle")
            self.assertEqual(status["runtimeStatus"], "idle")
            self.assertTrue(adapter._load_active_envelope(settings)["terminal"])
            events = [
                json.loads(command[-1])
                for command in commands
                if command[0] == "XADD" and command[1] == adapter.events_key(settings)
            ]
            self.assertTrue(any(event.get("event") == "task_received" for event in events))
            self.assertTrue(any(event.get("event") == "late_assignment_ignored" for event in events))
            self.assertFalse(any(event.get("event") == "task_started" for event in events))

    def test_narrative_preserves_running_assignment_state(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            commands = []
            formal = {
                "messageId": "formal-1",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "assignmentId": "dev-1",
                "workId": "dev-1",
                "requiresCompletion": True,
            }

            class FakeRedis:
                async def command(self, *args):
                    commands.append(args)
                    if args[0] == "SET":
                        return "OK"
                    if args[0] == "XADD":
                        return "1-0"
                    return None

                def close(self):
                    pass

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                instance._redis = FakeRedis()
                adapter._persist_active_envelope(settings, formal)
                adapter.write_local_status(
                    settings,
                    {
                        "availability": "busy",
                        "runtimeStatus": "running",
                        "currentTaskId": formal["taskId"],
                        "currentAssignmentId": formal["assignmentId"],
                    },
                )
                instance._track_active_assignment(formal)
                result = await instance.send(
                    formal["taskId"],
                    "Implementation is still progressing.",
                    metadata={"source_message_id": formal["messageId"]},
                )
                self.assertTrue(result.success)

            asyncio.run(run_test())
            status = adapter.read_team_statuses(settings, settings.member_id)
            self.assertEqual(status["availability"], "busy")
            self.assertEqual(status["runtimeStatus"], "running")
            replies = [
                json.loads(command[-1])
                for command in commands
                if command[0] == "XADD" and command[1] == adapter.events_key(settings)
            ]
            self.assertEqual(len(replies), 1)
            self.assertFalse(replies[0]["visibleToChat"])
            self.assertEqual(replies[0]["chatPolicy"], "hidden")
            self.assertEqual(replies[0]["stateEffect"], "none")

    def test_terminal_assignment_keeps_all_raw_narratives_internal(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            commands = []
            formal = {
                "messageId": "formal-terminal",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "assignmentId": "dev-1",
                "workId": "dev-1",
                "requiresCompletion": True,
                "terminal": True,
                "terminalStatus": "succeeded",
            }

            class FakeRedis:
                async def command(self, *args):
                    commands.append(args)
                    if args[0] == "SET":
                        return "OK"
                    if args[0] == "XADD":
                        return "1-0"
                    return None

                def close(self):
                    pass

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                instance._redis = FakeRedis()
                adapter._persist_active_envelope(settings, formal)
                adapter.write_local_status(
                    settings,
                    {
                        "availability": "idle",
                        "runtimeStatus": "succeeded",
                        "currentTaskId": formal["taskId"],
                        "currentAssignmentId": formal["assignmentId"],
                        "lastSummary": "Accepted implementation result",
                    },
                )
                result = await instance.send(
                    formal["taskId"],
                    "Final implementation report with verified artifacts.",
                    metadata={"source_message_id": formal["messageId"]},
                )
                self.assertTrue(result.success)
                late = await instance.send(
                    formal["taskId"],
                    "Internal post-completion bookkeeping.",
                    metadata={"source_message_id": formal["messageId"]},
                )
                self.assertTrue(late.success)

            asyncio.run(run_test())
            status = adapter.read_team_statuses(settings, settings.member_id)
            self.assertEqual(status["availability"], "idle")
            self.assertEqual(status["runtimeStatus"], "succeeded")
            self.assertEqual(status["lastSummary"], "Accepted implementation result")
            replies = [
                json.loads(command[-1])
                for command in commands
                if command[0] == "XADD" and command[1] == adapter.events_key(settings)
            ]
            self.assertEqual(len(replies), 2)
            self.assertFalse(replies[0]["visibleToChat"])
            self.assertEqual(replies[0]["chatPolicy"], "hidden")
            self.assertTrue(replies[0]["terminalDelivery"])
            self.assertIsNone(replies[0].get("suppressedAfterTerminal"))
            self.assertFalse(replies[1]["visibleToChat"])
            self.assertEqual(replies[1]["chatPolicy"], "hidden")
            self.assertTrue(replies[1]["lateProjection"])
            self.assertTrue(replies[1]["suppressedAfterTerminal"])

    def test_failed_terminal_delivery_audit_projection_remains_retryable_and_hidden(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            commands = []
            formal = {
                "messageId": "formal-terminal-retry",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "assignmentId": "dev-1",
                "workId": "dev-1",
                "requiresCompletion": True,
                "terminal": True,
                "terminalStatus": "succeeded",
            }

            class FakeRedis:
                def __init__(self):
                    self.fail_once = True

                async def command(self, *args):
                    commands.append(args)
                    if args[0] == "XADD" and self.fail_once:
                        self.fail_once = False
                        raise ConnectionError("projection interrupted")
                    if args[0] == "XADD":
                        return "2-0"
                    return None

                def close(self):
                    pass

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                instance._redis = FakeRedis()
                adapter._persist_active_envelope(settings, formal)
                adapter.write_local_status(
                    settings,
                    {
                        "availability": "idle",
                        "runtimeStatus": "succeeded",
                        "currentTaskId": formal["taskId"],
                        "currentAssignmentId": formal["assignmentId"],
                    },
                )
                first = await instance.send(
                    formal["taskId"],
                    "Final verified delivery.",
                    metadata={"source_message_id": formal["messageId"]},
                )
                self.assertFalse(first.success)
                self.assertFalse(adapter._load_active_envelope(settings).get("terminalNarrativePublished"))
                retry = await instance.send(
                    formal["taskId"],
                    "Final verified delivery.",
                    metadata={"source_message_id": formal["messageId"]},
                )
                self.assertTrue(retry.success)
                self.assertTrue(adapter._load_active_envelope(settings)["terminalNarrativePublished"])

            asyncio.run(run_test())
            published = [
                json.loads(command[-1])
                for command in commands
                if command[0] == "XADD" and len(command) >= 5 and command[1] == adapter.events_key(settings)
            ]
            self.assertEqual(len(published), 2)
            self.assertTrue(all(not event["visibleToChat"] for event in published))
            self.assertTrue(all(event["chatPolicy"] == "hidden" for event in published))
            self.assertEqual(published[0]["eventId"], published[1]["eventId"])

    def test_turn_without_completion_keeps_assignment_active_for_monitoring(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            formal = {
                "messageId": "formal-no-result",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "assignmentId": "dev-1",
                "workId": "dev-1",
                "requiresCompletion": True,
            }

            class FakeRedis:
                async def command(self, *args):
                    if args[0] == "XADD":
                        return "1-0"
                    return None

                def close(self):
                    pass

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                instance._redis = FakeRedis()
                adapter._persist_active_envelope(settings, formal)
                instance._track_active_assignment(formal)
                instance._accepted_messages[formal["messageId"]] = adapter._assignment_identity(formal)
                event = types.SimpleNamespace(
                    raw_message=formal,
                    message_id=formal["messageId"],
                    source=types.SimpleNamespace(chat_id=formal["taskId"]),
                )
                await instance.on_processing_complete(event, adapter.ProcessingOutcome.SUCCESS)
                self.assertNotIn(formal["messageId"], instance._accepted_messages)
                self.assertIn(adapter._assignment_identity(formal), instance._active_assignments)

            asyncio.run(run_test())
            status = adapter.read_team_statuses(settings, settings.member_id)
            self.assertEqual(status["availability"], "busy")
            self.assertEqual(status["runtimeStatus"], "awaiting_completion_receipt")

    def test_consumer_switches_from_empty_pending_to_new_messages(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            reads = []
            handled = []

            class FakeRedis:
                async def command(self, *args):
                    reads.append(args)
                    read_id = args[-1]
                    if read_id == "0":
                        return [[adapter.inbox_key(settings), []]]
                    return [
                        [
                            adapter.inbox_key(settings),
                            [["2-0", ["payload", json.dumps({"messageId": "msg-2", "taskId": "task-2"})]]],
                        ]
                    ]

                def close(self):
                    pass

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                instance._consumer_redis = FakeRedis()
                instance.is_connected = True

                async def handle(raw):
                    handled.append(raw["messageId"])
                    instance.is_connected = False

                instance._handle_redis_message = handle
                await instance._consumer_loop()

            asyncio.run(run_test())
            self.assertEqual(handled, ["msg-2"])
            self.assertEqual(reads[0][-1], "0")
            self.assertNotIn("BLOCK", reads[0])
            self.assertEqual(reads[1][-1], ">")
            self.assertIn("BLOCK", reads[1])

    def test_consumer_recovers_pending_before_new_messages(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            pending_reads = 0
            handled = []

            class FakeRedis:
                async def command(self, *args):
                    nonlocal pending_reads
                    read_id = args[-1]
                    if read_id == "0":
                        pending_reads += 1
                        if pending_reads == 1:
                            return [
                                [
                                    adapter.inbox_key(settings),
                                    [["1-0", ["payload", json.dumps({"messageId": "pending", "taskId": "task-1"})]]],
                                ]
                            ]
                        return [[adapter.inbox_key(settings), []]]
                    return [
                        [
                            adapter.inbox_key(settings),
                            [["2-0", ["payload", json.dumps({"messageId": "new", "taskId": "task-2"})]]],
                        ]
                    ]

                def close(self):
                    pass

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                instance._consumer_redis = FakeRedis()
                instance.is_connected = True

                async def handle(raw):
                    handled.append(raw["messageId"])
                    if len(handled) == 2:
                        instance.is_connected = False

                instance._handle_redis_message = handle
                await instance._consumer_loop()

            asyncio.run(run_test())
            self.assertEqual(handled, ["pending", "new"])

    def test_redis_reconnect_swaps_both_clients_and_restores_readiness(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))

            class FakeRedis:
                def __init__(self):
                    self.closed = False

                def close(self):
                    self.closed = True

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                old_presence = FakeRedis()
                old_consumer = FakeRedis()
                new_presence = FakeRedis()
                new_consumer = FakeRedis()
                instance._redis = old_presence
                instance._consumer_redis = old_consumer
                instance.is_connected = True
                adapter.write_local_status(
                    settings,
                    {"availability": "running", "runtimeStatus": "running"},
                )
                status = adapter.read_team_statuses(settings, settings.member_id)
                with (
                    mock.patch.object(
                        instance,
                        "_open_redis_clients",
                        new=mock.AsyncMock(return_value=(new_presence, new_consumer, status)),
                    ),
                    mock.patch.object(adapter, "_publish_ready_file") as publish_ready,
                ):
                    self.assertTrue(await instance._reconnect_redis_clients(old_consumer))
                    publish_ready.assert_called_once()
                self.assertIs(instance._redis, new_presence)
                self.assertIs(instance._consumer_redis, new_consumer)
                self.assertTrue(old_presence.closed)
                self.assertTrue(old_consumer.closed)

            asyncio.run(run_test())

    def test_completing_one_message_keeps_parallel_assignment_turn_accepted(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                identity = ("team-42-task-7", "dev-1")
                instance._accepted_messages = {"msg-1": identity, "msg-2": identity}
                event = types.SimpleNamespace(
                    raw_message={
                        "messageId": "msg-1",
                        "taskId": identity[0],
                        "rootTaskId": identity[0],
                        "assignmentId": identity[1],
                    },
                    message_id="msg-1",
                    source=types.SimpleNamespace(chat_id=identity[0]),
                )
                with mock.patch.object(instance, "_on_processing_complete_inner", new=mock.AsyncMock()):
                    await instance.on_processing_complete(event, adapter.ProcessingOutcome.SUCCESS)
                self.assertNotIn("msg-1", instance._accepted_messages)
                self.assertEqual(instance._accepted_messages["msg-2"], identity)

            asyncio.run(run_test())

    def test_redis_finalize_retry_never_dispatches_the_same_turn_twice(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            dispatches = []

            class FakeRedis:
                def __init__(self):
                    self.fail_processed_set = True

                async def command(self, *args):
                    if args[0] == "GET":
                        return None
                    if args[0] == "SET" and self.fail_processed_set:
                        self.fail_processed_set = False
                        raise ConnectionError("connection dropped before processed marker")
                    if args[0] == "XADD":
                        return "1-0"
                    return "OK"

                def close(self):
                    pass

            envelope = {
                "messageId": "msg-transport-retry",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "assignmentId": "dev-1",
                "teamId": "42",
                "from": "leader",
                "to": "developer",
                "text": "Implement the requested artifact.",
                "redisId": "7-0",
            }

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                instance._redis = FakeRedis()

                async def dispatch(value, **_kwargs):
                    dispatches.append(value["messageId"])

                instance._dispatch_envelope = dispatch
                with self.assertRaises(ConnectionError):
                    await instance._handle_redis_message(envelope)
                self.assertIn(envelope["messageId"], instance._transport_accepted_messages)
                await instance._handle_redis_message(envelope)
                self.assertNotIn(envelope["messageId"], instance._transport_accepted_messages)

            asyncio.run(run_test())
            self.assertEqual(dispatches, ["msg-transport-retry"])

    def test_invalid_inbound_message_is_dlqed_and_acked(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            commands = []

            class FakeRedis:
                async def command(self, *args):
                    commands.append(args)
                    return "OK"

                def close(self):
                    pass

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                instance._redis = FakeRedis()
                await instance._handle_redis_message({"redisId": "9-0", "rawPayload": "not-json"})

            asyncio.run(run_test())
            self.assertTrue(any(command[0] == "XADD" and command[1] == adapter.dlq_key(settings) for command in commands))
            self.assertTrue(any(command[0] == "XACK" and command[-1] == "9-0" for command in commands))

    def test_backlogged_monitor_observes_active_assignment_without_dispatch(self):
        with tempfile.TemporaryDirectory() as tmp:
            settings = self.settings(Path(tmp))
            commands = []
            dispatched = []
            formal = {
                "messageId": "assignment-1",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "assignmentId": "dev-1",
                "workId": "dev-1",
                "teamId": "42",
                "from": "leader",
                "to": "developer",
                "text": "Implement the requested artifact.",
            }
            monitor = {
                "messageId": "monitor-1",
                "taskId": "team-42-task-7",
                "rootTaskId": "team-42-task-7",
                "assignmentId": "dev-1",
                "workId": "dev-1",
                "teamId": "42",
                "from": "clawmanager-monitor",
                "to": "developer",
                "intent": "assignment_status_check",
                "requiresCompletion": False,
                "metadata": {"monitorType": "assignment_status_check", "checkId": "monitor-1"},
            }

            class FakeRedis:
                async def command(self, *args):
                    commands.append(args)
                    if args[0] == "GET":
                        return None
                    return "OK"

                def close(self):
                    pass

            async def run_test():
                with mock.patch.object(adapter, "load_settings", return_value=settings):
                    instance = adapter.RedisTeamAdapter(types.SimpleNamespace(extra={}))
                instance._redis = FakeRedis()

                async def dispatch(envelope, **_kwargs):
                    dispatched.append(envelope["messageId"])
                    instance._accepted_messages[envelope["messageId"]] = adapter._assignment_identity(envelope)
                    adapter.write_local_status(
                        settings,
                        {
                            "availability": "running",
                            "runtimeStatus": "running",
                            "currentTaskId": envelope["taskId"],
                            "currentAssignmentId": envelope["assignmentId"],
                        },
                    )

                instance._dispatch_envelope = dispatch
                await instance._handle_redis_message({**formal, "redisId": "1-0"})
                active_before = adapter._load_active_envelope(settings)
                await instance._handle_redis_message({**monitor, "redisId": "2-0"})
                active_after = adapter._load_active_envelope(settings)

                self.assertEqual(active_before["messageId"], "assignment-1")
                self.assertEqual(active_after["messageId"], "assignment-1")

            asyncio.run(run_test())
            self.assertEqual(dispatched, ["assignment-1", "monitor-1"])
            monitor_events = []
            for command in commands:
                if command[0] == "XADD" and command[1] == adapter.events_key(settings):
                    payload = json.loads(command[-1])
                    if payload.get("eventKind") == "assignment_check_result":
                        monitor_events.append(payload)
            self.assertEqual(len(monitor_events), 0, "a non-terminal Monitor must reach the model instead of being answered mechanically")

if __name__ == "__main__":
    unittest.main()
