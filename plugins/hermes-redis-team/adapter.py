"""Redis Team platform adapter for ClawManager-managed Hermes runtimes."""

from __future__ import annotations

import asyncio
import base64
import hashlib
import hmac
import json
import logging
import os
import re
import ssl
import stat
import time
import unicodedata
import uuid
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Dict, List, Optional
from urllib.parse import quote, unquote, urlparse

from gateway.config import Platform, PlatformConfig
from gateway.platforms.base import (
    BasePlatformAdapter,
    MessageEvent,
    MessageType,
    ProcessingOutcome,
    SendResult,
)
from gateway.session import SessionSource

logger = logging.getLogger(__name__)

WIRE_SCHEMA_VERSION = 1
PROTOCOL_VERSION = 4
PROTOCOL_CAPABILITIES = [
    "completion_ack_v1",
    "explicit_completion_receipt_v1",
    "turn_end_monitor_v1",
    "turn_outcome_v1",
    "immediate_recheck_v1",
    "assignment_lifecycle_v1",
    "assignment_heartbeat_v1",
    "assignment_activity_v2",
    "durable_turn_facts_v1",
    "terminal_tool_stop_v1",
    "team_artifact_preview_v1",
    "team_artifact_preview_v2",
    "review_contract_v1",
    "validation_contract_v2",
]
COMPLETION_SOURCE = "team_complete_task"
DEFAULT_SHARED_DIR = "/team"
DEFAULT_CONSUMER_GROUP = "team-members"
READ_BLOCK_MS = 5000
STATUS_INTERVAL_SECONDS = 30
PENDING_DRAIN_BATCH_LIMIT = 3
REDIS_RECONNECT_BACKOFF_SECONDS = (1, 2, 5, 10, 30)
TEAM_SHARED_DIR_MODE = 0o2775
TEAM_SHARED_FILE_MODE = 0o664
MAX_ARTIFACT_BYTES = 1024 * 1024
TURN_TOOL_TRACE_LIMIT = 24
TEAM_CONTROL_TOOLS = {"team_send", "team_update_progress", "team_complete_task"}


def _truthy(value: Any, default: bool = False) -> bool:
    if value is None:
        return default
    if isinstance(value, bool):
        return value
    if isinstance(value, str):
        value = value.strip().lower()
        if value in {"1", "true", "yes", "on"}:
            return True
        if value in {"0", "false", "no", "off"}:
            return False
    return bool(value)


def _trim(value: Any) -> str:
    return str(value).strip() if value is not None else ""


def _now_iso() -> str:
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


def _safe_name(value: str) -> str:
    safe = re.sub(r"[^A-Za-z0-9._-]+", "_", value).strip("._")
    return safe or "unknown"


def _redis_key_part(value: Any) -> str:
    raw = _trim(value)
    if not raw:
        return "unknown"
    return re.sub(r"[^A-Za-z0-9_:.-]", "-", raw)


def _redis_client_name(settings: "RedisTeamSettings", purpose: str) -> str:
    return f"redis-team:{_safe_name(settings.team_id)}:{_safe_name(settings.member_id)}:{purpose}"[:512]


def _short_text(value: str, limit: int = 500) -> str:
    text = _trim(value)
    if len(text) <= limit:
        return text
    return text[: max(0, limit - 3)] + "..."


def _effective_access(path: Path, mode: int) -> bool:
    try:
        return os.access(path, mode, effective_ids=True)
    except (NotImplementedError, TypeError):
        return os.access(path, mode)


def _shared_directory_error(path: Path, detail: str) -> PermissionError:
    try:
        info = path.lstat()
        owner = f"uid={info.st_uid} gid={info.st_gid} mode={oct(stat.S_IMODE(info.st_mode))}"
    except OSError as exc:
        owner = f"stat_error={exc}"
    effective_uid = os.geteuid() if hasattr(os, "geteuid") else -1
    effective_gid = os.getegid() if hasattr(os, "getegid") else -1
    groups = ",".join(str(value) for value in os.getgroups()) if hasattr(os, "getgroups") else ""
    return PermissionError(
        f"Team shared directory is unusable: {path} ({detail}; {owner}; "
        f"euid={effective_uid} egid={effective_gid} groups={groups})"
    )


def _validate_shared_directory(path: Path) -> None:
    try:
        info = path.lstat()
    except OSError as exc:
        raise _shared_directory_error(path, f"unable to inspect directory: {exc}") from exc
    if stat.S_ISLNK(info.st_mode):
        raise _shared_directory_error(path, "symbolic links are not allowed")
    if not stat.S_ISDIR(info.st_mode):
        raise _shared_directory_error(path, "path is not a directory")
    if not _effective_access(path, os.R_OK | os.W_OK | os.X_OK):
        raise _shared_directory_error(path, "current Worker lacks read/write/execute access")


def _ensure_shared_directory(path: Path) -> None:
    """Create a cooperative Team directory without chmod-ing foreign NFS owners."""

    try:
        info = path.lstat()
    except FileNotFoundError:
        info = None
    except OSError as exc:
        raise _shared_directory_error(path, f"unable to inspect directory: {exc}") from exc

    if info is not None:
        _validate_shared_directory(path)
        return

    parent = path.parent
    if parent == path:
        raise _shared_directory_error(path, "directory has no creatable parent")
    _ensure_shared_directory(parent)

    created = False
    try:
        path.mkdir(mode=TEAM_SHARED_DIR_MODE)
        created = True
    except FileExistsError:
        # Another Team member may have won the creation race.
        pass
    except OSError as exc:
        raise _shared_directory_error(path, f"unable to create directory: {exc}") from exc

    if created:
        try:
            path.chmod(TEAM_SHARED_DIR_MODE)
        except OSError as exc:
            raise _shared_directory_error(path, f"unable to set newly-created directory permissions: {exc}") from exc
    _validate_shared_directory(path)


def _atomic_write_json(path: Path, value: Any) -> None:
    _ensure_shared_directory(path.parent)
    tmp = path.with_name(f"{path.name}.{os.getpid()}.{int(time.time() * 1000)}.{uuid.uuid4().hex}.tmp")
    tmp.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    tmp.chmod(TEAM_SHARED_FILE_MODE)
    tmp.replace(path)
    path.chmod(TEAM_SHARED_FILE_MODE)


def _atomic_write_text(path: Path, value: str) -> None:
    _ensure_shared_directory(path.parent)
    tmp = path.with_name(f"{path.name}.{os.getpid()}.{int(time.time() * 1000)}.{uuid.uuid4().hex}.tmp")
    tmp.write_text(value, encoding="utf-8")
    tmp.chmod(TEAM_SHARED_FILE_MODE)
    tmp.replace(path)
    path.chmod(TEAM_SHARED_FILE_MODE)


def _read_json(path: Path) -> Any:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except Exception:
        return None


@dataclass(frozen=True)
class RedisTeamSettings:
    enabled: bool
    redis_url: str
    team_id: str
    member_id: str
    role: str = "member"
    shared_dir: str = DEFAULT_SHARED_DIR
    auto_run: bool = True
    consumer_group: str = DEFAULT_CONSUMER_GROUP
    embedded_timeout_seconds: int = 1800
    manager_url: str = ""
    preview_origin: str = ""
    team_token: str = ""
    ready_file: str = ""
    instance_id: int = 0
    generation: int = 0

    @property
    def shared_path(self) -> Path:
        return Path(self.shared_dir)

    @property
    def valid(self) -> bool:
        return bool(self.enabled and self.redis_url and self.team_id and self.member_id)


def load_settings(config: PlatformConfig | None = None) -> RedisTeamSettings:
    extra = dict(getattr(config, "extra", {}) or {})
    from_env = _truthy(extra.get("from_env"), True)

    def pick(key: str, env_name: str, default: Any = "", *aliases: str) -> Any:
        for candidate in (key, *aliases):
            value = extra.get(candidate)
            if value not in (None, ""):
                return value
        if from_env:
            env_value = os.getenv(env_name)
            if env_value not in (None, ""):
                return env_value
        return default

    timeout_raw = pick(
        "embedded_timeout_seconds",
        "CLAWMANAGER_TEAM_EMBEDDED_TIMEOUT_SECONDS",
        1800,
        "embeddedTimeoutSeconds",
    )
    try:
        timeout = int(timeout_raw)
    except (TypeError, ValueError):
        timeout = 1800
    instance_id_raw = pick("instance_id", "CLAWMANAGER_INSTANCE_ID", 0, "instanceId")
    generation_raw = pick("generation", "CLAWMANAGER_GATEWAY_GENERATION", 0)
    try:
        instance_id = int(instance_id_raw)
    except (TypeError, ValueError):
        instance_id = 0
    try:
        generation = int(generation_raw)
    except (TypeError, ValueError):
        generation = 0

    return RedisTeamSettings(
        enabled=_truthy(pick("enabled", "CLAWMANAGER_TEAM_ENABLED", False), False),
        redis_url=_trim(pick("redis_url", "CLAWMANAGER_TEAM_REDIS_URL", "", "redisUrl")),
        team_id=_trim(pick("team_id", "CLAWMANAGER_TEAM_ID", "", "teamId")),
        member_id=_trim(pick("member_id", "CLAWMANAGER_TEAM_MEMBER_ID", "", "memberId")),
        role=_trim(pick("role", "CLAWMANAGER_TEAM_ROLE", "member")) or "member",
        shared_dir=_trim(pick("shared_dir", "CLAWMANAGER_TEAM_SHARED_DIR", DEFAULT_SHARED_DIR, "sharedDir"))
        or DEFAULT_SHARED_DIR,
        auto_run=_truthy(pick("auto_run", "CLAWMANAGER_TEAM_AUTORUN", True, "autoRun"), True),
        consumer_group=_trim(pick("consumer_group", "CLAWMANAGER_TEAM_CONSUMER_GROUP", DEFAULT_CONSUMER_GROUP, "consumerGroup"))
        or DEFAULT_CONSUMER_GROUP,
        embedded_timeout_seconds=max(1, timeout),
        manager_url=_trim(pick("manager_url", "CLAWMANAGER_TEAM_MANAGER_URL", "", "managerUrl")),
        preview_origin=_trim(pick("preview_origin", "CLAWMANAGER_TEAM_PREVIEW_ORIGIN", "", "previewOrigin")),
        team_token=_trim(pick("team_token", "CLAWMANAGER_TEAM_TOKEN", "", "teamToken")),
        ready_file=_trim(pick("ready_file", "CLAWMANAGER_TEAM_READY_FILE", "", "readyFile")),
        instance_id=instance_id,
        generation=generation,
    )


def _key_prefix(settings: RedisTeamSettings) -> str:
    return f"claw:team:{settings.team_id}"


def inbox_key(settings: RedisTeamSettings, member_id: Optional[str] = None) -> str:
    return f"{_key_prefix(settings)}:inbox:{member_id or settings.member_id}"


def events_key(settings: RedisTeamSettings) -> str:
    return f"{_key_prefix(settings)}:events"


def presence_key(settings: RedisTeamSettings) -> str:
    return f"{_key_prefix(settings)}:presence"


def dlq_key(settings: RedisTeamSettings) -> str:
    return f"{_key_prefix(settings)}:dlq"


def assignment_activity_key(settings: RedisTeamSettings, root_task_id: str, assignment_id: str) -> str:
    return (
        f"{_key_prefix(settings)}:assignment-activity:"
        f"{_redis_key_part(root_task_id)}:{_redis_key_part(assignment_id)}"
    )


def root_workflow_state_key(settings: RedisTeamSettings, root_task_id: str) -> str:
    return f"{_key_prefix(settings)}:root:{_redis_key_part(root_task_id)}:state"


def assignment_dispatch_state_key(settings: RedisTeamSettings, root_task_id: str) -> str:
    return f"{_key_prefix(settings)}:root:{_redis_key_part(root_task_id)}:assignment-dispatch"


def event_for(settings: RedisTeamSettings, event: str, extra: Optional[dict[str, Any]] = None) -> dict[str, Any]:
    payload = {
        "v": WIRE_SCHEMA_VERSION,
        "protocolVersion": PROTOCOL_VERSION,
        "protocolCapabilities": PROTOCOL_CAPABILITIES,
        "eventId": f"evt_{uuid.uuid4().hex}",
        "event": event,
        "teamId": settings.team_id,
        "memberId": settings.member_id,
        "role": settings.role,
        "at": _now_iso(),
    }
    if extra:
        payload.update(extra)
    return payload


def ensure_team_dirs(settings: RedisTeamSettings) -> None:
    for directory in [
        settings.shared_path,
        *(settings.shared_path / child for child in ("inbox", "status", "tasks", "results", "artifacts", "tmp", ".hermes-redis-team")),
    ]:
        _ensure_shared_directory(directory)


def _active_envelope_path(settings: RedisTeamSettings) -> Path:
    return settings.shared_path / ".hermes-redis-team" / f"active-{_safe_name(settings.member_id)}.json"


def _load_active_envelope(settings: RedisTeamSettings) -> dict[str, Any]:
    value = _read_json(_active_envelope_path(settings))
    return value if isinstance(value, dict) else {}


def _persist_active_envelope(settings: RedisTeamSettings, envelope: dict[str, Any]) -> None:
    _atomic_write_json(_active_envelope_path(settings), envelope)


def _turn_observation_path(settings: RedisTeamSettings) -> Path:
    return settings.shared_path / ".hermes-redis-team" / f"turn-{_safe_name(settings.member_id)}.json"


def _begin_turn_observation(settings: RedisTeamSettings, envelope: dict[str, Any], *, context_only: bool) -> None:
    try:
        _atomic_write_json(_turn_observation_path(settings), {
            "messageId": _trim(envelope.get("messageId")),
            "rootTaskId": _trim(envelope.get("rootTaskId") or envelope.get("taskId")),
            "assignmentId": _trim(envelope.get("assignmentId") or envelope.get("workId")),
            "revision": max(1, int(envelope.get("revision") or 1)),
            "contextOnly": context_only,
            "startedAt": _now_iso(),
            "observedAt": _now_iso(),
            "turnState": "starting",
            "apiCallCount": 0,
            "toolCallCount": 0,
            "toolTrace": [],
            "lastTool": None,
        })
    except Exception as exc:
        logger.warning("Redis Team: turn observation start skipped: %s", exc)


def _record_turn_tool_result(settings: RedisTeamSettings, tool_name: str, result: dict[str, Any]) -> None:
    try:
        observation = _read_json(_turn_observation_path(settings)) or {}
        if not isinstance(observation, dict) or not _trim(observation.get("messageId")):
            return
        failed = result.get("ok") is False or result.get("success") is False or bool(result.get("error"))
        team_send_result = result.get("sent") if tool_name == "team_send" and isinstance(result.get("sent"), dict) else {}
        tool_fact = {
            "toolName": tool_name,
            "failed": failed,
            "retryable": result.get("retryable") is True,
            "succeeded": not failed,
            "error": _trim(result.get("error") or result.get("message")) or None,
            "code": _trim(result.get("code")) or None,
            "candidates": result.get("candidates") if isinstance(result.get("candidates"), list) else None,
            "observedAt": _now_iso(),
            "outboundObserved": bool(team_send_result),
            "businessMutation": team_send_result.get("businessMutation") is True,
            "businessDeliveryKind": _trim(team_send_result.get("businessDeliveryKind")) or None,
            "outboundTarget": _trim(team_send_result.get("to")) or None,
        }
        trace = observation.get("toolTrace") if isinstance(observation.get("toolTrace"), list) else []
        trace.append(tool_fact)
        observation["toolTrace"] = trace[-TURN_TOOL_TRACE_LIMIT:]
        observation["toolCallCount"] = max(int(observation.get("toolCallCount") or 0), len(trace))
        observation["lastToolActivity"] = tool_fact
        observation["lastToolAt"] = tool_fact["observedAt"]
        observation["lastToolName"] = tool_name
        observation["lastToolFailed"] = failed
        observation["pendingToolName"] = ""
        observation["turnState"] = "running"
        observation["observedAt"] = tool_fact["observedAt"]
        if tool_name in TEAM_CONTROL_TOOLS:
            observation["lastTool"] = tool_fact
        _atomic_write_json(_turn_observation_path(settings), observation)
    except Exception as exc:
        logger.warning("Redis Team: turn tool observation skipped: %s", exc)


def _team_tool_json_result(settings: RedisTeamSettings, tool_name: str, result: dict[str, Any]) -> str:
    _record_turn_tool_result(settings, tool_name, result)
    return json.dumps(result, ensure_ascii=False)


def _update_turn_observation_from_hook(
    *,
    phase: str,
    tool_name: str = "",
    result: Any = None,
    task_id: str = "",
    turn_id: str = "",
    api_request_id: str = "",
    api_call_count: int = 0,
    duration_ms: int = 0,
    finish_reason: str = "",
    status: str = "",
    error_message: str = "",
) -> None:
    """Persist bounded Hermes loop facts without changing Team business state."""

    settings = load_settings(None)
    if not settings.enabled:
        return
    try:
        path = _turn_observation_path(settings)
        observation = _read_json(path) or {}
        if not isinstance(observation, dict) or not _trim(observation.get("messageId")):
            return
        expected_task = _trim(observation.get("rootTaskId") or observation.get("taskId"))
        if task_id and expected_task and _trim(task_id) != expected_task:
            return
        now = _now_iso()
        observation["observedAt"] = now
        if turn_id:
            observation["turnId"] = _trim(turn_id)
        if api_request_id:
            observation["apiRequestId"] = _trim(api_request_id)
        if phase == "pre_api":
            observation["turnState"] = "running"
            observation["lastActivityKind"] = "api_request_started"
            observation["lastApiActivityAt"] = now
            observation["apiCallCount"] = max(int(observation.get("apiCallCount") or 0), int(api_call_count or 0))
        elif phase == "post_api":
            observation["turnState"] = "running"
            observation["lastActivityKind"] = "api_response_received"
            observation["lastApiActivityAt"] = now
            observation["apiCallCount"] = max(int(observation.get("apiCallCount") or 0), int(api_call_count or 0))
            observation["lastApiDurationMs"] = max(0, int(duration_ms or 0))
            observation["lastFinishReason"] = _trim(finish_reason) or None
        elif phase == "pre_tool":
            observation["turnState"] = "waiting_tool"
            observation["lastActivityKind"] = "tool_call_started"
            observation["pendingToolName"] = _trim(tool_name)
            observation["lastToolAt"] = now
            observation["toolCallCount"] = int(observation.get("toolCallCount") or 0) + 1
        elif phase == "post_tool":
            parsed = result
            if isinstance(result, str):
                try:
                    parsed = json.loads(result)
                except (TypeError, ValueError):
                    parsed = {"ok": status != "error", "error": error_message or None}
            if not isinstance(parsed, dict):
                parsed = {"ok": status != "error", "result": _short_text(str(parsed), 240)}
            if status == "error" and not parsed.get("error"):
                parsed["error"] = _trim(error_message) or "Hermes tool call failed"
            _record_turn_tool_result(settings, _trim(tool_name), parsed)
            return
        _atomic_write_json(path, observation)
    except Exception as exc:
        logger.debug("Redis Team: Hermes hook observation skipped: %s", exc)


def _hook_pre_api_request(**kwargs: Any) -> None:
    _update_turn_observation_from_hook(
        phase="pre_api",
        task_id=_trim(kwargs.get("task_id")),
        turn_id=_trim(kwargs.get("turn_id")),
        api_request_id=_trim(kwargs.get("api_request_id")),
        api_call_count=int(kwargs.get("api_call_count") or 0),
    )


def _hook_post_api_request(**kwargs: Any) -> None:
    _update_turn_observation_from_hook(
        phase="post_api",
        task_id=_trim(kwargs.get("task_id")),
        turn_id=_trim(kwargs.get("turn_id")),
        api_request_id=_trim(kwargs.get("api_request_id")),
        api_call_count=int(kwargs.get("api_call_count") or 0),
        duration_ms=int(float(kwargs.get("api_duration") or 0) * 1000),
        finish_reason=_trim(kwargs.get("finish_reason")),
    )


def _hook_pre_tool_call(**kwargs: Any) -> None:
    _update_turn_observation_from_hook(
        phase="pre_tool",
        tool_name=_trim(kwargs.get("tool_name")),
        task_id=_trim(kwargs.get("task_id")),
        turn_id=_trim(kwargs.get("turn_id")),
        api_request_id=_trim(kwargs.get("api_request_id")),
    )


def _hook_post_tool_call(**kwargs: Any) -> None:
    tool_name = _trim(kwargs.get("tool_name"))
    # Team handlers persist canonical results themselves. A schema/dispatch
    # failure may happen before the handler, though; in that case the pending
    # marker is still present and the hook must retain the retryable evidence.
    if tool_name.startswith("team_"):
        try:
            settings = load_settings(None)
            observation = _read_json(_turn_observation_path(settings)) or {}
            if _trim(observation.get("pendingToolName")) != tool_name:
                return
        except Exception:
            pass
    _update_turn_observation_from_hook(
        phase="post_tool",
        tool_name=tool_name,
        result=kwargs.get("result"),
        task_id=_trim(kwargs.get("task_id")),
        turn_id=_trim(kwargs.get("turn_id")),
        api_request_id=_trim(kwargs.get("api_request_id")),
        duration_ms=int(kwargs.get("duration_ms") or 0),
        status=_trim(kwargs.get("status")),
        error_message=_trim(kwargs.get("error_message")),
    )


def _finish_turn_observation(settings: RedisTeamSettings, message_id: str) -> dict[str, Any]:
    path = _turn_observation_path(settings)
    try:
        observation = _read_json(path) or {}
    except Exception as exc:
        logger.warning("Redis Team: turn observation read skipped: %s", exc)
        return {}
    if not isinstance(observation, dict) or _trim(observation.get("messageId")) != _trim(message_id):
        return {}
    try:
        path.unlink(missing_ok=True)
    except OSError:
        pass
    return observation


def _context_turn_outcome_policy(envelope: dict[str, Any]) -> dict[str, Any]:
    configured = envelope.get("turnOutcomePolicy") or envelope.get("turn_outcome_policy")
    if isinstance(configured, dict):
        return {
            "actionExpected": _truthy(configured.get("actionExpected", configured.get("action_expected")), False),
            "immediateRecoveryAllowed": _truthy(configured.get("immediateRecoveryAllowed", configured.get("immediate_recovery_allowed")), False),
            "reason": _trim(configured.get("reason")) or "control_plane_policy",
        }
    metadata = envelope.get("metadata") if isinstance(envelope.get("metadata"), dict) else {}
    intent = _trim(envelope.get("intent") or metadata.get("intent") or metadata.get("monitorType")).lower()
    if intent in {"member_result_confirmed", "leader_synthesis_reminder", "leader_workflow_decision", "leader_decision_reminder"}:
        return {"actionExpected": True, "immediateRecoveryAllowed": True, "reason": "legacy_workflow_notification"}
    if intent in {"root_coordination_recovery", "root_assignment_recovery", "assignment_recovery_reminder", "target_resolution_review"}:
        return {"actionExpected": True, "immediateRecoveryAllowed": False, "reason": "recovery_turn"}
    return {"actionExpected": False, "immediateRecoveryAllowed": False, "reason": "ordinary_context"}


def _observe_team_turn_outcome(envelope: dict[str, Any], active: dict[str, Any], turn: dict[str, Any]) -> dict[str, Any]:
    context_only = not _truthy(envelope.get("requiresCompletion"), True)
    policy = _context_turn_outcome_policy(envelope) if context_only else {
        "actionExpected": True,
        "immediateRecoveryAllowed": True,
        "reason": "formal_assignment",
    }
    last_tool = turn.get("lastTool") if isinstance(turn.get("lastTool"), dict) else {}
    completion_observed = bool(active.get("terminal") or active.get("explicitCompletionSubmitted"))
    conflicts: list[str] = []
    if completion_observed and last_tool.get("failed") is True and last_tool.get("retryable") is True:
        conflicts.append("completion_and_retryable_tool_gap")
    if conflicts:
        outcome = "runtime_observation_unknown"
    elif completion_observed:
        outcome = "completed"
    elif (
        last_tool.get("toolName") == "team_send"
        and last_tool.get("succeeded") is True
        and (
            last_tool.get("businessMutation") is True
            or _trim(last_tool.get("businessDeliveryKind")).lower() == "assignment"
        )
    ):
        outcome = "legitimate_wait"
    elif last_tool.get("failed") is True and last_tool.get("retryable") is True:
        outcome = "retryable_tool_gap"
    elif policy["actionExpected"]:
        outcome = "completion_receipt_gap"
    else:
        outcome = "ordinary_open_turn"
    return {
        "outcome": outcome,
        "contextOnly": context_only,
        "actionExpected": policy["actionExpected"],
        "immediateRecoveryEligible": bool(
            policy["immediateRecoveryAllowed"]
            and outcome in {"retryable_tool_gap", "completion_receipt_gap"}
        ),
        "policyReason": policy["reason"],
        "evidenceConflict": bool(conflicts),
        "evidenceConflicts": conflicts,
        "lastTool": last_tool,
        "hadOutboundAssignment": last_tool.get("outboundObserved") is True,
        "downstreamAssignmentStarted": (
            last_tool.get("businessMutation") is True
            or _trim(last_tool.get("businessDeliveryKind")).lower() == "assignment"
        ),
        "outboundDeliveryKind": _trim(last_tool.get("businessDeliveryKind")) or None,
        "outboundTarget": _trim(last_tool.get("outboundTarget")) or None,
    }


def _envelope_value(settings: RedisTeamSettings, args: dict[str, Any], *keys: str) -> str:
    active = _load_active_envelope(settings)
    for key in keys:
        value = _trim(args.get(key))
        if value:
            return value
        value = _trim(active.get(key))
        if value:
            return value
    return ""


def _artifact_relative_path(value: Any) -> Path:
    raw = _trim(value).replace("\\", "/")
    if raw.startswith("/team/"):
        raw = raw[len("/team/") :]
    if not raw or raw.startswith("/"):
        raise ValueError("Team artifact path must be a non-empty current-Team path")
    parts = [part for part in raw.split("/") if part not in {"", "."}]
    if not parts or any(part == ".." for part in parts):
        raise ValueError("Team artifact path traversal is not allowed")
    return Path(*parts)


def _assert_no_symlink_traversal(shared_root: Path, candidate: Path) -> None:
    resolved_root = shared_root.resolve()
    try:
        lexical_parts = candidate.relative_to(shared_root).parts
    except ValueError as exc:
        raise ValueError("Team artifact path escaped the current Team workspace") from exc
    current = shared_root
    for component in lexical_parts:
        current = current / component
        if current.is_symlink():
            raise ValueError(f"Team artifact paths may not traverse symlinks: {current}")
    resolved_candidate = candidate.resolve(strict=False)
    resolved_parent = resolved_candidate.parent
    if resolved_parent != resolved_root and resolved_root not in resolved_parent.parents:
        raise ValueError("Team artifact path escaped the current Team workspace")


def _artifact_path(
    settings: RedisTeamSettings,
    args: dict[str, Any],
    *,
    default_scope: str,
    write: bool = False,
) -> Path:
    scope = _trim(args.get("scope") or default_scope).lower()
    relative = _artifact_relative_path(args.get("path"))
    shared_root = settings.shared_path
    if scope == "member":
        root_task_id = _envelope_value(settings, args, "rootTaskId", "root_task_id")
        assignment_id = _envelope_value(settings, args, "assignmentId", "assignment_id", "workId", "work_id")
        if not root_task_id or not assignment_id:
            raise ValueError("Active rootTaskId and assignmentId are required for member artifacts")
        root = (
            settings.shared_path
            / "artifacts"
            / _safe_name(root_task_id)
            / "members"
            / _safe_name(settings.member_id)
            / _safe_name(assignment_id)
        )
        canonical_candidate = settings.shared_path / relative
        if _trim(args.get("path")).replace("\\", "/").startswith("/team/"):
            try:
                canonical_candidate.relative_to(root)
            except ValueError as exc:
                raise ValueError("Canonical Team artifact path is outside the active member scope") from exc
            candidate = canonical_candidate
        else:
            candidate = root / relative
    elif scope == "team":
        candidate = settings.shared_path / relative
        if write and "leader" not in settings.role.lower() and "leader" not in settings.member_id.lower():
            active = _load_active_envelope(settings)
            root_task_id = _trim(active.get("rootTaskId") or active.get("taskId"))
            assignment_id = _trim(active.get("assignmentId") or active.get("workId"))
            allowed_root = (
                settings.shared_path
                / "results"
                / _safe_name(root_task_id)
                / "reviews"
                / _safe_name(assignment_id)
            )
            if not _assigned_validation_writer(settings, active):
                raise ValueError("Only the Team Leader or assigned validator may write this team-scoped artifact")
            try:
                candidate.relative_to(allowed_root)
            except ValueError as exc:
                raise ValueError("Canonical Team artifact path is outside the active validation scope") from exc
    else:
        raise ValueError("Team artifact scope must be member or team")
    _assert_no_symlink_traversal(shared_root, candidate)
    return candidate


def _normalize_validation_artifact_write_args(
    settings: RedisTeamSettings,
    args: dict[str, Any],
) -> dict[str, Any]:
    active = _load_active_envelope(settings)
    if not _assigned_validation_writer(settings, active):
        return args
    root_task_id = _trim(active.get("rootTaskId") or active.get("taskId"))
    assignment_id = _trim(active.get("assignmentId") or active.get("workId"))
    raw = _trim(args.get("path")).replace("\\", "/")
    relative = raw[len("/team/") :] if raw.startswith("/team/") else raw
    prefix = f"results/{_safe_name(root_task_id)}/reviews/"
    if not root_task_id or not assignment_id or not relative.startswith(prefix):
        return args
    remainder = relative[len(prefix) :]
    parts = remainder.split("/", 1)
    if len(parts) != 2 or not parts[0] or not parts[1] or ".." in parts[1].split("/"):
        return args
    normalized = dict(args)
    normalized["scope"] = "team"
    normalized["kind"] = "review"
    normalized["path"] = (
        f"/team/results/{_safe_name(root_task_id)}/reviews/"
        f"{_safe_name(assignment_id)}/{parts[1]}"
    )
    return normalized


def canonical_artifact_ref(settings: RedisTeamSettings, path: Path) -> str:
    shared_root = settings.shared_path.resolve()
    resolved = path.resolve(strict=False)
    try:
        relative = resolved.relative_to(shared_root)
    except ValueError as exc:
        raise ValueError(f"artifact path escaped Redis Team shared directory: {path}") from exc
    return "/team/" + relative.as_posix()


def validate_artifact_refs(settings: RedisTeamSettings, refs: Optional[list[str]]) -> list[str]:
    validated: list[str] = []
    for ref in refs or []:
        raw = _trim(ref)
        if not raw:
            continue
        candidate = settings.shared_path / _artifact_relative_path(raw)
        _assert_no_symlink_traversal(settings.shared_path, candidate)
        if not candidate.is_file():
            raise ValueError(f"artifact reference is not a readable file: {ref}")
        canonical = canonical_artifact_ref(settings, candidate)
        if canonical not in validated:
            validated.append(canonical)
    return validated


def _assigned_validation_writer(settings: RedisTeamSettings, envelope: dict[str, Any]) -> bool:
    role = settings.role.lower()
    member = settings.member_id.lower()
    return bool(
        "review" in role
        or "qa" in role
        or "review" in member
        or member == "qa"
        or envelope.get("validationAssignment")
        or envelope.get("validation_assignment")
        or _trim(
            envelope.get("validationTargetAssignmentId")
            or envelope.get("validation_target_assignment_id")
            or envelope.get("reviewedAssignmentId")
            or envelope.get("reviewed_assignment_id")
        )
    )


def _explicit_validation_assignment(envelope: dict[str, Any]) -> bool:
    return bool(
        _truthy(envelope.get("validationAssignment") or envelope.get("validation_assignment"), False)
        or _trim(
            envelope.get("validationTargetAssignmentId")
            or envelope.get("validation_target_assignment_id")
            or envelope.get("reviewedAssignmentId")
            or envelope.get("reviewed_assignment_id")
        )
    )


def _assignment_validation_guidance(settings: RedisTeamSettings, envelope: dict[str, Any]) -> str:
    if _explicit_validation_assignment(envelope):
        return (
            "Validation ownership: this assignment is test/review/evidence work. "
            "Perform the validation requested by the Leader normally; validation is assignment-specific "
            "and may be distributed across several members regardless of role name."
        )
    if _truthy(envelope.get("reviewRequired") or envelope.get("review_required"), False) or _truthy(
        envelope.get("validationRequired") or envelope.get("validation_required"), False
    ):
        return (
            "Validation ownership: this is production-only work with independent validation downstream. "
            "Produce and hand off the requested artifact without running syntax checks, tests, Browser "
            "acceptance, or another validation pass. Tools remain available for implementation and focused "
            "debugging; always hand off a usable result or an exact blocker."
        )
    if _assigned_validation_writer(settings, envelope):
        return (
            "Validation ownership: this legacy assignment has a validation-oriented member role but no "
            "explicit validation contract. Perform only the validation requested in the assignment; future "
            "assignments should use validationAssignment so ownership does not depend on role names."
        )
    return (
        "Validation ownership: follow the Leader's assignment scope instead of inferring testing duties "
        "from your role. Production-only work should produce and hand off the artifact without tests or "
        "acceptance checks. Only perform validation when this assignment explicitly asks for test, review, "
        "or evidence work. This guidance must never block delivery."
    )


def _canonicalize_reviewer_completion_report(
    settings: RedisTeamSettings,
    envelope: dict[str, Any],
    result_markdown: str,
    explicit_refs: Optional[list[str]],
) -> list[str]:
    root_task_id = _trim(envelope.get("rootTaskId") or envelope.get("taskId"))
    assignment_id = _trim(envelope.get("assignmentId") or envelope.get("workId"))
    if not root_task_id or not assignment_id or not _assigned_validation_writer(settings, envelope):
        return []
    text_refs = re.findall(r"/team/[^\s`'\"<>()[\]{}]+", result_markdown or "")
    member_prefix = (
        f"/team/artifacts/{_safe_name(root_task_id)}/members/"
        f"{_safe_name(settings.member_id)}/{_safe_name(assignment_id)}/"
    )
    candidates: list[str] = []
    for raw in [*(explicit_refs or []), *text_refs]:
        ref = _trim(raw).rstrip(".,;:!?锛岋紱锛氥€傦紒")
        ref = ref.rstrip("，。；：、！？")
        if not ref.startswith(member_prefix) or Path(ref).suffix.lower() not in {".md", ".txt", ".json"}:
            continue
        if ref not in candidates:
            candidates.append(ref)
    if not candidates:
        return []
    mirrored: list[str] = []
    try:
        for candidate in candidates[:16]:
            source = settings.shared_path / _artifact_relative_path(candidate)
            if not source.is_file():
                continue
            destination = (
                settings.shared_path
                / "results"
                / _safe_name(root_task_id)
                / "reviews"
                / _safe_name(assignment_id)
                / source.name
            )
            _assert_no_symlink_traversal(settings.shared_path, source)
            _assert_no_symlink_traversal(settings.shared_path, destination)
            _atomic_write_text(destination, source.read_text(encoding="utf-8"))
            canonical = canonical_artifact_ref(settings, destination)
            if canonical not in mirrored:
                mirrored.append(canonical)
    except Exception:
        pass
    return mirrored


def artifact_metadata(settings: RedisTeamSettings, refs: Optional[list[str]]) -> list[dict[str, Any]]:
    metadata: list[dict[str, Any]] = []
    for canonical in validate_artifact_refs(settings, refs):
        target = settings.shared_path / _artifact_relative_path(canonical)
        digest = hashlib.sha256(target.read_bytes()).hexdigest()
        metadata.append(
            {
                "path": canonical,
                "sha256": digest,
                "bytes": target.stat().st_size,
            }
        )
    return metadata


def _artifact_read_path_with_fallback(settings: RedisTeamSettings, args: dict[str, Any]) -> Path:
    target = _artifact_path(settings, args, default_scope="team")
    if target.is_file():
        return target
    raw = _trim(args.get("path")).replace("\\", "/")
    relative = raw[len("/team/") :] if raw.startswith("/team/") else raw
    parts = [part for part in relative.split("/") if part]
    requested_root = parts[1] if len(parts) > 2 and parts[0] in {"results", "artifacts"} else ""
    active = _load_active_envelope(settings)
    active_root = _trim(active.get("rootTaskId") or active.get("taskId"))
    root_task_id = _safe_name(requested_root or active_root)
    if not root_task_id or (requested_root and active_root and _safe_name(active_root) != root_task_id):
        return target
    basename = target.name
    allowed_roots = (
        settings.shared_path / "results" / root_task_id,
        settings.shared_path / "artifacts" / root_task_id,
    )
    referenced: list[Path] = []
    for ref in [*(active.get("artifactRefs") or []), *(active.get("contextRefs") or [])]:
        try:
            candidate = settings.shared_path / _artifact_relative_path(ref)
            if candidate.name != basename or not candidate.is_file():
                continue
            if not any(candidate == root or root in candidate.parents for root in allowed_roots):
                continue
            _assert_no_symlink_traversal(settings.shared_path, candidate)
            if candidate not in referenced:
                referenced.append(candidate)
        except Exception:
            continue
    if len(referenced) == 1:
        return referenced[0]
    if len(referenced) > 1:
        return target

    matches: list[Path] = []
    inspected = 0
    for root in allowed_roots:
        if not root.is_dir():
            continue
        for candidate in root.rglob("*"):
            inspected += 1
            if inspected > 128:
                return target
            if candidate.is_symlink() or not candidate.is_file() or candidate.name != basename:
                continue
            _assert_no_symlink_traversal(settings.shared_path, candidate)
            matches.append(candidate)
            if len(matches) > 1:
                return target
    return matches[0] if len(matches) == 1 else target


async def _tool_team_artifact_write(args: dict[str, Any], **_kwargs) -> str:
    settings = load_settings(None)
    try:
        effective_args = _normalize_validation_artifact_write_args(settings, args)
        target = _artifact_path(settings, effective_args, default_scope="member", write=True)
        _atomic_write_text(target, str(args.get("content") or ""))
        return _team_tool_json_result(
            settings,
            "team_artifact_write",
            {"ok": True, "artifact": {"path": canonical_artifact_ref(settings, target), "bytes": target.stat().st_size}},
        )
    except Exception as exc:
        return _team_tool_json_result(settings, "team_artifact_write", {"ok": False, "error": str(exc)})


async def _tool_team_artifact_read(args: dict[str, Any], **_kwargs) -> str:
    settings = load_settings(None)
    try:
        target = _artifact_read_path_with_fallback(settings, args)
        max_bytes = min(MAX_ARTIFACT_BYTES, max(1, int(args.get("maxBytes") or 256 * 1024)))
        offset = max(0, int(args.get("offset") or 0))
        if not target.is_file():
            raise ValueError("Team artifact is not a file")
        size = target.stat().st_size
        if offset > size:
            raise ValueError("Team artifact offset exceeds file size")
        with target.open("rb") as handle:
            handle.seek(offset)
            content_bytes = handle.read(max_bytes)
        content = content_bytes.decode("utf-8", errors="replace")
        next_offset = offset + len(content_bytes)
        truncated = next_offset < size
        return _team_tool_json_result(
            settings,
            "team_artifact_read",
            {
                "ok": True,
                "artifact": {
                    "path": canonical_artifact_ref(settings, target),
                    "content": content,
                    "bytes": size,
                    "offset": offset,
                    "returnedBytes": len(content_bytes),
                    "truncated": truncated,
                    "nextOffset": next_offset if truncated else None,
                },
            },
        )
    except Exception as exc:
        return _team_tool_json_result(settings, "team_artifact_read", {"ok": False, "error": str(exc)})


async def _tool_team_artifact_list(args: dict[str, Any], **_kwargs) -> str:
    settings = load_settings(None)
    try:
        target = _artifact_path(settings, args, default_scope="team")
        limit = min(200, max(1, int(args.get("limit") or 100)))
        if not target.is_dir():
            raise ValueError("Team artifact path is not a directory")
        entries = []
        for child in sorted(target.iterdir(), key=lambda item: item.name)[:limit]:
            if child.is_symlink():
                continue
            entries.append(
                {
                    "name": child.name,
                    "type": "directory" if child.is_dir() else "file",
                    "path": canonical_artifact_ref(settings, child),
                    "bytes": child.stat().st_size if child.is_file() else None,
                }
            )
        return _team_tool_json_result(
            settings,
            "team_artifact_list",
            {"ok": True, "path": canonical_artifact_ref(settings, target), "entries": entries},
        )
    except Exception as exc:
        return _team_tool_json_result(settings, "team_artifact_list", {"ok": False, "error": str(exc)})


async def _tool_team_artifact_mkdir(args: dict[str, Any], **_kwargs) -> str:
    settings = load_settings(None)
    try:
        target = _artifact_path(settings, args, default_scope="member", write=True)
        _ensure_shared_directory(target)
        return _team_tool_json_result(
            settings,
            "team_artifact_mkdir",
            {"ok": True, "artifact": {"path": canonical_artifact_ref(settings, target)}},
        )
    except Exception as exc:
        return _team_tool_json_result(settings, "team_artifact_mkdir", {"ok": False, "error": str(exc)})


def _preview_url(settings: RedisTeamSettings, target: Path) -> str:
    origin = settings.preview_origin.rstrip("/")
    parsed = urlparse(origin)
    hostname = (parsed.hostname or "").lower().rstrip(".")
    labels = hostname.split(".")
    managed_proxy = (
        parsed.scheme == "http"
        and labels
        and labels[0] == "clawmanager-egress-proxy"
        and len(labels) >= 3
        and labels[2] == "svc"
    )
    if not managed_proxy:
        raise ValueError("Team artifact Browser preview origin is unavailable")
    if not settings.team_token:
        raise ValueError("Team artifact Browser preview token is unavailable")
    canonical = canonical_artifact_ref(settings, target)
    parts = [part for part in canonical[len("/team/") :].split("/") if part]
    if not parts:
        raise ValueError("Team artifact preview requires a file")
    signed_prefix = "/".join(parts[:-1])
    encoded_prefix = (
        base64.urlsafe_b64encode(signed_prefix.encode("utf-8")).decode("ascii").rstrip("=")
        if signed_prefix
        else "_"
    )
    interactive = target.suffix.lower() == ".html"
    mode = "interactive" if interactive else ""
    payload = (
        f"team-preview-v2\n{mode}\n{settings.team_id}\n{signed_prefix}"
        if interactive
        else f"team-preview-v1\n{settings.team_id}\n{signed_prefix}"
    )
    signature = (
        base64.urlsafe_b64encode(
            hmac.new(settings.team_token.encode("utf-8"), payload.encode("utf-8"), hashlib.sha256).digest()
        )
        .decode("ascii")
        .rstrip("=")
    )
    if interactive:
        # Start on the resolvable managed service. ClawManager validates this
        # signed bootstrap URL before redirecting the Browser to the isolated
        # per-directory origin used for interactive state.
        return (
            f"{parsed.scheme}://{parsed.netloc}/v2/interactive/"
            f"{quote(settings.team_id, safe='')}/{encoded_prefix}/{signature}/{quote(parts[-1], safe='')}"
        )
    return (
        f"{parsed.scheme}://{parsed.netloc}/v1/"
        f"{quote(settings.team_id, safe='')}/{encoded_prefix}/{signature}/{quote(parts[-1], safe='')}"
    )


async def _tool_team_artifact_preview(args: dict[str, Any], **_kwargs) -> str:
    settings = load_settings(None)
    try:
        target = _artifact_path(settings, args, default_scope="team")
        if not target.is_file():
            raise ValueError("Team artifact preview requires a readable file")
        return _team_tool_json_result(
            settings,
            "team_artifact_preview",
            {
                "ok": True,
                "artifact": {"path": canonical_artifact_ref(settings, target), "bytes": target.stat().st_size},
                "previewUrl": _preview_url(settings, target),
            },
        )
    except Exception as exc:
        return _team_tool_json_result(settings, "team_artifact_preview", {"ok": False, "error": str(exc)})


def write_local_status(settings: RedisTeamSettings, patch: Optional[dict[str, Any]] = None) -> dict[str, Any]:
    ensure_team_dirs(settings)
    path = settings.shared_path / "status" / f"{_safe_name(settings.member_id)}.json"
    previous = _read_json(path) or {}
    status = {
        "teamId": settings.team_id,
        "memberId": settings.member_id,
        "role": settings.role,
        "liveness": "online",
        "runtime": "hermes",
        "availability": "idle",
        "lastSeenAt": _now_iso(),
    }
    status.update(previous)
    status.update(
        {
            "teamId": settings.team_id,
            "memberId": settings.member_id,
            "role": settings.role,
            "lastSeenAt": _now_iso(),
        }
    )
    if patch:
        status.update({k: v for k, v in patch.items() if v is not None})
    _atomic_write_json(path, status)
    return status


def _clear_ready_file(settings: RedisTeamSettings) -> None:
    raw = _trim(settings.ready_file)
    if not raw:
        return
    path = Path(raw)
    if not path.is_absolute():
        raise ValueError("CLAWMANAGER_TEAM_READY_FILE must be absolute")
    for candidate in (path, _startup_failure_path(path)):
        try:
            candidate.unlink()
        except FileNotFoundError:
            pass


def _startup_failure_path(ready_path: Path) -> Path:
    return ready_path.with_name(ready_path.name + ".failed")


def _write_private_startup_state(path: Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    path.parent.chmod(0o700)
    tmp = path.with_name(f"{path.name}.{os.getpid()}.{uuid.uuid4().hex}.tmp")
    tmp.write_text(json.dumps(payload, ensure_ascii=False) + "\n", encoding="utf-8")
    tmp.chmod(0o600)
    tmp.replace(path)
    path.chmod(0o600)


def _publish_startup_failure(
    settings: RedisTeamSettings,
    *,
    code: str,
    message: str,
    retryable: bool,
) -> None:
    raw = _trim(settings.ready_file)
    if not raw:
        return
    ready_path = Path(raw)
    if not ready_path.is_absolute():
        raise ValueError("CLAWMANAGER_TEAM_READY_FILE must be absolute")
    try:
        ready_path.unlink()
    except FileNotFoundError:
        pass
    _write_private_startup_state(
        _startup_failure_path(ready_path),
        {
            "ready": False,
            "state": "failed",
            "teamId": settings.team_id,
            "memberId": settings.member_id,
            "instanceId": settings.instance_id,
            "generation": settings.generation,
            "runtime": "hermes",
            "protocolVersion": PROTOCOL_VERSION,
            "failedAt": _now_iso(),
            "error": {
                "code": _safe_name(code),
                "message": _short_text(message, 1000),
                "retryable": retryable,
            },
        },
    )


def _publish_ready_file(settings: RedisTeamSettings, status: dict[str, Any]) -> None:
    raw = _trim(settings.ready_file)
    if not raw:
        return
    path = Path(raw)
    if not path.is_absolute():
        raise ValueError("CLAWMANAGER_TEAM_READY_FILE must be absolute")
    try:
        _startup_failure_path(path).unlink()
    except FileNotFoundError:
        pass
    _write_private_startup_state(
        path,
        {
            "ready": True,
            "state": "ready",
            "teamId": settings.team_id,
            "memberId": settings.member_id,
            "instanceId": settings.instance_id,
            "generation": settings.generation,
            "runtime": "hermes",
            "protocolVersion": PROTOCOL_VERSION,
            "readyAt": _now_iso(),
            "presence": {
                "liveness": status.get("liveness"),
                "availability": status.get("availability"),
            },
        },
    )


def _record_startup_failure(
    settings: RedisTeamSettings,
    *,
    code: str,
    message: str,
    retryable: bool,
) -> None:
    try:
        _publish_startup_failure(
            settings,
            code=code,
            message=message,
            retryable=retryable,
        )
    except Exception as exc:
        logger.error("Redis Team: failed to publish startup failure state: %s", exc)


def read_team_statuses(settings: RedisTeamSettings, member_id: str = "") -> Any:
    ensure_team_dirs(settings)
    status_dir = settings.shared_path / "status"
    if member_id:
        return _read_json(status_dir / f"{_safe_name(member_id)}.json")
    statuses = []
    for path in sorted(status_dir.glob("*.json")):
        value = _read_json(path)
        if value:
            statuses.append(value)
    statuses.sort(key=lambda item: str(item.get("memberId", "")))
    return statuses


def _team_roster_members(settings: RedisTeamSettings) -> list[dict[str, Any]]:
    raw = _read_json(settings.shared_path / "team.json") or {}
    candidates = raw.get("members")
    if not isinstance(candidates, list) and isinstance(raw.get("team"), dict):
        candidates = raw["team"].get("members")
    if not isinstance(candidates, list):
        return []
    members: list[dict[str, Any]] = []
    for candidate in candidates:
        if not isinstance(candidate, dict):
            continue
        member_id = _trim(
            candidate.get("memberId") or candidate.get("memberID")
            or candidate.get("memberKey") or candidate.get("id") or candidate.get("key")
        )
        if not member_id:
            continue
        aliases = {member_id, _safe_name(member_id)}
        for value in (candidate.get("displayName"), candidate.get("name")):
            text = _trim(value)
            if text:
                aliases.update({text, _safe_name(text)})
        members.append({"memberId": member_id, "aliases": sorted(aliases)})
    return members


def _comparable_roster_identity(value: Any) -> str:
    return unicodedata.normalize("NFKC", _trim(value)).casefold()


def _roster_identity_fragments(value: Any) -> set[str]:
    comparable = _comparable_roster_identity(value)
    if not comparable:
        return set()
    return {comparable, *[part for part in re.split(r"[^\w]+", comparable, flags=re.UNICODE) if part]}


def _bounded_roster_alias_match(value: str, alias: str) -> bool:
    if not value or not alias:
        return False
    if value == alias:
        return True
    return re.search(r"(?<!\w)" + re.escape(alias) + r"(?!\w)", value, flags=re.UNICODE) is not None


def _roster_edit_distance(left: str, right: str) -> int:
    previous = list(range(len(right) + 1))
    for index, left_char in enumerate(left, start=1):
        current = [index]
        for offset, right_char in enumerate(right, start=1):
            current.append(min(
                current[-1] + 1,
                previous[offset] + 1,
                previous[offset - 1] + (0 if left_char == right_char else 1),
            ))
        previous = current
    return previous[-1]


def _resolve_roster_target(settings: RedisTeamSettings, value: Any) -> dict[str, Any]:
    original = _trim(value)
    comparable = _comparable_roster_identity(original)
    fragments = _roster_identity_fragments(original)
    members = _team_roster_members(settings)
    if not comparable or not members:
        return {"memberId": original if comparable and not members else "", "kind": "roster_unavailable", "candidates": [], "suggestions": []}
    matches: list[dict[str, Any]] = []
    for member in members:
        aliases = {_comparable_roster_identity(alias) for alias in member["aliases"] if _trim(alias)}
        matched = sorted(alias for alias in aliases if alias in fragments or _bounded_roster_alias_match(comparable, alias))
        if matched:
            matches.append({"member": member, "aliases": matched})
    if len(matches) == 1:
        return {
            "memberId": matches[0]["member"]["memberId"],
            "kind": "exact_roster_alias" if comparable in matches[0]["aliases"] else "unique_roster_information",
            "candidates": [matches[0]["member"]["memberId"]],
            "suggestions": [],
        }
    if len(matches) > 1:
        return {"memberId": "", "kind": "ambiguous_roster_information", "candidates": [match["member"]["memberId"] for match in matches], "suggestions": []}
    scored: list[tuple[int, str]] = []
    for member in members:
        distances = [
            _roster_edit_distance(fragment, _comparable_roster_identity(alias))
            for fragment in fragments
            for alias in member["aliases"]
            if len(fragment) >= 3 and len(_comparable_roster_identity(alias)) >= 3
        ]
        if distances and min(distances) <= 2:
            scored.append((min(distances), member["memberId"]))
    best = min((score for score, _ in scored), default=None)
    suggestions = [member_id for score, member_id in scored if score == best] if best is not None else []
    return {"memberId": "", "kind": "suggestion_only" if suggestions else "unresolved", "candidates": [], "suggestions": suggestions}


def write_task_result(
    settings: RedisTeamSettings,
    task_id: str,
    *,
    envelope: Optional[dict[str, Any]] = None,
    status: str,
    summary: str,
    result_markdown: str = "",
    artifact_refs: Optional[list[str]] = None,
) -> dict[str, Any]:
    ensure_team_dirs(settings)
    task_id = task_id or f"task_{uuid.uuid4().hex}"
    refs = validate_artifact_refs(settings, artifact_refs)
    active = envelope if isinstance(envelope, dict) else _load_active_envelope(settings)
    root_task_id = _trim(active.get("rootTaskId")) or task_id
    assignment_id = _trim(active.get("assignmentId") or active.get("workId"))
    if status != "succeeded" and not refs and root_task_id and assignment_id:
        failure_report = (
            settings.shared_path
            / "artifacts"
            / _safe_name(root_task_id)
            / "members"
            / _safe_name(settings.member_id)
            / _safe_name(assignment_id)
            / "failure-result.md"
        )
        _assert_no_symlink_traversal(settings.shared_path, failure_report)
        _atomic_write_text(failure_report, result_markdown or summary)
        refs.append(canonical_artifact_ref(settings, failure_report))
    payload = {
        "taskId": task_id,
        "rootTaskId": root_task_id,
        "assignmentId": assignment_id or None,
        "memberId": settings.member_id,
        "status": status,
        "summary": summary,
        "resultMarkdown": result_markdown,
        "artifactRefs": refs,
        "completedAt": _now_iso(),
        "protocolVersion": PROTOCOL_VERSION,
    }
    completion_dir = settings.shared_path / ".hermes-redis-team" / "completions"
    completion_name = (
        f"{_safe_name(settings.member_id)}-"
        f"{_safe_name(task_id)}-"
        f"{_safe_name(assignment_id or 'unscoped')}.json"
    )
    _atomic_write_json(completion_dir / completion_name, payload)
    write_local_status(
        settings,
        {
            "availability": "idle" if status == "succeeded" else "blocked",
            "currentTaskId": task_id,
            "progress": 100 if status == "succeeded" else None,
            "lastSummary": summary,
            "artifactRefs": refs,
        },
    )
    return payload


def normalize_envelope(raw: Any) -> Optional[dict[str, Any]]:
    if not isinstance(raw, dict):
        return None
    message_id = _trim(raw.get("messageId") or raw.get("id"))
    task_id = _trim(raw.get("taskId") or raw.get("task_id"))
    if not message_id or not task_id:
        return None
    return {
        "schemaVersion": raw.get("v") or raw.get("schemaVersion") or WIRE_SCHEMA_VERSION,
        "protocolVersion": raw.get("protocolVersion") or raw.get("protocol_version") or raw.get("v") or WIRE_SCHEMA_VERSION,
        "messageId": message_id,
        "taskId": task_id,
        "rootTaskId": raw.get("rootTaskId") or raw.get("root_task_id") or raw.get("taskId") or raw.get("task_id"),
        "rootMessageId": raw.get("rootMessageId") or raw.get("root_message_id") or raw.get("messageId") or raw.get("id"),
        "workId": raw.get("workId") or raw.get("work_id") or raw.get("assignmentId") or raw.get("assignment_id"),
        "assignmentId": raw.get("assignmentId") or raw.get("assignment_id") or raw.get("workId") or raw.get("work_id"),
        "phaseId": raw.get("phaseId") or raw.get("phase_id"),
        "revision": raw.get("revision") or 1,
        "teamId": raw.get("teamId"),
        "from": raw.get("from") or raw.get("sender") or "unknown",
        "to": raw.get("to") or raw.get("recipient") or "",
        "conversationId": raw.get("conversationId") or raw.get("conversation_id") or raw.get("taskId") or raw.get("task_id"),
        "type": raw.get("type") or "message",
        "intent": raw.get("intent") or "",
        "role": raw.get("role") or "teammate",
        "text": raw.get("text") or raw.get("prompt") or raw.get("rawPayload") or "",
        "priority": raw.get("priority") or "normal",
        "createdAt": raw.get("createdAt") or raw.get("created_at") or _now_iso(),
        "expiresAt": raw.get("expiresAt") or raw.get("expires_at"),
        "contextRefs": [x for x in raw.get("contextRefs", []) if x] if isinstance(raw.get("contextRefs"), list) else [],
        "artifacts": raw.get("artifacts") or [],
        "metadata": raw.get("metadata") if isinstance(raw.get("metadata"), dict) else {},
        "sessionKey": raw.get("sessionKey") or raw.get("approvalSessionKey") or "",
        "approval": raw.get("approval") if isinstance(raw.get("approval"), dict) else {},
        "requiresCompletion": raw.get("requiresCompletion", raw.get("requires_completion", True)),
        "turnOutcomePolicy": (
            raw.get("turnOutcomePolicy")
            or raw.get("turn_outcome_policy")
            or (raw.get("metadata") if isinstance(raw.get("metadata"), dict) else {}).get("turnOutcomePolicy")
            or {}
        ),
        "businessDeliveryKind": raw.get("businessDeliveryKind") or raw.get("business_delivery_kind"),
        "businessDeliveryReason": raw.get("businessDeliveryReason") or raw.get("business_delivery_reason"),
        "deliverySemanticsVersion": raw.get("deliverySemanticsVersion") or raw.get("delivery_semantics_version"),
        "businessMutation": raw.get("businessMutation", raw.get("business_mutation")),
        "revisionAuthorized": raw.get("revisionAuthorized", raw.get("revision_authorized")),
        "nonAuthoritative": raw.get("nonAuthoritative", raw.get("non_authoritative")),
        "responseLocale": raw.get("responseLocale") or raw.get("response_locale") or "zh-CN",
        "sharedWorkspace": raw.get("sharedWorkspace") if isinstance(raw.get("sharedWorkspace"), dict) else {},
        "idempotencyKey": raw.get("idempotencyKey") or message_id,
        "redisId": raw.get("redisId"),
    }


def _assignment_identity(envelope: dict[str, Any]) -> tuple[str, str]:
    return (
        _trim(envelope.get("rootTaskId") or envelope.get("taskId")),
        _trim(envelope.get("assignmentId") or envelope.get("workId")),
    )


def _is_context_only_envelope(envelope: dict[str, Any]) -> bool:
    if not envelope:
        return False
    if not _truthy(envelope.get("requiresCompletion"), True):
        return True
    delivery_kind = _trim(
        envelope.get("businessDeliveryKind")
        or envelope.get("business_delivery_kind")
        or envelope.get("deliveryKind")
        or envelope.get("delivery_kind")
    ).lower()
    if delivery_kind in {"context", "peer_request", "notification", "monitor", "ambiguous"}:
        return True
    metadata = envelope.get("metadata") if isinstance(envelope.get("metadata"), dict) else {}
    intent = _trim(envelope.get("intent") or metadata.get("intent") or envelope.get("type")).lower()
    return intent in {
        "member_result_confirmed", "context", "context_update", "notification",
        "peer_request", "question", "reminder", "follow_up",
        "assignment_status_check", "assignment_recovery_request", "leader_synthesis_reminder",
    }


def _is_formal_assignment(envelope: dict[str, Any]) -> bool:
    root_task_id, assignment_id = _assignment_identity(envelope)
    return bool(
        root_task_id
        and assignment_id
        and _truthy(envelope.get("requiresCompletion"), True)
        and not _is_monitor_envelope(envelope)
        and not _is_context_only_envelope(envelope)
    )


def _root_workflow_state_is_terminal(value: Any) -> bool:
    if not isinstance(value, dict):
        return False
    if value.get("terminal") is True:
        return True
    status = _trim(value.get("status") or value.get("workflowState") or value.get("workflow_state")).lower()
    return status in {"succeeded", "failed", "cancelled", "completed"}


async def _root_task_is_terminal(
    redis: "AsyncRedisClient",
    settings: RedisTeamSettings,
    envelope: dict[str, Any],
) -> bool:
    root_task_id, _ = _assignment_identity(envelope)
    if not root_task_id:
        return False
    try:
        raw = await redis.command("GET", root_workflow_state_key(settings, root_task_id))
        if not raw:
            return False
        if isinstance(raw, bytes):
            raw = raw.decode("utf-8", errors="replace")
        value = json.loads(raw) if isinstance(raw, str) else raw
        return _root_workflow_state_is_terminal(value)
    except Exception:
        # Older ClawManager versions may not publish root workflow state.
        # Keep the Runtime compatible and rely on the control-plane terminal
        # barrier rather than turning a transient read failure into task loss.
        return False


async def _read_root_workflow_state(
    redis: "AsyncRedisClient",
    settings: RedisTeamSettings,
    root_task_id: str,
) -> dict[str, Any]:
    if not root_task_id:
        return {}
    try:
        raw = await redis.command("GET", root_workflow_state_key(settings, root_task_id))
        if isinstance(raw, bytes):
            raw = raw.decode("utf-8", errors="replace")
        value = json.loads(raw) if isinstance(raw, str) and raw else raw
        return value if isinstance(value, dict) else {}
    except Exception:
        return {}


async def _recent_target_assignment_dispatch(
    redis: "AsyncRedisClient",
    settings: RedisTeamSettings,
    root_task_id: str,
    target_member_id: str,
) -> dict[str, Any]:
    try:
        entries = await redis.command("HGETALL", assignment_dispatch_state_key(settings, root_task_id))
    except Exception:
        return {}
    if not isinstance(entries, list):
        return {}
    latest: dict[str, Any] = {}
    latest_timestamp = 0.0
    for index in range(0, len(entries) - 1, 2):
        try:
            value = json.loads(str(entries[index + 1]))
        except (TypeError, ValueError, json.JSONDecodeError):
            continue
        if not isinstance(value, dict) or _trim(value.get("to")) != _trim(target_member_id):
            continue
        if _trim(value.get("rootTaskId")) != _trim(root_task_id):
            continue
        if _trim(value.get("status")).lower() not in {"dispatched", "waiting_dependencies"}:
            continue
        try:
            created_at = datetime.fromisoformat(_trim(value.get("createdAt")).replace("Z", "+00:00")).timestamp()
        except ValueError:
            continue
        if time.time() - created_at > 120 or created_at <= latest_timestamp:
            continue
        latest = value
        latest_timestamp = created_at
    return latest


def _is_monitor_envelope(envelope: dict[str, Any]) -> bool:
    metadata = envelope.get("metadata") if isinstance(envelope.get("metadata"), dict) else {}
    intent = _trim(envelope.get("intent") or metadata.get("intent")).lower()
    monitor_type = _trim(metadata.get("monitorType") or metadata.get("monitor_type")).lower()
    sender = _trim(envelope.get("from")).lower()
    return (
        not _truthy(envelope.get("requiresCompletion"), True)
        and intent == "assignment_status_check"
        and (sender == "clawmanager-monitor" or monitor_type == "assignment_status_check")
    )


def _reply_target(settings: RedisTeamSettings, metadata: dict[str, Any]) -> str:
    for key in ("reply_to_member", "from", "sender", "leader", "requester"):
        value = _trim(metadata.get(key))
        if value and value != settings.member_id:
            return value
    value = _trim(metadata.get("to"))
    if value and value != settings.member_id:
        return value
    return ""


def _approval_session_key(envelope: dict[str, Any]) -> str:
    metadata = envelope.get("metadata") if isinstance(envelope.get("metadata"), dict) else {}
    for value in (
        envelope.get("sessionKey"),
        envelope.get("approvalSessionKey"),
        metadata.get("sessionKey"),
        metadata.get("approvalSessionKey"),
    ):
        text = _trim(value)
        if text:
            return text
    approval = metadata.get("approval")
    if isinstance(approval, dict):
        for key in ("sessionKey", "approvalSessionKey"):
            text = _trim(approval.get(key))
            if text:
                return text
    approval = envelope.get("approval")
    if isinstance(approval, dict):
        for key in ("sessionKey", "approvalSessionKey"):
            text = _trim(approval.get(key))
            if text:
                return text
    return ""


def _parse_approval_command(text: str) -> Optional[tuple[str, str, bool]]:
    raw = _trim(text).lower()
    if not raw.startswith("/"):
        return None
    parts = raw.split()
    command = parts[0].lstrip("/")
    args = set(parts[1:])
    if command == "approve":
        if "always" in args:
            choice = "always"
        elif "session" in args:
            choice = "session"
        else:
            choice = "once"
        return ("approve", choice, "all" in args)
    if command == "deny":
        return ("deny", "deny", "all" in args)
    return None


def _completion_event_for_status(status: str) -> str:
    normalized = _trim(status).lower()
    if normalized == "succeeded":
        return "task_completed"
    if normalized == "cancelled":
        return "task_cancelled"
    return "task_failed"


def _completion_id(settings: RedisTeamSettings, envelope: dict[str, Any]) -> str:
    root = _safe_name(_trim(envelope.get("rootTaskId") or envelope.get("taskId")))
    assignment = _safe_name(_trim(envelope.get("assignmentId") or envelope.get("workId") or "assignment"))
    revision = max(1, int(envelope.get("revision") or 1))
    return f"completion:{_safe_name(settings.team_id)}:{root}:{_safe_name(settings.member_id)}:{assignment}:r{revision}"


def _task_event(
    settings: RedisTeamSettings,
    event: str,
    envelope: dict[str, Any],
    extra: Optional[dict[str, Any]] = None,
) -> dict[str, Any]:
    payload = {
        "messageId": envelope.get("messageId"),
        "sourceMessageId": envelope.get("messageId"),
        "taskId": envelope.get("taskId"),
        "rootTaskId": envelope.get("rootTaskId") or envelope.get("taskId"),
        "rootMessageId": envelope.get("rootMessageId"),
        "workId": envelope.get("assignmentId") or envelope.get("workId"),
        "assignmentId": envelope.get("assignmentId") or envelope.get("workId"),
        "canonicalWorkId": envelope.get("assignmentId") or envelope.get("workId"),
        "phaseId": envelope.get("phaseId"),
        "revision": max(1, int(envelope.get("revision") or 1)),
        "inReplyTo": envelope.get("messageId"),
        "requiresCompletion": envelope.get("requiresCompletion", True),
    }
    if extra:
        payload.update(extra)
    return event_for(settings, event, payload)


def _processed_message_key(settings: RedisTeamSettings, key: str) -> str:
    digest = hashlib.sha256(str(key or "").encode("utf-8")).hexdigest()
    return f"{_key_prefix(settings)}:processed:{_redis_key_part(settings.member_id)}:{digest}"


def _completion_attempt_key(settings: RedisTeamSettings, completion_id: str, attempt_id: str) -> str:
    return (
        f"{_key_prefix(settings)}:completion-attempt:"
        f"{_redis_key_part(completion_id)}:{_redis_key_part(attempt_id)}"
    )


def _completion_ack_key(settings: RedisTeamSettings, completion_id: str, attempt_id: str) -> str:
    return (
        f"{_key_prefix(settings)}:completion-ack:"
        f"{_redis_key_part(completion_id)}:{_redis_key_part(attempt_id)}"
    )


def _completion_state_key(settings: RedisTeamSettings, completion_id: str) -> str:
    return f"{_key_prefix(settings)}:completion-state:{_redis_key_part(completion_id)}"


class RespError(RuntimeError):
    pass


class AsyncRedisClient:
    """Small RESP2 client for the Redis commands used by the Team Bus."""

    def __init__(self, redis_url: str):
        self.redis_url = redis_url
        self._url = urlparse(redis_url)
        self._reader: Optional[asyncio.StreamReader] = None
        self._writer: Optional[asyncio.StreamWriter] = None
        self._command_lock = asyncio.Lock()

    async def connect(self) -> None:
        scheme = self._url.scheme or "redis"
        if scheme not in {"redis", "rediss"}:
            raise ValueError(f"unsupported Redis URL scheme: {scheme}")
        host = self._url.hostname or "localhost"
        port = self._url.port or (6380 if scheme == "rediss" else 6379)
        ssl_ctx = ssl.create_default_context() if scheme == "rediss" else None
        self._reader, self._writer = await asyncio.open_connection(host, port, ssl=ssl_ctx)
        if self._url.password:
            if self._url.username:
                await self.command("AUTH", unquote(self._url.username), unquote(self._url.password))
            else:
                await self.command("AUTH", unquote(self._url.password))
        db = (self._url.path or "").lstrip("/")
        if db:
            await self.command("SELECT", db)

    async def command(self, *parts: Any) -> Any:
        async with self._command_lock:
            if self._reader is None or self._writer is None:
                raise RuntimeError("Redis client is not connected")
            encoded_parts = [str(part).encode("utf-8") for part in parts]
            frame = [f"*{len(encoded_parts)}\r\n".encode("ascii")]
            for part in encoded_parts:
                frame.append(f"${len(part)}\r\n".encode("ascii"))
                frame.append(part)
                frame.append(b"\r\n")
            self._writer.write(b"".join(frame))
            await self._writer.drain()
            return await self._read_value()

    async def _read_value(self) -> Any:
        assert self._reader is not None
        prefix = await self._reader.readexactly(1)
        if prefix == b"+":
            return (await self._reader.readline()).rstrip(b"\r\n").decode("utf-8", "replace")
        if prefix == b"-":
            message = (await self._reader.readline()).rstrip(b"\r\n").decode("utf-8", "replace")
            raise RespError(message)
        if prefix == b":":
            return int((await self._reader.readline()).rstrip(b"\r\n"))
        if prefix == b"$":
            size = int((await self._reader.readline()).rstrip(b"\r\n"))
            if size < 0:
                return None
            data = await self._reader.readexactly(size)
            await self._reader.readexactly(2)
            return data.decode("utf-8", "replace")
        if prefix == b"*":
            size = int((await self._reader.readline()).rstrip(b"\r\n"))
            if size < 0:
                return None
            return [await self._read_value() for _ in range(size)]
        raise RespError(f"unknown Redis RESP prefix: {prefix!r}")

    def close(self) -> None:
        writer = self._writer
        self._reader = None
        self._writer = None
        if writer is not None:
            writer.close()


def _stream_fields_to_dict(fields: Any) -> dict[str, Any]:
    out: dict[str, Any] = {}
    if not isinstance(fields, list):
        return out
    for idx in range(0, len(fields), 2):
        if idx + 1 < len(fields):
            out[str(fields[idx])] = fields[idx + 1]
    return out


def _parse_stream_response(value: Any) -> list[dict[str, Any]]:
    messages: list[dict[str, Any]] = []
    if not isinstance(value, list):
        return messages
    for stream in value:
        if not isinstance(stream, list) or len(stream) < 2 or not isinstance(stream[1], list):
            continue
        for item in stream[1]:
            if not isinstance(item, list) or len(item) < 2:
                continue
            fields = _stream_fields_to_dict(item[1])
            payload = fields.get("payload")
            if isinstance(payload, str):
                try:
                    parsed = json.loads(payload)
                except json.JSONDecodeError:
                    parsed = {"rawPayload": payload}
            else:
                parsed = dict(fields)
            if isinstance(parsed, dict):
                parsed["redisId"] = item[0]
                messages.append(parsed)
    return messages


async def xadd_json(redis: AsyncRedisClient, stream: str, event: dict[str, Any]) -> Any:
    return await redis.command("XADD", stream, "*", "payload", json.dumps(event, ensure_ascii=False))


async def _publish_once(
    redis: AsyncRedisClient,
    settings: RedisTeamSettings,
    dedupe_key: str,
    event: dict[str, Any],
    *,
    ttl_seconds: int = 7 * 24 * 60 * 60,
) -> dict[str, Any]:
    claimed = await redis.command("SET", dedupe_key, json.dumps(event, ensure_ascii=False), "NX", "EX", ttl_seconds)
    if claimed != "OK":
        return {"published": False, "streamId": ""}
    stream_id = await xadd_json(redis, events_key(settings), event)
    return {"published": True, "streamId": stream_id}


async def _completion_ack(
    redis: AsyncRedisClient,
    settings: RedisTeamSettings,
    completion_id: str,
    attempt_id: str,
    *,
    wait_seconds: float = 5.0,
) -> Optional[dict[str, Any]]:
    deadline = time.monotonic() + max(0.0, wait_seconds)
    while True:
        for key in (
            _completion_ack_key(settings, completion_id, attempt_id),
            _completion_state_key(settings, completion_id),
        ):
            raw = await redis.command("GET", key)
            if raw:
                try:
                    value = json.loads(raw)
                except (TypeError, json.JSONDecodeError):
                    value = None
                if isinstance(value, dict):
                    return value
        if time.monotonic() >= deadline:
            return None
        await asyncio.sleep(0.2)


async def _observe_late_completion(
    settings: RedisTeamSettings,
    completion_id: str,
    attempt_id: str,
    task_id: str,
    status: str,
    summary: str,
    artifact_refs: list[str],
) -> None:
    redis = AsyncRedisClient(settings.redis_url)
    try:
        await redis.connect()
        acknowledgement = await _completion_ack(
            redis,
            settings,
            completion_id,
            attempt_id,
            wait_seconds=300,
        )
    except Exception:
        return
    finally:
        redis.close()
    decision = _trim((acknowledgement or {}).get("decision")).lower()
    if decision not in {"accepted", "rejected"}:
        return
    active = _load_active_envelope(settings)
    if _trim(active.get("taskId") or active.get("rootTaskId")) == task_id:
        active["completionDecision"] = decision
        active["terminal"] = decision == "accepted"
        active["terminalStatus"] = status if decision == "accepted" else ""
        active["completionId"] = completion_id
        if decision == "accepted":
            active["completedAt"] = _now_iso()
        _persist_active_envelope(settings, active)
    write_local_status(
        settings,
        {
            "availability": "idle" if decision == "accepted" and status == "succeeded" else "blocked",
            "runtimeStatus": status if decision == "accepted" else "blocked",
            "currentTaskId": task_id,
            "progress": 100 if decision == "accepted" else 99,
            "lastSummary": _trim((acknowledgement or {}).get("reason")) or summary,
            "artifactRefs": artifact_refs,
        },
    )


async def _propose_completion(
    settings: RedisTeamSettings,
    envelope: dict[str, Any],
    *,
    status: str,
    summary: str,
    result_markdown: str,
    artifact_refs: Optional[list[str]] = None,
    explicit: bool,
    review_verdict: str = "",
    reviewed_assignment_id: str = "",
    reviewed_revision: Optional[int] = None,
    reviewed_artifact_refs: Optional[list[str]] = None,
) -> dict[str, Any]:
    if not settings.valid:
        raise ValueError("Redis Team env is incomplete")
    status = _trim(status).lower()
    if status not in {"succeeded", "failed", "cancelled"}:
        raise ValueError("completion status must be succeeded, failed or cancelled")
    task_id = _trim(envelope.get("taskId") or envelope.get("rootTaskId"))
    if not task_id:
        raise ValueError("active task identity is unavailable")
    canonical_review_refs = _canonicalize_reviewer_completion_report(
        settings,
        envelope,
        result_markdown,
        artifact_refs,
    )
    normalized_artifact_refs = [*(artifact_refs or []), *canonical_review_refs]
    result = write_task_result(
        settings,
        task_id,
        envelope=envelope,
        status=status,
        summary=summary,
        result_markdown=result_markdown,
        artifact_refs=normalized_artifact_refs,
    )
    completion_id = _completion_id(settings, envelope)
    attempt_id = f"attempt_{uuid.uuid4().hex}"
    artifact_meta = artifact_metadata(settings, result["artifactRefs"])
    reviewed_refs = validate_artifact_refs(settings, reviewed_artifact_refs)
    reviewed_meta = artifact_metadata(settings, reviewed_refs)
    result_content_hash = hashlib.sha256(
        (result_markdown + "\n" + "\n".join(result["artifactRefs"])).encode("utf-8")
    ).hexdigest()
    completion = _task_event(
        settings,
        "completion_proposed",
        envelope,
        {
            "completionId": completion_id,
            "attemptId": attempt_id,
            # Business completion is emitted only by the explicit completion
            # tool. A finished model turn without that receipt remains running
            # and is handled by the out-of-band Monitor.
            "completionSource": COMPLETION_SOURCE,
            "explicitCompletion": True,
            "agentInvokedCompletionTool": explicit or None,
            "assignmentResultOnly": True,
            "rootTaskTerminal": False,
            "status": status,
            "availability": "idle" if status == "succeeded" else "blocked",
            "runtimeStatus": "completion_pending",
            "workflowFinal": False,
            "finalAnswerReady": False,
            "summary": summary,
            "result": result_markdown,
            "resultMarkdown": result_markdown,
            "artifactRefs": result["artifactRefs"],
            "artifactMetadata": artifact_meta,
            "resultContentHash": result_content_hash,
            "reviewVerdict": review_verdict or None,
            "reviewedAssignmentId": reviewed_assignment_id or None,
            "reviewedRevision": reviewed_revision,
            "reviewedArtifactRefs": reviewed_refs,
            "reviewedArtifactMetadata": reviewed_meta,
            "visibleToChat": False,
            "chatPolicy": "hidden",
        },
    )
    redis = AsyncRedisClient(settings.redis_url)
    try:
        await redis.connect()
        published = await _publish_once(
            redis,
            settings,
            _completion_attempt_key(settings, completion_id, attempt_id),
            completion,
        )
        acknowledgement = await _completion_ack(redis, settings, completion_id, attempt_id)
    finally:
        redis.close()
    decision = _trim((acknowledgement or {}).get("decision")).lower() or "submitted"
    runtime_status = "completion_pending"
    availability = "busy"
    if decision == "accepted":
        runtime_status = status
        availability = "idle" if status == "succeeded" else "blocked"
    elif decision == "rejected":
        runtime_status = "running"
        availability = "busy"
    active = _load_active_envelope(settings)
    if _trim(active.get("taskId") or active.get("rootTaskId")) == task_id:
        active["completionDecision"] = decision
        active["completionId"] = completion_id
        if explicit:
            active["explicitCompletionSubmitted"] = True
        if decision == "accepted":
            active["terminal"] = True
            active["terminalStatus"] = status
            active["completedAt"] = _now_iso()
        elif decision == "rejected":
            active["terminal"] = False
            active["terminalStatus"] = ""
        _persist_active_envelope(settings, active)
    if decision in {"submitted", "deferred"}:
        asyncio.create_task(
            _observe_late_completion(
                settings,
                completion_id,
                attempt_id,
                task_id,
                status,
                summary,
                result["artifactRefs"],
            )
        )
    write_local_status(
        settings,
        {
            "availability": availability,
            "runtimeStatus": runtime_status,
            "currentTaskId": task_id,
            "currentAssignmentId": envelope.get("assignmentId") or envelope.get("workId"),
            "progress": 100 if decision == "accepted" else 99,
            "lastSummary": _trim((acknowledgement or {}).get("reason")) or summary,
            "artifactRefs": result["artifactRefs"],
        },
    )
    return {
        "ok": True,
        **result,
        **published,
        "completionId": completion_id,
        "attemptId": attempt_id,
        "decision": decision,
        "acknowledgement": acknowledgement,
    }


async def _publish_event(settings: RedisTeamSettings, event: str, payload: dict[str, Any]) -> None:
    if not settings.valid:
        return
    redis = AsyncRedisClient(settings.redis_url)
    try:
        await redis.connect()
        await xadd_json(redis, events_key(settings), event_for(settings, event, payload))
    finally:
        redis.close()


def _normalized_send_intent(value: Any) -> str:
    return re.sub(r"[\s-]+", "_", _trim(value).lower())


def _send_intent_is_context(value: Any) -> bool:
    return _normalized_send_intent(value) in {
        "context", "context_update", "peer_request", "question", "reminder",
        "follow_up", "status_check", "assignment_status_check", "notification", "ack",
    }


def _stable_hermes_assignment_id(settings: RedisTeamSettings, root_task_id: str, target: str, title: str, text: str) -> str:
    digest = hashlib.sha256(
        "\n".join([settings.team_id, settings.member_id, root_task_id, target, title, text]).encode("utf-8")
    ).hexdigest()[:12]
    return f"assignment-{_safe_name(target)}-{digest}"


def _hermes_business_delivery_from_ledger(
    settings: RedisTeamSettings,
    message: dict[str, Any],
    workflow_state: dict[str, Any],
    *,
    explicit_assignment_id: bool,
    recent_target_dispatch: Optional[dict[str, Any]] = None,
    target_status: Optional[dict[str, Any]] = None,
) -> dict[str, Any]:
    intent = _normalized_send_intent(message.get("intent"))
    if "leader" in _trim(message.get("to")).lower():
        return {"kind": "context", "reason": "message_to_leader", "revision": 1, "authorized": False}
    if _send_intent_is_context(intent):
        return {"kind": "peer_request" if intent == "peer_request" else "context", "reason": "explicit_context_intent", "revision": 1, "authorized": False}
    assignments_value = workflow_state.get("assignments")
    assignments = list(assignments_value.values()) if isinstance(assignments_value, dict) else []
    assignments = [entry for entry in assignments if isinstance(entry, dict)]
    existing = assignments_value.get(message.get("assignmentId")) if isinstance(assignments_value, dict) else None
    target_assignments = [
        entry for entry in assignments
        if _trim(entry.get("ownerMemberKey")) == _trim(message.get("to"))
    ]
    target_assignments.sort(
        key=lambda entry: (int(entry.get("revision") or 1), _trim(entry.get("updatedAt"))),
        reverse=True,
    )
    latest = target_assignments[0] if target_assignments else None
    if isinstance(existing, dict):
        revision = max(1, int(existing.get("revision") or 1))
        assignment_id = _trim(existing.get("assignmentId") or existing.get("workId"))
        owner = _trim(existing.get("ownerMemberKey"))
        if owner and owner != _trim(message.get("to")):
            return {"kind": "ambiguous", "reason": "assignment_owner_conflict", "revision": revision, "authorized": False}
        status = _trim(existing.get("status")).lower()
        if status in {"pending", "dispatched", "running"}:
            return {"kind": "context", "reason": "existing_attempt_active", "assignmentId": assignment_id, "revision": revision, "authorized": False}
        runtime_status = _trim((target_status or {}).get("runtimeStatus")).lower()
        availability = _trim((target_status or {}).get("availability")).lower()
        status_assignment = _trim((target_status or {}).get("currentAssignmentId") or (target_status or {}).get("assignmentId") or (target_status or {}).get("workId"))
        status_revision = max(1, int((target_status or {}).get("currentRevision") or 1))
        last_seen = _trim((target_status or {}).get("lastSeenAt"))
        try:
            last_seen_at = datetime.fromisoformat(last_seen.replace("Z", "+00:00")).timestamp()
        except ValueError:
            last_seen_at = 0.0
        runtime_active = (
            (runtime_status in {"running", "busy", "completion_pending", "recovering"} or availability == "busy")
            and last_seen_at > 0
            and time.time() - last_seen_at <= 120
            and status_assignment == assignment_id
            and status_revision == revision
        )
        if runtime_active:
            return {"kind": "context", "reason": "runtime_attempt_still_active", "assignmentId": assignment_id, "revision": revision, "authorized": False}
        if existing.get("nextRevisionAllowed") is True and int(existing.get("nextRevision") or 0) == revision + 1:
            return {"kind": "assignment", "reason": "ledger_authorized_recovery", "assignmentId": assignment_id, "revision": revision + 1, "authorized": True}
        return {"kind": "ambiguous", "reason": "terminal_attempt_follow_up", "assignmentId": assignment_id, "revision": revision, "authorized": False}
    if not explicit_assignment_id and isinstance(latest, dict):
        revision = max(1, int(latest.get("revision") or 1))
        assignment_id = _trim(latest.get("assignmentId") or latest.get("workId"))
        return {"kind": "ambiguous", "reason": "target_has_existing_assignment", "assignmentId": assignment_id, "revision": revision, "authorized": False}
    if not explicit_assignment_id and isinstance(recent_target_dispatch, dict) and recent_target_dispatch:
        return {
            "kind": "ambiguous",
            "reason": "target_has_recent_unprojected_assignment",
            "assignmentId": _trim(recent_target_dispatch.get("assignmentId") or recent_target_dispatch.get("workId")),
            "revision": max(1, int(recent_target_dispatch.get("revision") or 1)),
            "authorized": False,
        }
    sender_is_leader = "leader" in settings.role.lower() or "leader" in settings.member_id.lower()
    if not sender_is_leader and not explicit_assignment_id:
        return {"kind": "peer_request", "reason": "member_message_without_assignment_contract", "revision": 1, "authorized": False}
    return {"kind": "assignment", "reason": "new_assignment_contract", "assignmentId": message.get("assignmentId"), "revision": 1, "authorized": True}


async def _tool_team_send(args: dict[str, Any], **_kwargs) -> str:
    settings = load_settings(None)
    if not settings.valid:
        return _team_tool_json_result(settings, "team_send", {"ok": False, "retryable": True, "error": "Redis Team env is incomplete"})
    target_values = {
        _trim(args.get(key))
        for key in ("to", "recipient", "targetMemberId", "target_member_id")
        if _trim(args.get(key))
    }
    text_values = {
        _trim(args.get(key))
        for key in ("text", "message", "prompt")
        if _trim(args.get(key))
    }
    if len(target_values) > 1:
        return _team_tool_json_result(settings, "team_send", {
            "ok": False,
            "retryable": True,
            "clarificationRequired": True,
            "error": "Conflicting Team recipient fields; confirm one current roster member.",
            "candidates": sorted(target_values),
        })
    if len(text_values) > 1:
        return _team_tool_json_result(settings, "team_send", {
            "ok": False,
            "retryable": True,
            "clarificationRequired": True,
            "error": "Conflicting Team message fields; provide one text value.",
            "code": "conflicting_team_message",
        })
    raw_to = next(iter(target_values), "")
    text = next(iter(text_values), "")
    if not raw_to or not text:
        return _team_tool_json_result(settings, "team_send", {
            "ok": False,
            "retryable": True,
            "error": "A recipient and text are required",
        })
    target_resolution = _resolve_roster_target(settings, raw_to)
    to = _trim(target_resolution.get("memberId"))
    if not to:
        active = _load_active_envelope(settings)
        warning = {
            "eventKind": "target_resolution_warning",
            "failureDomain": "transport",
            "failureKind": "target_resolution",
            "retryable": True,
            "nonAuthoritative": True,
            "stateEffect": "none",
            "rootTaskTerminal": False,
            "clarificationRequired": True,
            "status": "attention_required",
            "runtimeStatus": "running",
            "availability": "busy",
            "taskId": _trim(active.get("taskId") or active.get("rootTaskId")),
            "rootTaskId": _trim(active.get("rootTaskId") or active.get("taskId")),
            "rootMessageId": _trim(active.get("rootMessageId") or active.get("messageId")),
            "assignmentId": _trim(active.get("assignmentId") or active.get("workId")),
            "workId": _trim(active.get("workId") or active.get("assignmentId")),
            "revision": max(1, int(active.get("revision") or 1)),
            "from": settings.member_id,
            "to": raw_to,
            "targetCandidates": target_resolution.get("candidates") or [],
            "targetSuggestions": target_resolution.get("suggestions") or [],
            "summary": "Redis Team recipient could not be uniquely resolved from the current roster.",
        }
        await _publish_event(settings, "message_warning", warning)
        return _team_tool_json_result(settings, "team_send", {
            "ok": False,
            "sent": False,
            "messageDelivered": False,
            "retryable": True,
            "nonAuthoritative": True,
            "clarificationRequired": True,
            "candidates": target_resolution.get("candidates") or [],
            "suggestions": target_resolution.get("suggestions") or [],
            "error": "Recipient is ambiguous or unknown; confirm one current roster member.",
        })
    active = _load_active_envelope(settings)
    task_id = _trim(args.get("taskId")) or _trim(active.get("taskId") or active.get("rootTaskId"))
    root_task_id = _trim(active.get("rootTaskId") or task_id)
    intent = _normalized_send_intent(args.get("intent") or "send")
    explicit_assignment_id = _trim(args.get("assignmentId") or args.get("workId"))
    title = _trim(args.get("title")) or "Team Message"
    trusted_recovery_assignment_id = ""
    if (
        not explicit_assignment_id
        and _normalized_send_intent(active.get("intent")) == "assignment_recovery_request"
        and _trim(active.get("from")).lower() == "clawmanager"
    ):
        trusted_recovery_assignment_id = _trim(active.get("assignmentId") or active.get("workId"))
    assignment_id = explicit_assignment_id or trusted_recovery_assignment_id or _stable_hermes_assignment_id(settings, root_task_id, to, title, text)
    message = {
        "v": WIRE_SCHEMA_VERSION,
        "protocolVersion": PROTOCOL_VERSION,
        "messageId": f"msg_{uuid.uuid4().hex}",
        "teamId": settings.team_id,
        "from": settings.member_id,
        "to": to,
        "intent": intent or "send",
        "taskId": task_id,
        "rootTaskId": root_task_id,
        "rootMessageId": _trim(active.get("rootMessageId")),
        "workId": assignment_id,
        "assignmentId": assignment_id,
        "phaseId": _trim(args.get("phaseId")) or _trim(active.get("phaseId")),
        "revision": 1,
        "required": _truthy(args.get("required"), True),
        "reviewRequired": _truthy(args.get("reviewRequired") or args.get("review_required"), False),
        "validationRequired": _truthy(args.get("validationRequired") or args.get("validation_required"), False),
        "validationAssignment": _truthy(args.get("validationAssignment") or args.get("validation_assignment"), False),
        "validationTargetAssignmentId": _trim(
            args.get("validationTargetAssignmentId")
            or args.get("reviewedAssignmentId")
        ),
        "validationTargetRevision": int(
            args.get("validationTargetRevision")
            or args.get("reviewedRevision")
            or 0
        ),
        "dependsOn": [
            _trim(value)
            for value in (args.get("dependsOn") if isinstance(args.get("dependsOn"), list) else [])
            if _trim(value)
        ],
        "title": title,
        "text": text,
        "contextRefs": args.get("contextRefs") if isinstance(args.get("contextRefs"), list) else [],
        "ttlSeconds": args.get("ttlSeconds") if isinstance(args.get("ttlSeconds"), int) else 3600,
        "priority": _trim(args.get("priority")) or "normal",
        "metadata": args.get("metadata") if isinstance(args.get("metadata"), dict) else {},
        "createdAt": _now_iso(),
    }
    redis = AsyncRedisClient(settings.redis_url)
    try:
        await redis.connect()
        workflow_state = await _read_root_workflow_state(redis, settings, root_task_id)
        recent_target_dispatch = {}
        if not explicit_assignment_id and not trusted_recovery_assignment_id:
            recent_target_dispatch = await _recent_target_assignment_dispatch(
                redis, settings, root_task_id, to
            )
        decision = _hermes_business_delivery_from_ledger(
            settings,
            message,
            workflow_state,
            explicit_assignment_id=bool(explicit_assignment_id or trusted_recovery_assignment_id),
            recent_target_dispatch=recent_target_dispatch,
            target_status=read_team_statuses(settings, to) or {},
        )
        if _trim(decision.get("assignmentId")):
            message["assignmentId"] = _trim(decision.get("assignmentId"))
            message["workId"] = message["assignmentId"]
        business_delivery_kind = _trim(decision.get("kind")) or "ambiguous"
        message.update({
            "revision": max(1, int(decision.get("revision") or 1)),
            "businessDeliveryKind": business_delivery_kind,
            "businessDeliveryReason": decision.get("reason"),
            "deliverySemanticsVersion": 1,
            "decisionLedgerVersion": int(workflow_state.get("ledgerVersion") or 0),
            "decisionPlanVersion": int(workflow_state.get("planVersion") or 0),
            "businessMutation": business_delivery_kind == "assignment",
            "requiresCompletion": business_delivery_kind == "assignment",
            "revisionAuthorized": decision.get("authorized") is True,
            "sourceTool": "team_send",
            "agentIntent": _trim(args.get("intent")) or None,
            "nonAuthoritative": business_delivery_kind != "assignment",
        })
        redis_id = await xadd_json(redis, inbox_key(settings, to), message)
        if business_delivery_kind == "assignment":
            dispatch_state = json.dumps({
                "assignmentId": message["assignmentId"],
                "workId": message["workId"],
                "revision": message["revision"],
                "rootTaskId": root_task_id,
                "to": to,
                "messageId": message["messageId"],
                "status": "dispatched",
                "createdAt": message["createdAt"],
            }, ensure_ascii=False)
            dispatch_key = assignment_dispatch_state_key(settings, root_task_id)
            await redis.command("HSET", dispatch_key, message["assignmentId"], dispatch_state)
            await redis.command("EXPIRE", dispatch_key, 604800)
        await xadd_json(
            redis,
            events_key(settings),
            event_for(settings, "peer_request" if business_delivery_kind == "peer_request" else "outbound", {
                **message,
                "inReplyTo": _trim(active.get("messageId")),
            }),
        )
    finally:
        redis.close()
    message["redisId"] = redis_id
    response: dict[str, Any] = {"ok": True, "sent": message}
    if message.get("businessDeliveryKind") == "ambiguous":
        response.update({
            "deliveryState": "delivered_as_context",
            "clarificationRequired": True,
            "leaderGuidance": (
                "The message was delivered without creating a new Work Item because the current ledger does not "
                "uniquely identify a new business assignment. If this is a real next stage, resend it with a distinct "
                "assignmentId; if it is rework, first confirm the exact failed or stale attempt."
            ),
        })
    return _team_tool_json_result(settings, "team_send", response)


async def _tool_team_status(args: dict[str, Any], **_kwargs) -> str:
    settings = load_settings(None)
    if not settings.enabled:
        return _team_tool_json_result(settings, "team_status", {"ok": False, "retryable": True, "error": "Redis Team is disabled"})
    return _team_tool_json_result(
        settings,
        "team_status",
        {"ok": True, "status": read_team_statuses(settings, _trim(args.get("memberId")))},
    )


async def _tool_team_update_progress(args: dict[str, Any], **_kwargs) -> str:
    settings = load_settings(None)
    if not settings.enabled:
        return _team_tool_json_result(settings, "team_update_progress", {"ok": False, "retryable": True, "error": "Redis Team is disabled"})
    active = _load_active_envelope(settings)
    reported_task_id = _trim(args.get("taskId"))
    active_task_id = _trim(active.get("rootTaskId") or active.get("taskId"))
    task_id = active_task_id or reported_task_id
    status_text = _trim(args.get("status")).lower()
    summary = _trim(args.get("summary"))
    if not task_id or not status_text:
        return _team_tool_json_result(settings, "team_update_progress", {"ok": False, "retryable": True, "error": "taskId and status are required"})
    progress = args.get("progress")
    waiting = status_text in {"blocked", "waiting", "dependency_wait", "completion_pending"}
    idle = status_text == "idle"
    local_runtime_status = "waiting" if waiting else "idle" if idle else "running"
    status = write_local_status(
        settings,
        {
            "availability": "idle" if idle else "busy",
            "runtimeStatus": local_runtime_status,
            "currentTaskId": task_id,
            "progress": progress if isinstance(progress, (int, float)) else None,
            "lastSummary": summary or status_text,
            "artifactRefs": args.get("artifactRefs") if isinstance(args.get("artifactRefs"), list) else [],
        },
    )
    progress_payload = {
        **dict(args),
        "taskId": task_id,
        "rootTaskId": active.get("rootTaskId") or task_id,
        "rootMessageId": active.get("rootMessageId"),
        "workId": active.get("assignmentId") or active.get("workId"),
        "assignmentId": active.get("assignmentId") or active.get("workId"),
        "phaseId": active.get("phaseId"),
        "revision": active.get("revision") or 1,
        "eventKind": _trim(args.get("eventKind")) or "worker_progress",
        "status": "waiting" if waiting else status_text,
        "runtimeStatus": local_runtime_status,
        "availability": "idle" if idle else "busy",
        "reportedTaskId": reported_task_id if reported_task_id and reported_task_id != task_id else None,
    }
    await _publish_event(settings, "task_progress", progress_payload)
    return _team_tool_json_result(settings, "team_update_progress", {"ok": True, "status": status})


async def _tool_team_complete_task(args: dict[str, Any], **_kwargs) -> str:
    settings = load_settings(None)
    if not settings.enabled:
        return _team_tool_json_result(settings, "team_complete_task", {"ok": False, "retryable": True, "error": "Redis Team is disabled"})
    active = _load_active_envelope(settings)
    reported_task_id = _trim(args.get("taskId"))
    active_task_id = _trim(active.get("rootTaskId") or active.get("taskId"))
    task_id = active_task_id or reported_task_id
    status_text = _trim(args.get("status")).lower()
    summary = _trim(args.get("summary"))
    if not task_id or not status_text or not summary:
        return _team_tool_json_result(settings, "team_complete_task", {"ok": False, "retryable": True, "error": "taskId, status and summary are required"})
    if status_text not in {"succeeded", "failed", "cancelled"}:
        return _team_tool_json_result(settings, "team_complete_task", {"ok": False, "retryable": True, "error": "status must be succeeded, failed or cancelled"})
    if reported_task_id and reported_task_id != task_id:
        active["reportedTaskId"] = reported_task_id
    active["taskId"] = active.get("taskId") or task_id
    active["rootTaskId"] = active.get("rootTaskId") or task_id
    _persist_active_envelope(settings, active)
    try:
        result = await _propose_completion(
            settings,
            active,
            status=status_text,
            summary=summary,
            result_markdown=_trim(args.get("resultMarkdown")) or summary,
            artifact_refs=args.get("artifactRefs") if isinstance(args.get("artifactRefs"), list) else [],
            explicit=True,
            review_verdict=_trim(args.get("reviewVerdict")),
            reviewed_assignment_id=_trim(args.get("reviewedAssignmentId")),
            reviewed_revision=int(args.get("reviewedRevision")) if args.get("reviewedRevision") is not None else None,
            reviewed_artifact_refs=args.get("reviewedArtifactRefs") if isinstance(args.get("reviewedArtifactRefs"), list) else [],
        )
    except Exception as exc:
        return _team_tool_json_result(settings, "team_complete_task", {"ok": False, "retryable": True, "error": str(exc)})
    normalized_result = dict(result) if isinstance(result, dict) else {"ok": True, "result": result}
    normalized_result.setdefault("ok", True)
    return _team_tool_json_result(settings, "team_complete_task", normalized_result)


class RedisTeamAdapter(BasePlatformAdapter):
    def __init__(self, config: PlatformConfig):
        super().__init__(config=config, platform=Platform("redis_team"))
        self.settings = load_settings(config)
        self._redis: Optional[AsyncRedisClient] = None
        self._consumer_redis: Optional[AsyncRedisClient] = None
        self._consumer_task: Optional[asyncio.Task] = None
        self._presence_task: Optional[asyncio.Task] = None
        self._lifecycle_lock = asyncio.Lock()
        self._redis_reconnect_lock = asyncio.Lock()
        # Track accepted work by message, not only by assignment. A recovery or
        # correction may legitimately enqueue more than one turn for the same
        # assignment; completing one turn must not make the other look stale.
        self._accepted_messages: Dict[str, tuple[str, str]] = {}
        # Business Assignment lifetime is intentionally separate from the
        # Redis transport and from an individual Hermes model turn. It remains
        # active while completion is pending/deferred or a corrective turn is
        # still required, and ends only after an accepted terminal outcome.
        self._active_assignments: Dict[tuple[str, str], dict[str, Any]] = {}
        # A message can be accepted by Hermes before Redis records the
        # processed marker/ACK. Keep that transport boundary distinct from
        # model activity so a reconnect finalizes delivery without dispatching
        # the same turn twice.
        self._transport_accepted_messages: set[str] = set()
        self._redis_reply_metadata: Dict[str, Dict[str, Any]] = {}
        self._turn_responses: Dict[str, str] = {}
        self._approval_session_by_key: Dict[str, str] = {}
        self._latest_approval_session_key = ""

    @property
    def name(self) -> str:
        return "Redis Team"

    def _track_active_assignment(self, envelope: dict[str, Any]) -> tuple[str, str]:
        identity = _assignment_identity(envelope)
        if not all(identity):
            return identity
        current = self._active_assignments.get(identity, {})
        message_ids = set(current.get("messageIds") or [])
        message_id = _trim(envelope.get("messageId"))
        if message_id:
            message_ids.add(message_id)
        self._active_assignments[identity] = {
            "envelope": dict(envelope),
            "messageIds": message_ids,
            "startedAt": current.get("startedAt") or _now_iso(),
        }
        return identity

    def _assignment_is_locally_terminal(self, identity: tuple[str, str]) -> bool:
        root_task_id, assignment_id = identity
        if not root_task_id or not assignment_id:
            return False
        active = _load_active_envelope(self.settings)
        if _assignment_identity(active) == identity and active.get("terminal") is True:
            return True
        status = read_team_statuses(self.settings, self.settings.member_id) or {}
        status_task_id = _trim(status.get("currentTaskId"))
        status_assignment_id = _trim(status.get("currentAssignmentId"))
        runtime_status = _trim(status.get("runtimeStatus")).lower()
        return bool(
            status_task_id == root_task_id
            and (not status_assignment_id or status_assignment_id == assignment_id)
            and runtime_status in {"succeeded", "failed", "cancelled"}
        )

    def _retire_terminal_assignment(self, identity: tuple[str, str]) -> bool:
        if identity not in self._active_assignments:
            return False
        if not self._assignment_is_locally_terminal(identity):
            return False
        self._active_assignments.pop(identity, None)
        return True

    def _has_active_assignment(self, identity: tuple[str, str]) -> bool:
        if not all(identity):
            return False
        if self._assignment_is_locally_terminal(identity):
            self._active_assignments.pop(identity, None)
            return False
        if identity in self._active_assignments:
            return True
        active = _load_active_envelope(self.settings)
        if _is_formal_assignment(active) and _assignment_identity(active) == identity and not active.get("terminal"):
            self._track_active_assignment(active)
            return True
        return False

    async def _emit_assignment_lifecycle(
        self,
        event_name: str,
        envelope: dict[str, Any],
        *,
        status: str,
        summary: str,
    ) -> None:
        if not self._redis:
            raise RuntimeError("Redis Team presence connection is unavailable")
        message_id = _trim(envelope.get("messageId"))
        event_id = (
            f"assignment-lifecycle:{_safe_name(message_id)}:{event_name}"
            if message_id
            else f"assignment-lifecycle:{_safe_name(envelope.get('taskId'))}:{event_name}"
        )
        await xadd_json(
            self._redis,
            events_key(self.settings),
            _task_event(
                self.settings,
                event_name,
                envelope,
                {
                    "eventId": event_id,
                    "availability": "busy",
                    "runtimeStatus": "running",
                    "status": status,
                    "summary": summary,
                    "visibleToChat": False,
                    "chatPolicy": "hidden",
                },
            ),
        )

    async def _open_redis_clients(
        self,
        *,
        status_patch: Optional[dict[str, Any]] = None,
    ) -> tuple[AsyncRedisClient, AsyncRedisClient, dict[str, Any]]:
        presence = AsyncRedisClient(self.settings.redis_url)
        consumer = AsyncRedisClient(self.settings.redis_url)
        try:
            await presence.connect()
            try:
                await presence.command("CLIENT", "SETNAME", _redis_client_name(self.settings, "presence"))
            except Exception:
                pass
            await consumer.connect()
            try:
                await consumer.command("CLIENT", "SETNAME", _redis_client_name(self.settings, "consumer"))
            except Exception:
                pass
            try:
                await presence.command(
                    "XGROUP",
                    "CREATE",
                    inbox_key(self.settings),
                    self.settings.consumer_group,
                    "0",
                    "MKSTREAM",
                )
            except RespError as exc:
                if "BUSYGROUP" not in str(exc):
                    raise
            status = write_local_status(self.settings, status_patch or {"availability": "idle"})
            await presence.command(
                "HSET",
                presence_key(self.settings),
                self.settings.member_id,
                json.dumps(status, ensure_ascii=False),
            )
            return presence, consumer, status
        except Exception:
            consumer.close()
            presence.close()
            raise

    async def _reconnect_redis_clients(self, failed_client: AsyncRedisClient) -> bool:
        async with self._redis_reconnect_lock:
            if not self.is_connected:
                return False
            if failed_client is not self._redis and failed_client is not self._consumer_redis:
                return True

            try:
                previous_status = read_team_statuses(self.settings, self.settings.member_id) or {}
            except Exception as exc:
                logger.warning("Redis Team: could not read local status before reconnect: %s", exc)
                previous_status = {}
            previous_runtime_status = _trim(previous_status.get("runtimeStatus")) or "idle"
            previous_availability = _trim(previous_status.get("availability")) or "idle"
            try:
                _clear_ready_file(self.settings)
            except Exception as exc:
                logger.warning("Redis Team: failed to clear readiness during reconnect: %s", exc)
            try:
                write_local_status(
                    self.settings,
                    {
                        "liveness": "reconnecting",
                        "runtimeStatus": "reconnecting",
                    },
                )
            except Exception:
                pass

            attempt = 0
            while self.is_connected:
                delay = REDIS_RECONNECT_BACKOFF_SECONDS[min(attempt, len(REDIS_RECONNECT_BACKOFF_SECONDS) - 1)]
                try:
                    presence, consumer, status = await self._open_redis_clients(
                        status_patch={
                            "liveness": "online",
                            "runtimeStatus": previous_runtime_status,
                            "availability": previous_availability,
                        }
                    )
                except asyncio.CancelledError:
                    raise
                except Exception as exc:
                    attempt += 1
                    logger.warning(
                        "Redis Team: reconnect attempt %s failed: %s; retrying in %ss",
                        attempt,
                        exc,
                        delay,
                    )
                    await asyncio.sleep(delay)
                    continue

                old_presence = self._redis
                old_consumer = self._consumer_redis
                self._redis = presence
                self._consumer_redis = consumer
                if old_consumer:
                    old_consumer.close()
                if old_presence:
                    old_presence.close()
                try:
                    _publish_ready_file(self.settings, status)
                except Exception as exc:
                    logger.error("Redis Team: failed to restore readiness after reconnect: %s", exc)
                    consumer.close()
                    presence.close()
                    self._consumer_redis = None
                    self._redis = None
                    attempt += 1
                    await asyncio.sleep(delay)
                    continue
                logger.info(
                    "Redis Team: Redis connections restored team=%s member=%s",
                    self.settings.team_id,
                    self.settings.member_id,
                )
                return True
            return False

    async def connect(self, is_reconnect: bool = False, **_kwargs: Any) -> bool:
        async with self._lifecycle_lock:
            try:
                _clear_ready_file(self.settings)
            except Exception as exc:
                logger.error("Redis Team: invalid readiness path: %s", exc)
                self._set_fatal_error("readiness_path_invalid", str(exc), retryable=False)
                return False
            if not self.settings.enabled:
                logger.info("Redis Team: disabled")
                return False
            if not self.settings.valid:
                logger.error("Redis Team: CLAWMANAGER_TEAM_REDIS_URL, TEAM_ID and MEMBER_ID are required")
                self._set_fatal_error("config_missing", "Redis Team env is incomplete", retryable=False)
                _record_startup_failure(
                    self.settings,
                    code="config_missing",
                    message="Redis Team env is incomplete",
                    retryable=False,
                )
                return False
            if not Path(self.settings.shared_dir).is_absolute():
                logger.error("Redis Team: CLAWMANAGER_TEAM_SHARED_DIR must be absolute")
                self._set_fatal_error("invalid_shared_dir", "CLAWMANAGER_TEAM_SHARED_DIR must be absolute", retryable=False)
                _record_startup_failure(
                    self.settings,
                    code="invalid_shared_dir",
                    message="CLAWMANAGER_TEAM_SHARED_DIR must be absolute",
                    retryable=False,
                )
                return False
            if self._consumer_task and not self._consumer_task.done():
                logger.info("Redis Team: consumer already running for member=%s", self.settings.member_id)
                return True

            await self._disconnect_unlocked(mark_offline=False)
            try:
                ensure_team_dirs(self.settings)
                write_local_status(self.settings, {"availability": "idle"})
            except (OSError, ValueError) as exc:
                message = str(exc)
                logger.error("Redis Team: shared workspace is unusable: %s", message)
                self._set_fatal_error("shared_workspace_unusable", message, retryable=False)
                _record_startup_failure(
                    self.settings,
                    code="shared_workspace_unusable",
                    message=message,
                    retryable=False,
                )
                return False
            try:
                self._redis, self._consumer_redis, initial_status = await self._open_redis_clients()
            except Exception as exc:
                logger.error("Redis Team: failed to connect: %s", exc)
                self._set_fatal_error("connect_failed", str(exc), retryable=True)
                try:
                    _clear_ready_file(self.settings)
                except Exception:
                    pass
                if self._consumer_redis:
                    self._consumer_redis.close()
                    self._consumer_redis = None
                if self._redis:
                    self._redis.close()
                    self._redis = None
                return False

            self._mark_connected()
            persisted_active = _load_active_envelope(self.settings)
            if _is_formal_assignment(persisted_active) and not persisted_active.get("terminal"):
                persisted_status = read_team_statuses(self.settings, self.settings.member_id) or {}
                root_terminal = await _root_task_is_terminal(self._redis, self.settings, persisted_active)
                if root_terminal:
                    persisted_active["terminal"] = True
                    # The root projection may be succeeded, failed or
                    # cancelled. Do not invent a member outcome while merely
                    # suppressing a stale local Assignment after restart.
                    persisted_active["terminalStatus"] = ""
                    persisted_active["completedAt"] = _now_iso()
                    persisted_active["terminalNarrativePublished"] = True
                    _persist_active_envelope(self.settings, persisted_active)
                    initial_status = write_local_status(
                        self.settings,
                        {
                            "availability": "idle",
                            "runtimeStatus": "idle",
                            "currentTaskId": persisted_active.get("rootTaskId") or persisted_active.get("taskId"),
                            "currentAssignmentId": persisted_active.get("assignmentId") or persisted_active.get("workId"),
                        },
                    )
                elif _trim(persisted_status.get("runtimeStatus")).lower() not in {
                    "succeeded",
                    "failed",
                    "cancelled",
                }:
                    self._track_active_assignment(persisted_active)
                    initial_status = write_local_status(
                        self.settings,
                        {
                            "availability": "busy",
                            "runtimeStatus": _trim(persisted_status.get("runtimeStatus")) or "running",
                            "currentTaskId": persisted_active.get("rootTaskId") or persisted_active.get("taskId"),
                            "currentAssignmentId": persisted_active.get("assignmentId") or persisted_active.get("workId"),
                        },
                    )
                try:
                    await self._redis.command(
                        "HSET",
                        presence_key(self.settings),
                        self.settings.member_id,
                        json.dumps(initial_status, ensure_ascii=False),
                    )
                except Exception as exc:
                    # Readiness still carries the coherent local status and
                    # the presence loop immediately retries projection. Do not
                    # turn a transient second HSET into a false startup death.
                    logger.warning("Redis Team: restored presence projection deferred: %s", exc)
            self._presence_task = asyncio.create_task(self._presence_loop())
            self._consumer_task = asyncio.create_task(self._consumer_loop())
            try:
                _publish_ready_file(self.settings, initial_status)
            except Exception as exc:
                logger.error("Redis Team: failed to publish readiness: %s", exc)
                self._set_fatal_error("readiness_publish_failed", str(exc), retryable=False)
                await self._disconnect_unlocked(mark_offline=False)
                _record_startup_failure(
                    self.settings,
                    code="readiness_publish_failed",
                    message=str(exc),
                    retryable=False,
                )
                return False
            logger.info(
                "Redis Team: connected team=%s member=%s group=%s",
                self.settings.team_id,
                self.settings.member_id,
                self.settings.consumer_group,
            )
            return True

    async def disconnect(self) -> None:
        async with self._lifecycle_lock:
            await self._disconnect_unlocked(mark_offline=True)

    async def _disconnect_unlocked(self, *, mark_offline: bool) -> None:
        was_connected = self.is_connected
        self._mark_disconnected()
        if was_connected or not mark_offline:
            try:
                _clear_ready_file(self.settings)
            except Exception:
                pass
        for task in (self._consumer_task, self._presence_task):
            if task and not task.done():
                task.cancel()
                try:
                    await task
                except asyncio.CancelledError:
                    pass
        self._consumer_task = None
        self._presence_task = None
        self._accepted_messages.clear()
        self._active_assignments.clear()
        self._transport_accepted_messages.clear()
        self._turn_responses.clear()
        if mark_offline and self._redis:
            try:
                await self._redis.command(
                    "HSET",
                    presence_key(self.settings),
                    self.settings.member_id,
                    json.dumps(
                        {
                            "teamId": self.settings.team_id,
                            "memberId": self.settings.member_id,
                            "role": self.settings.role,
                            "liveness": "offline",
                            "lastSeenAt": _now_iso(),
                        },
                        ensure_ascii=False,
                    ),
                )
            except Exception:
                pass
        if self._consumer_redis:
            self._consumer_redis.close()
            self._consumer_redis = None
        if self._redis:
            self._redis.close()
            self._redis = None
        if mark_offline and was_connected:
            try:
                write_local_status(self.settings, {"liveness": "offline"})
            except Exception as exc:
                logger.warning("Redis Team: unable to persist offline status: %s", exc)

    async def send(
        self,
        chat_id: str,
        content: str,
        reply_to: Optional[str] = None,
        metadata: Optional[Dict[str, Any]] = None,
    ) -> SendResult:
        metadata = metadata or {}
        task_id = metadata.get("task_id") or chat_id
        target = _reply_target(self.settings, metadata)
        active = _load_active_envelope(self.settings)
        source_message_id = _trim(metadata.get("source_message_id") or metadata.get("message_id"))
        if not source_message_id:
            source_message_id = _trim(active.get("messageId"))
        if source_message_id:
            self._turn_responses[source_message_id] = content
        active_identity = _assignment_identity(active)
        active_assignment = _is_formal_assignment(active) and self._has_active_assignment(active_identity)
        terminal_callback = _is_formal_assignment(active) and not active_assignment
        active_message_id = _trim(active.get("messageId"))
        terminal_delivery_visible = bool(
            terminal_callback
            and not active.get("terminalNarrativePublished")
            and (not source_message_id or source_message_id == active_message_id)
        )
        if not terminal_callback and (not source_message_id or source_message_id == active_message_id):
            active["lastAssistantResponse"] = content
            active["lastAssistantResponseAt"] = _now_iso()
            _persist_active_envelope(self.settings, active)
        content_hash = hashlib.sha256(content.encode("utf-8")).hexdigest()
        message_id = f"agent-narrative:{_safe_name(str(active.get('messageId') or task_id))}:{content_hash[:24]}"
        event = _task_event(
            self.settings,
            "reply",
            active,
            {
                "messageId": message_id,
                "eventId": message_id,
                "taskId": task_id,
                "conversationId": metadata.get("conversation_id") or chat_id,
                "to": target,
                "text": content,
                "content": content,
                "replyTo": reply_to,
                "eventKind": "agent_narrative",
                "messageKind": "narrative",
                # Raw Hermes assistant prose remains audit telemetry. Team chat is
                # reserved for explicit plans, progress, handoffs, blockers, reviews,
                # and structured completion results.
                "chatPolicy": "hidden",
                "visibleToChat": False,
                "nonAuthoritative": True,
                "stateEffect": "none",
                "contentHash": content_hash,
                "narrativeSource": "hermes_deliver_callback",
                "lateProjection": terminal_callback or None,
                "terminalDelivery": terminal_delivery_visible or None,
                "suppressedAfterTerminal": terminal_callback and not terminal_delivery_visible or None,
            },
        )
        try:
            projection_committed = False
            if not terminal_callback:
                write_local_status(
                    self.settings,
                    {
                        # Narrative delivery is not a business state
                        # transition. Preserve running/waiting state while
                        # allowing active work to refresh its summary.
                        "lastSummary": _short_text(content),
                    },
                )
            if not self._redis:
                raise RuntimeError("Redis Team presence connection is unavailable")
            # The stable eventId/messageId makes retries idempotent at the
            # ClawManager ingestion boundary. Publish the event directly; a
            # separate pre-XADD marker could otherwise lose the final delivery
            # if Redis disconnected between the two commands.
            await xadd_json(self._redis, events_key(self.settings), event)
            projection_committed = True
            if terminal_delivery_visible and projection_committed:
                # Record the one canonical post-ACK delivery only after Redis
                # accepted its projection. A transient publish failure must
                # leave the callback retryable and visible.
                latest_active = _load_active_envelope(self.settings)
                if _assignment_identity(latest_active) == active_identity:
                    latest_active["terminalNarrativePublished"] = True
                    latest_active["terminalNarrativePublishedAt"] = _now_iso()
                    _persist_active_envelope(self.settings, latest_active)
        except Exception as exc:
            logger.warning("Redis Team: failed to publish reply: %s", exc)
            return SendResult(success=False, error=str(exc))
        return SendResult(success=True, message_id=message_id)

    async def send_exec_approval(
        self,
        chat_id: str,
        command: str,
        session_key: str,
        description: str = "",
        metadata: Optional[Dict[str, Any]] = None,
    ) -> SendResult:
        metadata = metadata or {}
        task_id = metadata.get("task_id") or chat_id
        conversation_id = metadata.get("conversation_id") or chat_id
        target = _reply_target(self.settings, metadata)
        approval_id = f"approval_{uuid.uuid4().hex}"
        self._remember_approval_session(session_key, task_id, conversation_id, target)
        text = (
            "Dangerous command requires approval.\n\n"
            f"Command:\n{command}\n\n"
            f"Reason: {description or 'command flagged'}\n\n"
            f"Approval sessionKey: {session_key}\n\n"
            "Reply with /approve, /approve session, /approve always, or /deny. "
            "Include this approval sessionKey in metadata when replying if the "
            "reply does not reuse the original taskId and conversationId."
        )
        approval_payload = {
            "v": WIRE_SCHEMA_VERSION,
            "protocolVersion": PROTOCOL_VERSION,
            "messageId": approval_id,
            "teamId": self.settings.team_id,
            "from": self.settings.member_id,
            "to": target,
            "taskId": task_id,
            "conversationId": conversation_id,
            "type": "approval_request",
            "intent": "approval_request",
            "role": self.settings.role,
            "text": text,
            "approval": {
                "sessionKey": session_key,
                "command": command,
                "description": description,
                "choices": ["approve", "approve session", "approve always", "deny"],
            },
            "metadata": {
                "sessionKey": session_key,
                "approvalSessionKey": session_key,
                "taskId": task_id,
                "conversationId": conversation_id,
                "commandPreview": _short_text(command, 300),
                "description": description,
            },
            "createdAt": _now_iso(),
        }
        event = event_for(
            self.settings,
            "approval_request",
            {
                "messageId": approval_id,
                "taskId": task_id,
                "conversationId": conversation_id,
                "to": target,
                "text": text,
                "summary": _short_text(f"Approval required: {description or command}"),
                "sessionKey": session_key,
                "approvalSessionKey": session_key,
                "approval": approval_payload["approval"],
                "metadata": approval_payload["metadata"],
            },
        )
        try:
            write_local_status(
                self.settings,
                {
                    "availability": "waiting_approval",
                    "currentTaskId": task_id,
                    "lastSummary": _short_text(f"Waiting for approval: {description or command}"),
                    "approvalSessionKey": session_key,
                },
            )
            if self._redis:
                await xadd_json(self._redis, events_key(self.settings), event)
                if target:
                    await xadd_json(self._redis, inbox_key(self.settings, target), approval_payload)
        except Exception as exc:
            logger.warning("Redis Team: failed to publish approval request: %s", exc)
            return SendResult(success=False, error=str(exc), retryable=True)
        return SendResult(success=True, message_id=approval_id, raw_response=approval_payload)

    def _remember_approval_session(
        self,
        session_key: str,
        task_id: Any = "",
        conversation_id: Any = "",
        target: Any = "",
    ) -> None:
        session_key = _trim(session_key)
        if not session_key:
            return
        self._latest_approval_session_key = session_key
        for value in (task_id, conversation_id, target):
            text = _trim(value)
            if text:
                self._approval_session_by_key[text] = session_key

    def _session_key_for_approval_response(self, envelope: dict[str, Any]) -> str:
        session_key = _approval_session_key(envelope)
        if session_key:
            return session_key
        for value in (
            envelope.get("taskId"),
            envelope.get("conversationId"),
            envelope.get("from"),
            envelope.get("to"),
        ):
            mapped = self._approval_session_by_key.get(_trim(value))
            if mapped:
                return mapped
        return self._latest_approval_session_key

    async def send_typing(self, chat_id: str, metadata=None) -> None:
        try:
            active = _load_active_envelope(self.settings)
            identity = _assignment_identity(active)
            status = read_team_statuses(self.settings, self.settings.member_id) or {}
            patch: dict[str, Any] = {
                "lastSummary": "Hermes is processing the Redis Team task",
            }
            if _is_formal_assignment(active) and self._has_active_assignment(identity):
                patch.update(
                    {
                        "availability": "busy",
                        "currentTaskId": active.get("rootTaskId") or active.get("taskId") or chat_id,
                        "currentAssignmentId": active.get("assignmentId") or active.get("workId"),
                    }
                )
                if _trim(status.get("runtimeStatus")).lower() in {"", "idle"}:
                    patch["runtimeStatus"] = "running"
            write_local_status(
                self.settings,
                patch,
            )
        except Exception:
            pass

    async def get_chat_info(self, chat_id: str) -> Dict[str, Any]:
        return {"name": f"Redis Team task {chat_id}", "type": "dm"}

    async def _report_monitor_without_model(self, envelope: dict[str, Any]) -> bool:
        if not _is_monitor_envelope(envelope) or not self._redis:
            return False
        root_task_id, assignment_id = _assignment_identity(envelope)
        if not root_task_id or not assignment_id:
            return False

        identity = (root_task_id, assignment_id)
        accepted = identity in self._accepted_messages.values() or self._has_active_assignment(identity)
        status = read_team_statuses(self.settings, self.settings.member_id) or {}
        status_task_id = _trim(status.get("currentTaskId"))
        status_assignment_id = _trim(status.get("currentAssignmentId"))
        runtime_status = _trim(status.get("runtimeStatus")).lower()
        terminal = (
            status_task_id == root_task_id
            and (not status_assignment_id or status_assignment_id == assignment_id)
            and runtime_status in {"succeeded", "failed", "cancelled"}
        )
        if not accepted and not terminal:
            return False
        # Non-terminal Monitor envelopes must reach the model so it can continue,
        # report an exact blocker, or submit a ready result. Mechanical replies are
        # safe only after the assignment is already terminal.
        if not terminal:
            return False

        metadata = envelope.get("metadata") if isinstance(envelope.get("metadata"), dict) else {}
        check_id = _trim(metadata.get("checkId") or metadata.get("check_id") or envelope.get("messageId"))
        progress_status = runtime_status if terminal else "running"
        availability = (
            "idle"
            if progress_status == "succeeded"
            else "blocked"
            if progress_status in {"failed", "cancelled"}
            else "busy"
        )
        summary = _trim(status.get("lastSummary"))
        if not summary:
            summary = (
                f"Hermes Team assignment {progress_status}"
                if terminal
                else "Hermes Team assignment is actively processing"
            )
        await xadd_json(
            self._redis,
            events_key(self.settings),
            _task_event(
                self.settings,
                "task_progress",
                envelope,
                {
                    "eventKind": "assignment_check_result",
                    "intent": "assignment_status_check",
                    "status": progress_status,
                    "runtimeStatus": progress_status,
                    "availability": availability,
                    "progress": 100 if terminal and progress_status == "succeeded" else status.get("progress"),
                    "summary": summary,
                    "artifactRefs": status.get("artifactRefs") if isinstance(status.get("artifactRefs"), list) else [],
                    "checkId": check_id,
                    "checkSequence": metadata.get("checkSequence") or metadata.get("check_sequence"),
                    "requestedAt": metadata.get("requestedAt") or metadata.get("requested_at"),
                    "respondedAt": _now_iso(),
                    "requiresCompletion": False,
                    "terminalEvidence": terminal,
                    "nonAuthoritative": True,
                    "rootTaskTerminal": False,
                    "visibleToChat": False,
                    "chatPolicy": "hidden",
                    # A monitor reply is evidence only. It must never close or
                    # otherwise mutate the assignment on its own.
                    "stateEffect": "none",
                },
            ),
        )
        logger.info(
            "Redis Team: answered monitor without model task=%s assignment=%s status=%s",
            root_task_id,
            assignment_id,
            progress_status,
        )
        return True

    async def on_processing_complete(self, event: MessageEvent, outcome: ProcessingOutcome) -> None:
        envelope = event.raw_message if isinstance(event.raw_message, dict) else {}
        task_id = str(envelope.get("taskId") or event.source.chat_id)
        try:
            await self._on_processing_complete_inner(event, outcome)
        finally:
            message_id = _trim(envelope.get("messageId") or event.message_id)
            if message_id:
                self._accepted_messages.pop(message_id, None)
                self._turn_responses.pop(message_id, None)
            identity = _assignment_identity(envelope)
            if all(identity):
                self._retire_terminal_assignment(identity)
            for tracked_identity in list(self._active_assignments):
                self._retire_terminal_assignment(tracked_identity)
            self._redis_reply_metadata.pop(task_id, None)

    async def _on_processing_complete_inner(self, event: MessageEvent, outcome: ProcessingOutcome) -> None:
        envelope = event.raw_message if isinstance(event.raw_message, dict) else _load_active_envelope(self.settings)
        task_id = str(envelope.get("taskId") or event.source.chat_id)
        message_id = str(envelope.get("messageId") or event.message_id or "")
        turn_observation = _finish_turn_observation(self.settings, message_id)
        if outcome == ProcessingOutcome.SUCCESS:
            active = _load_active_envelope(self.settings)
            response = _trim(self._turn_responses.get(message_id) or active.get("lastAssistantResponse"))
            if not _truthy(envelope.get("requiresCompletion"), True):
                observation = _observe_team_turn_outcome(envelope, active, turn_observation)
                if self._redis:
                    await xadd_json(
                        self._redis,
                        events_key(self.settings),
                        _task_event(
                            self.settings,
                            "turn_finished_without_completion",
                            envelope,
                            {
                                "messageId": message_id,
                                "eventKind": "turn_finished_without_completion",
                                "summary": "Non-terminal Team turn finished",
                                "activeTurnFinished": True,
                                "nonAuthoritative": True,
                                "rootTaskTerminal": False,
                                "stateEffect": "none",
                                "visibleToChat": False,
                                "chatPolicy": "hidden",
                                "turnObservationOutcome": observation["outcome"],
                                "contextOnlyTurn": True,
                                "observationActionExpected": observation["actionExpected"],
                                "immediateRecoveryEligible": observation["immediateRecoveryEligible"],
                                "observationConflict": observation["evidenceConflict"] or None,
                                "observationConflicts": observation["evidenceConflicts"] or None,
                                "observationPolicyReason": observation["policyReason"],
                                "hadOutboundAssignment": observation["hadOutboundAssignment"],
                                "downstreamAssignmentStarted": observation["downstreamAssignmentStarted"],
                                "outboundDeliveryKind": observation["outboundDeliveryKind"],
                                "outboundTarget": observation["outboundTarget"],
                                "lastToolFailed": observation["lastTool"].get("failed") is True or None,
                                "lastToolName": _trim(observation["lastTool"].get("toolName")) or None,
                                "lastToolError": _trim(observation["lastTool"].get("error")) or None,
                                "lastToolCode": _trim(observation["lastTool"].get("code")) or None,
                                "targetCandidates": observation["lastTool"].get("candidates") or None,
                                "retryable": observation["outcome"] in {"retryable_tool_gap", "completion_receipt_gap"} or None,
                            },
                        ),
                    )
                active_identity = _assignment_identity(active)
                if _is_formal_assignment(active) and self._has_active_assignment(active_identity):
                    write_local_status(
                        self.settings,
                        {
                            # A context/notification turn may finish while a
                            # formal Assignment remains active. Do not let the
                            # auxiliary turn make that Assignment look idle.
                            "lastSummary": _short_text(response) or "Non-terminal Team turn finished",
                        },
                    )
                else:
                    write_local_status(
                        self.settings,
                        {
                            "availability": "idle",
                            "runtimeStatus": "idle",
                            "currentTaskId": task_id,
                            "lastSummary": _short_text(response) or "Non-terminal Team turn finished",
                        },
                    )
            elif active.get("terminal"):
                terminal_status = _trim(active.get("terminalStatus")) or "succeeded"
                current_status = read_team_statuses(self.settings, self.settings.member_id) or {}
                write_local_status(
                    self.settings,
                    {
                        "availability": "idle" if terminal_status == "succeeded" else "blocked",
                        "runtimeStatus": terminal_status,
                        "currentTaskId": task_id,
                        "progress": 100,
                        "lastSummary": _trim(current_status.get("lastSummary"))
                        or _short_text(response)
                        or f"Completion {terminal_status}",
                    },
                )
            elif _trim(active.get("completionDecision")).lower() == "rejected":
                write_local_status(
                    self.settings,
                    {
                        "availability": "busy",
                        "runtimeStatus": "running",
                        "currentTaskId": task_id,
                        "progress": 99,
                        "lastSummary": _short_text(response) or "Completion was rejected",
                    },
                )
            elif active.get("explicitCompletionSubmitted"):
                write_local_status(
                    self.settings,
                    {
                        "availability": "busy",
                        "runtimeStatus": "completion_pending",
                        "currentTaskId": task_id,
                        "progress": 99,
                        "lastSummary": _short_text(response) or "Completion submitted",
                    },
                )
            else:
                observation = _observe_team_turn_outcome(envelope, active, turn_observation)
                write_local_status(
                    self.settings,
                    {
                        "availability": "busy",
                        "runtimeStatus": "awaiting_completion_receipt",
                        "currentTaskId": task_id,
                        "lastSummary": _short_text(response) or "Turn finished without an explicit completion receipt",
                    },
                )
                if self._redis:
                    await xadd_json(
                        self._redis,
                        events_key(self.settings),
                        _task_event(
                            self.settings,
                            "turn_finished_without_completion",
                            envelope,
                            {
                                "messageId": message_id,
                                "eventKind": "turn_finished_without_completion",
                                "summary": "Turn finished without an explicit completion receipt",
                                "status": "running",
                                "availability": "busy",
                                "runtimeStatus": "awaiting_completion_receipt",
                                "stateEffect": "none",
                                "nonAuthoritative": True,
                                "rootTaskTerminal": False,
                                "activeTurnFinished": True,
                                "turnObservationOutcome": observation["outcome"],
                                "observationActionExpected": observation["actionExpected"],
                                "immediateRecoveryEligible": observation["immediateRecoveryEligible"],
                                "observationConflict": observation["evidenceConflict"] or None,
                                "observationConflicts": observation["evidenceConflicts"] or None,
                                "observationPolicyReason": observation["policyReason"],
                                "hadOutboundAssignment": observation["hadOutboundAssignment"],
                                "downstreamAssignmentStarted": observation["downstreamAssignmentStarted"],
                                "outboundDeliveryKind": observation["outboundDeliveryKind"],
                                "outboundTarget": observation["outboundTarget"],
                                "lastToolFailed": observation["lastTool"].get("failed") is True or None,
                                "lastToolName": _trim(observation["lastTool"].get("toolName")) or None,
                                "lastToolError": _trim(observation["lastTool"].get("error")) or None,
                                "lastToolCode": _trim(observation["lastTool"].get("code")) or None,
                                "targetCandidates": observation["lastTool"].get("candidates") or None,
                                "retryable": observation["outcome"] in {"retryable_tool_gap", "completion_receipt_gap"} or None,
                                "resultMarkdown": response or None,
                                "visibleToChat": False,
                                "chatPolicy": "hidden",
                            },
                        ),
                    )
        else:
            status = "cancelled" if outcome == ProcessingOutcome.CANCELLED else "failed"
            summary = f"Redis Team task attempt {status}"
            write_local_status(
                self.settings,
                {
                    "availability": "busy",
                    "runtimeStatus": "retryable_error",
                    "currentTaskId": task_id,
                    "lastSummary": summary,
                },
            )
            if self._redis:
                await xadd_json(
                    self._redis,
                    events_key(self.settings),
                    _task_event(
                        self.settings,
                        "assignment_attempt_failed",
                        envelope,
                        {
                            "messageId": message_id,
                            "taskId": task_id,
                            "summary": summary,
                            "retryable": True,
                            "stateEffect": "none",
                            "visibleToChat": False,
                            "chatPolicy": "hidden",
                        },
                    ),
                )
        self._redis_reply_metadata.pop(task_id, None)

    async def _presence_loop(self) -> None:
        assert self._redis is not None
        while self.is_connected:
            try:
                for identity in list(self._active_assignments):
                    self._retire_terminal_assignment(identity)
                status = write_local_status(self.settings)
                if self._active_assignments:
                    runtime_status = _trim(status.get("runtimeStatus")).lower()
                    availability = _trim(status.get("availability")).lower()
                    if runtime_status not in {"succeeded", "failed", "cancelled"} and availability == "idle":
                        latest = next(reversed(self._active_assignments.values()))
                        latest_envelope = latest.get("envelope") or {}
                        patch: dict[str, Any] = {
                            "availability": "busy",
                            "currentTaskId": latest_envelope.get("rootTaskId") or latest_envelope.get("taskId"),
                            "currentAssignmentId": latest_envelope.get("assignmentId") or latest_envelope.get("workId"),
                        }
                        if runtime_status in {"", "idle"}:
                            patch["runtimeStatus"] = "running"
                        status = write_local_status(self.settings, patch)
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                logger.warning("Redis Team: local presence update failed: %s", exc)
                await asyncio.sleep(STATUS_INTERVAL_SECONDS)
                continue

            redis = self._redis
            if redis is None:
                await asyncio.sleep(STATUS_INTERVAL_SECONDS)
                continue
            try:
                await redis.command(
                    "HSET",
                    presence_key(self.settings),
                    self.settings.member_id,
                    json.dumps(status, ensure_ascii=False),
                )
                for identity, tracked in list(self._active_assignments.items()):
                    if self._retire_terminal_assignment(identity):
                        continue
                    root_task_id, assignment_id = identity
                    active = tracked.get("envelope") or {}
                    observation = _read_json(_turn_observation_path(self.settings)) or {}
                    observation_matches = bool(
                        isinstance(observation, dict)
                        and _trim(observation.get("rootTaskId")) == root_task_id
                        and _trim(observation.get("assignmentId")) == assignment_id
                    )
                    turn_state = _trim(observation.get("turnState")) if observation_matches else "finished"
                    if turn_state not in {"starting", "running", "waiting_tool"}:
                        turn_state = "finished"
                    activity = {
                        "schemaVersion": 2,
                        "teamId": self.settings.team_id,
                        "memberId": self.settings.member_id,
                        "rootTaskId": root_task_id,
                        "assignmentId": assignment_id,
                        "workId": assignment_id,
                        "revision": max(1, int(active.get("revision") or 1)),
                        "state": "running" if observation_matches else "awaiting_completion_receipt",
                        "turnId": _trim(observation.get("turnId")) if observation_matches else "",
                        "turnState": turn_state,
                        "startedAt": _trim(observation.get("startedAt")) if observation_matches else "",
                        "observedAt": _now_iso(),
                        "runtime": "hermes",
                        "executionAlive": observation_matches and turn_state != "finished",
                        "lastActivityKind": _trim(observation.get("lastActivityKind")) if observation_matches else "turn_finished",
                        "pendingToolName": _trim(observation.get("pendingToolName")) if observation_matches else "",
                        "lastToolName": _trim(observation.get("lastToolName")) if observation_matches else "",
                        "lastToolFailed": observation.get("lastToolFailed") is True if observation_matches else False,
                        "lastToolAt": _trim(observation.get("lastToolAt")) if observation_matches else "",
                        "lastSessionEventAt": (
                            _trim(observation.get("lastApiActivityAt") or observation.get("observedAt"))
                            if observation_matches
                            else ""
                        ),
                        "sessionCursor": (
                            f"{_trim(observation.get('turnId'))}:{int(observation.get('apiCallCount') or 0)}:"
                            f"{int(observation.get('toolCallCount') or 0)}"
                            if observation_matches
                            else ""
                        ),
                        "apiCallCount": int(observation.get("apiCallCount") or 0) if observation_matches else 0,
                        "toolCallCount": int(observation.get("toolCallCount") or 0) if observation_matches else 0,
                    }
                    await redis.command(
                        "SET",
                        assignment_activity_key(self.settings, root_task_id, assignment_id),
                        json.dumps(activity, ensure_ascii=False),
                        "EX",
                        120,
                    )
                    await xadd_json(
                        redis,
                        events_key(self.settings),
                        _task_event(
                            self.settings,
                            "assignment_heartbeat",
                            active,
                            {
                                "summary": "Hermes Team assignment is active",
                                "stateEffect": "none",
                                "visibleToChat": False,
                                "chatPolicy": "hidden",
                            },
                        ),
                    )
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                logger.warning("Redis Team: presence connection failed: %s", exc)
                await self._reconnect_redis_clients(redis)
            await asyncio.sleep(STATUS_INTERVAL_SECONDS)

    async def _consumer_loop(self) -> None:
        assert self._consumer_redis is not None
        redis = self._consumer_redis
        read_id = "0"
        pending_batches = 0
        while self.is_connected:
            try:
                command = [
                    "XREADGROUP",
                    "GROUP",
                    self.settings.consumer_group,
                    self.settings.member_id,
                    "COUNT",
                    10,
                ]
                if read_id == ">":
                    command.extend(["BLOCK", READ_BLOCK_MS])
                command.extend(["STREAMS", inbox_key(self.settings), read_id])
                response = await redis.command(*command)
                messages = _parse_stream_response(response)
                if read_id != ">":
                    if not messages:
                        read_id = ">"
                        logger.info("Redis Team: pending drain complete; switching to new messages")
                    else:
                        pending_batches += 1
                        if pending_batches >= PENDING_DRAIN_BATCH_LIMIT:
                            read_id = ">"
                            logger.warning(
                                "Redis Team: pending drain batch limit reached; switching to new messages"
                            )
                for raw in messages:
                    await self._handle_redis_message(raw)
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                logger.warning("Redis Team: consumer loop error: %s", exc)
                if not await self._reconnect_redis_clients(redis):
                    return
                assert self._consumer_redis is not None
                redis = self._consumer_redis
                read_id = "0"
                pending_batches = 0

    async def _handle_redis_message(self, raw: dict[str, Any]) -> None:
        assert self._redis is not None
        redis_id = raw.get("redisId")
        envelope = normalize_envelope(raw)
        invalid_reason = ""
        if not envelope:
            invalid_reason = "missing stable messageId or taskId"
        elif envelope.get("teamId") not in (None, "", self.settings.team_id):
            invalid_reason = "message teamId does not match this Team consumer"
        elif _trim(envelope.get("to")) not in {"", self.settings.member_id, "broadcast"}:
            invalid_reason = "message recipient does not match this Team member"
        if invalid_reason:
            await xadd_json(
                self._redis,
                dlq_key(self.settings),
                event_for(
                    self.settings,
                    "dlq",
                    {"redisId": redis_id, "error": invalid_reason, "message": raw},
                ),
            )
            await xadd_json(
                self._redis,
                events_key(self.settings),
                event_for(
                    self.settings,
                    "invalid_inbound_message",
                    {
                        "redisId": redis_id,
                        "summary": invalid_reason,
                        "stateEffect": "none",
                        "visibleToChat": False,
                        "chatPolicy": "hidden",
                    },
                ),
            )
            if redis_id:
                await self._redis.command("XACK", inbox_key(self.settings), self.settings.consumer_group, redis_id)
            return
        dedup_key = envelope.get("idempotencyKey") or envelope["messageId"]
        if await self._redis.command("GET", _processed_message_key(self.settings, dedup_key)):
            self._transport_accepted_messages.discard(envelope["messageId"])
            if redis_id:
                await self._redis.command("XACK", inbox_key(self.settings), self.settings.consumer_group, redis_id)
            return

        # The model/control action was already accepted and only the Redis
        # marker or ACK failed. Never dispatch it a second time; finish the
        # transport commit after the connection has recovered.
        if envelope["messageId"] in self._transport_accepted_messages:
            await self._redis.command(
                "SET",
                _processed_message_key(self.settings, dedup_key),
                _now_iso(),
                "EX",
                7 * 24 * 60 * 60,
            )
            if redis_id:
                await self._redis.command("XACK", inbox_key(self.settings), self.settings.consumer_group, redis_id)
            self._transport_accepted_messages.discard(envelope["messageId"])
            return

        try:
            await xadd_json(
                self._redis,
                events_key(self.settings),
                event_for(
                    self.settings,
                    "inbound",
                    {
                        "messageId": envelope["messageId"],
                        "taskId": envelope["taskId"],
                        "from": envelope["from"],
                    },
                ),
            )
            handled_without_dispatch = await self._try_resolve_approval_response(envelope)
            if not handled_without_dispatch:
                handled_without_dispatch = await self._report_monitor_without_model(envelope)
            if not handled_without_dispatch and self.settings.auto_run:
                formal_assignment = _is_formal_assignment(envelope)
                context_only = _is_context_only_envelope(envelope) or not formal_assignment
                if formal_assignment and await _root_task_is_terminal(self._redis, self.settings, envelope):
                    await xadd_json(
                        self._redis,
                        events_key(self.settings),
                        _task_event(
                            self.settings,
                            "late_assignment_ignored",
                            envelope,
                            {
                                "summary": "Ignored assignment for terminal root task",
                                "stateEffect": "none",
                                "nonAuthoritative": True,
                                "visibleToChat": False,
                                "chatPolicy": "hidden",
                            },
                        ),
                    )
                else:
                    if formal_assignment:
                        envelope["explicitCompletionSubmitted"] = False
                        envelope.pop("lastAssistantResponse", None)
                        _persist_active_envelope(self.settings, envelope)
                        self._track_active_assignment(envelope)
                        write_local_status(
                            self.settings,
                            {
                                "availability": "busy",
                                "runtimeStatus": "running",
                                "currentTaskId": envelope["taskId"],
                                "currentAssignmentId": envelope.get("assignmentId") or envelope.get("workId"),
                                "lastSummary": "Redis Team task received",
                            },
                        )
                        await self._emit_assignment_lifecycle(
                            "task_received",
                            envelope,
                            status="acknowledged",
                            summary="Redis Team task received",
                        )
                    await self._dispatch_envelope(envelope, context_only=context_only)
            elif not handled_without_dispatch:
                if _is_formal_assignment(envelope):
                    if await _root_task_is_terminal(self._redis, self.settings, envelope):
                        await xadd_json(
                            self._redis,
                            events_key(self.settings),
                            _task_event(
                                self.settings,
                                "late_assignment_ignored",
                                envelope,
                                {
                                    "summary": "Ignored assignment for terminal root task",
                                    "stateEffect": "none",
                                    "nonAuthoritative": True,
                                    "visibleToChat": False,
                                    "chatPolicy": "hidden",
                                },
                            ),
                        )
                    else:
                        _persist_active_envelope(self.settings, envelope)
                        self._track_active_assignment(envelope)
                        await self._emit_assignment_lifecycle(
                            "task_received",
                            envelope,
                            status="acknowledged",
                            summary="Redis Team task received",
                        )
                        write_local_status(
                            self.settings,
                            {
                                "availability": "waiting_manual",
                                "runtimeStatus": "waiting_manual",
                                "currentTaskId": envelope["taskId"],
                                "currentAssignmentId": envelope.get("assignmentId") or envelope.get("workId"),
                                "lastSummary": "Redis Team task received; autorun is disabled",
                            },
                        )
                else:
                    write_local_status(self.settings, {"lastContextAt": _now_iso()})
        except Exception as exc:
            error = str(exc)
            logger.warning("Redis Team: message processing failed: %s", error)
            retry_key = _processed_message_key(self.settings, f"retry:{dedup_key}")
            retry_count = int(await self._redis.command("INCR", retry_key))
            await self._redis.command("EXPIRE", retry_key, 60 * 60)
            if retry_count <= 3:
                await xadd_json(
                    self._redis,
                    events_key(self.settings),
                    _task_event(
                        self.settings,
                        "assignment_attempt_failed",
                        envelope,
                        {
                            "error": error,
                            "summary": "Hermes Team assignment attempt failed and remains retryable",
                            "retryable": True,
                            "retryCount": retry_count,
                            "stateEffect": "none",
                            "visibleToChat": False,
                            "chatPolicy": "hidden",
                        },
                    ),
                )
                write_local_status(
                    self.settings,
                    {
                        "availability": "busy",
                        "runtimeStatus": "retryable_error",
                        "currentTaskId": envelope["taskId"],
                        "lastSummary": error,
                    },
                )
                await asyncio.sleep(min(2**retry_count, 8))
                await self._handle_redis_message(raw)
                return
            await xadd_json(
                self._redis,
                dlq_key(self.settings),
                event_for(self.settings, "dlq", {"redisId": redis_id, "error": error, "message": raw}),
            )
            await xadd_json(
                self._redis,
                events_key(self.settings),
                _task_event(
                    self.settings,
                    "task_progress",
                    envelope,
                    {
                        "status": "running",
                        "availability": "busy",
                        "runtimeStatus": "retryable_error",
                        "summary": error,
                        "error": error,
                        "completionSource": "runtime_dispatch_error",
                        "explicitCompletion": False,
                        "eventKind": "assignment_attempt_failed",
                        "failureDomain": "runtime_adapter",
                        "retryable": True,
                        "stateEffect": "none",
                        "nonAuthoritative": True,
                        "rootTaskTerminal": False,
                        "visibleToChat": False,
                        "chatPolicy": "hidden",
                    },
                ),
            )
            write_local_status(
                self.settings,
                {
                    "availability": "busy",
                    "runtimeStatus": "retryable_error",
                    "currentTaskId": envelope["taskId"],
                    "currentAssignmentId": envelope.get("assignmentId") or envelope.get("workId"),
                    "lastSummary": error,
                },
            )
            if redis_id:
                await self._redis.command("XACK", inbox_key(self.settings), self.settings.consumer_group, redis_id)
            return

        # The business action is now accepted. Redis finalization is a
        # transport concern: let failures bubble to the consumer reconnect
        # path instead of reporting a false task failure or rerunning Hermes.
        self._transport_accepted_messages.add(envelope["messageId"])
        await self._redis.command(
            "SET",
            _processed_message_key(self.settings, dedup_key),
            _now_iso(),
            "EX",
            7 * 24 * 60 * 60,
        )
        if redis_id:
            await self._redis.command("XACK", inbox_key(self.settings), self.settings.consumer_group, redis_id)
        self._transport_accepted_messages.discard(envelope["messageId"])

    async def _try_resolve_approval_response(self, envelope: dict[str, Any]) -> bool:
        parsed = _parse_approval_command(str(envelope.get("text") or ""))
        if parsed is None:
            return False
        session_key = self._session_key_for_approval_response(envelope)
        if not session_key:
            return False

        command_name, choice, resolve_all = parsed
        try:
            from tools.approval import has_blocking_approval, resolve_gateway_approval
        except Exception as exc:
            logger.warning("Redis Team: approval resolver unavailable: %s", exc)
            return False

        if not has_blocking_approval(session_key):
            await xadd_json(
                self._redis,
                events_key(self.settings),
                event_for(
                    self.settings,
                    "approval_response",
                    {
                        "taskId": envelope["taskId"],
                        "conversationId": envelope.get("conversationId"),
                        "from": envelope.get("from"),
                        "status": "no_pending_approval",
                        "sessionKey": session_key,
                    },
                ),
            )
            return True

        count = resolve_gateway_approval(session_key, choice, resolve_all=resolve_all)
        if count:
            self._latest_approval_session_key = ""
            self._approval_session_by_key = {
                key: value for key, value in self._approval_session_by_key.items() if value != session_key
            }
        status_text = "denied" if command_name == "deny" else "approved"
        active = _load_active_envelope(self.settings)
        active_identity = _assignment_identity(active)
        formal_active = _is_formal_assignment(active) and self._has_active_assignment(active_identity)
        status_patch: dict[str, Any] = {
            "currentTaskId": envelope["taskId"],
            "lastSummary": f"Redis Team approval {status_text}: {count} command(s)",
            "approvalSessionKey": session_key,
        }
        if formal_active:
            status_patch.update(
                {
                    "availability": "busy",
                    "runtimeStatus": "running",
                    "currentTaskId": active.get("rootTaskId") or active.get("taskId"),
                    "currentAssignmentId": active.get("assignmentId") or active.get("workId"),
                }
            )
        else:
            status_patch.update({"availability": "idle", "runtimeStatus": "idle"})
        write_local_status(
            self.settings,
            status_patch,
        )
        await xadd_json(
            self._redis,
            events_key(self.settings),
            event_for(
                self.settings,
                "approval_response",
                {
                    "taskId": envelope["taskId"],
                    "conversationId": envelope.get("conversationId"),
                    "from": envelope.get("from"),
                    "status": status_text,
                    "choice": choice,
                    "resolvedCount": count,
                    "resolveAll": resolve_all,
                    "sessionKey": session_key,
                },
            ),
        )
        return True

    async def _dispatch_envelope(self, envelope: dict[str, Any], *, context_only: bool = False) -> None:
        source = SessionSource(
            platform=Platform("redis_team"),
            chat_id=str(envelope["taskId"]),
            chat_name=f"Team {self.settings.team_id}",
            chat_type="dm",
            user_id=str(envelope["from"]),
            user_name=str(envelope.get("role") or envelope["from"]),
            thread_id=str(envelope.get("conversationId") or envelope["taskId"]),
            message_id=str(envelope["messageId"]),
        )
        text = str(envelope.get("text") or "")
        if not text.strip():
            text = json.dumps(envelope, ensure_ascii=False)
        context_refs = envelope.get("contextRefs") or []
        if context_refs:
            text += "\n\nContext refs:\n" + "\n".join(f"- {ref}" for ref in context_refs)
        root_task_id = _trim(envelope.get("rootTaskId") or envelope.get("taskId"))
        assignment_id = _trim(envelope.get("assignmentId") or envelope.get("workId"))
        member_artifact_root = (
            f"/team/artifacts/{_safe_name(root_task_id)}/members/"
            f"{_safe_name(self.settings.member_id)}/{_safe_name(assignment_id)}"
            if root_task_id and assignment_id
            else ""
        )
        if not context_only:
            text += (
                "\n\nClawManager Team context:\n"
                f"- rootTaskId: {root_task_id}\n"
                f"- rootMessageId: {_trim(envelope.get('rootMessageId'))}\n"
                f"- assignmentId/workId: {assignment_id}\n"
                f"- revision: {max(1, int(envelope.get('revision') or 1))}\n"
                f"- requiresCompletion: {bool(envelope.get('requiresCompletion', True))}\n"
            )
            if member_artifact_root:
                text += f"- Current member artifact root: {member_artifact_root}\n"
            text += (
                "- The Runtime inherits these IDs for Team tools; omit optional IDs instead of inventing replacements.\n"
                "- When the assigned work is ready, call team_complete_task once with the actual result. "
                "If the call is missed, end the turn normally; ClawManager Monitor will send a separate reminder without treating prose as completion.\n"
            )
            text += f"- {_assignment_validation_guidance(self.settings, envelope)}\n"

        event = MessageEvent(
            text=text,
            message_type=MessageType.TEXT,
            source=source,
            raw_message=envelope,
            message_id=str(envelope["messageId"]),
            internal=True,
        )
        metadata = {
            "task_id": envelope["taskId"],
            "conversation_id": envelope.get("conversationId"),
            "from": envelope.get("from"),
            "to": envelope.get("to"),
            "root_task_id": envelope.get("rootTaskId"),
            "root_message_id": envelope.get("rootMessageId"),
            "work_id": envelope.get("workId"),
            "assignment_id": envelope.get("assignmentId"),
            "phase_id": envelope.get("phaseId"),
            "revision": envelope.get("revision"),
            "source_message_id": envelope.get("messageId"),
            "context_only": context_only,
        }
        _begin_turn_observation(self.settings, envelope, context_only=context_only)
        if not context_only:
            if self._redis and await _root_task_is_terminal(self._redis, self.settings, envelope):
                identity = _assignment_identity(envelope)
                self._active_assignments.pop(identity, None)
                active = _load_active_envelope(self.settings)
                if _assignment_identity(active) == identity:
                    active["terminal"] = True
                    active["terminalStatus"] = ""
                    active["completedAt"] = _now_iso()
                    active["terminalNarrativePublished"] = True
                    _persist_active_envelope(self.settings, active)
                write_local_status(
                    self.settings,
                    {
                        "availability": "idle",
                        "runtimeStatus": "idle",
                        "currentTaskId": root_task_id,
                        "currentAssignmentId": assignment_id or None,
                        "lastSummary": "Assignment ignored because the root task is already terminal",
                    },
                )
                await xadd_json(
                    self._redis,
                    events_key(self.settings),
                    _task_event(
                        self.settings,
                        "late_assignment_ignored",
                        envelope,
                        {
                            "summary": "Ignored assignment before Hermes dispatch because the root task is terminal",
                            "stateEffect": "none",
                            "nonAuthoritative": True,
                            "visibleToChat": False,
                            "chatPolicy": "hidden",
                        },
                    ),
                )
                return
            write_local_status(
                self.settings,
                {
                    "availability": "busy",
                    "runtimeStatus": "running",
                    "currentTaskId": envelope["taskId"],
                    "currentAssignmentId": assignment_id or None,
                    "lastSummary": "Redis Team task started",
                },
            )
        self._redis_reply_metadata[str(envelope["taskId"])] = metadata
        identity = _assignment_identity(envelope)
        message_id = _trim(envelope.get("messageId"))
        if not context_only and message_id and all(identity):
            self._accepted_messages[message_id] = identity
            self._track_active_assignment(envelope)
            await self._emit_assignment_lifecycle(
                "task_started",
                envelope,
                status="running",
                summary="Redis Team task started",
            )
        try:
            await self.handle_message(event)
        except Exception:
            if not context_only and message_id:
                self._accepted_messages.pop(message_id, None)
            raise

    async def _send_with_retry(
        self,
        chat_id,
        content,
        reply_to=None,
        metadata=None,
        max_retries: int = 2,
        base_delay: float = 2.0,
    ):
        merged = dict(self._redis_reply_metadata.get(str(chat_id), {}))
        if metadata:
            merged.update(metadata)
        return await super()._send_with_retry(
            chat_id,
            content,
            reply_to=reply_to,
            metadata=merged,
            max_retries=max_retries,
            base_delay=base_delay,
        )


def check_requirements() -> bool:
    return _truthy(os.getenv("CLAWMANAGER_TEAM_ENABLED"), False) and bool(
        os.getenv("CLAWMANAGER_TEAM_REDIS_URL")
        and os.getenv("CLAWMANAGER_TEAM_ID")
        and os.getenv("CLAWMANAGER_TEAM_MEMBER_ID")
    )


def validate_config(config: PlatformConfig) -> bool:
    return load_settings(config).valid


def is_connected(config: PlatformConfig) -> bool:
    return load_settings(config).valid


def _env_enablement() -> Optional[dict[str, Any]]:
    settings = load_settings(None)
    if not settings.valid:
        return None
    return {
        "enabled": settings.enabled,
        "redis_url": settings.redis_url,
        "team_id": settings.team_id,
        "member_id": settings.member_id,
        "role": settings.role,
        "shared_dir": settings.shared_dir,
        "auto_run": settings.auto_run,
        "consumer_group": settings.consumer_group,
        "embedded_timeout_seconds": settings.embedded_timeout_seconds,
        "manager_url": settings.manager_url,
        "home_channel": {
            "chat_id": settings.member_id,
            "name": f"Redis Team {settings.member_id}",
        },
    }


async def _standalone_send(
    pconfig,
    chat_id: str,
    message: str,
    *,
    thread_id: Optional[str] = None,
    media_files: Optional[List[str]] = None,
    force_document: bool = False,
) -> Dict[str, Any]:
    settings = load_settings(pconfig)
    if not settings.valid:
        return {"error": "Redis Team standalone send: CLAWMANAGER_TEAM_* env is incomplete"}
    target = chat_id or settings.member_id
    payload = {
        "v": WIRE_SCHEMA_VERSION,
        "protocolVersion": PROTOCOL_VERSION,
        "messageId": f"msg_{uuid.uuid4().hex}",
        "teamId": settings.team_id,
        "from": settings.member_id,
        "to": target,
        "taskId": thread_id or f"task_{uuid.uuid4().hex}",
        "conversationId": thread_id,
        "type": "message",
        "role": settings.role,
        "text": message,
        "artifacts": media_files or [],
        "createdAt": _now_iso(),
    }
    redis = AsyncRedisClient(settings.redis_url)
    try:
        await redis.connect()
        redis_id = await xadd_json(redis, inbox_key(settings, target), payload)
        await xadd_json(redis, events_key(settings), event_for(settings, "outbound", {"messageId": payload["messageId"], "to": target}))
    except Exception as exc:
        return {"error": f"Redis Team standalone send failed: {exc}"}
    finally:
        redis.close()
    return {"success": True, "message_id": str(redis_id)}


def register(ctx) -> None:
    # These hooks are observational and fail-open. They let the control-plane
    # Monitor distinguish an active Hermes API/tool loop from a finished turn
    # without making Runtime telemetry authoritative business state.
    ctx.register_hook("pre_api_request", _hook_pre_api_request)
    ctx.register_hook("post_api_request", _hook_post_api_request)
    ctx.register_hook("pre_tool_call", _hook_pre_tool_call)
    ctx.register_hook("post_tool_call", _hook_post_tool_call)
    artifact_path_properties = {
        "scope": {"type": "string", "enum": ["member", "team"]},
        "path": {"type": "string", "description": "Current-Team path; traversal and symlinks are rejected"},
        "rootTaskId": {"type": "string"},
        "assignmentId": {"type": "string"},
    }
    ctx.register_tool(
        name="team_artifact_write",
        toolset="redis_team",
        schema={
            "name": "team_artifact_write",
            "description": "Atomically write a UTF-8 artifact inside the current Team workspace.",
            "parameters": {
                "type": "object",
                "additionalProperties": False,
                "required": ["path", "content"],
                "properties": {**artifact_path_properties, "content": {"type": "string"}},
            },
        },
        handler=_tool_team_artifact_write,
        check_fn=check_requirements,
        requires_env=["CLAWMANAGER_TEAM_SHARED_DIR"],
        is_async=True,
        description="Write a current-Team artifact.",
    )
    ctx.register_tool(
        name="team_artifact_read",
        toolset="redis_team",
        schema={
            "name": "team_artifact_read",
            "description": "Read a UTF-8 artifact from the current Team workspace.",
            "parameters": {
                "type": "object",
                "additionalProperties": False,
                "required": ["path"],
                "properties": {
                    **artifact_path_properties,
                    "offset": {"type": "integer", "minimum": 0},
                    "maxBytes": {"type": "integer", "minimum": 1, "maximum": MAX_ARTIFACT_BYTES},
                },
            },
        },
        handler=_tool_team_artifact_read,
        check_fn=check_requirements,
        requires_env=["CLAWMANAGER_TEAM_SHARED_DIR"],
        is_async=True,
        description="Read a current-Team artifact.",
    )
    ctx.register_tool(
        name="team_artifact_list",
        toolset="redis_team",
        schema={
            "name": "team_artifact_list",
            "description": "List current-Team artifacts without following symlinks.",
            "parameters": {
                "type": "object",
                "additionalProperties": False,
                "required": ["path"],
                "properties": {**artifact_path_properties, "limit": {"type": "integer", "minimum": 1, "maximum": 200}},
            },
        },
        handler=_tool_team_artifact_list,
        check_fn=check_requirements,
        requires_env=["CLAWMANAGER_TEAM_SHARED_DIR"],
        is_async=True,
        description="List current-Team artifacts.",
    )
    ctx.register_tool(
        name="team_artifact_mkdir",
        toolset="redis_team",
        schema={
            "name": "team_artifact_mkdir",
            "description": "Create a directory inside the current Team workspace.",
            "parameters": {
                "type": "object",
                "additionalProperties": False,
                "required": ["path"],
                "properties": artifact_path_properties,
            },
        },
        handler=_tool_team_artifact_mkdir,
        check_fn=check_requirements,
        requires_env=["CLAWMANAGER_TEAM_SHARED_DIR"],
        is_async=True,
        description="Create a current-Team artifact directory.",
    )
    ctx.register_tool(
        name="team_artifact_preview",
        toolset="redis_team",
        schema={
            "name": "team_artifact_preview",
            "description": "Create a managed read-only HTTP Browser URL for a current-Team file.",
            "parameters": {
                "type": "object",
                "additionalProperties": False,
                "required": ["path"],
                "properties": artifact_path_properties,
            },
        },
        handler=_tool_team_artifact_preview,
        check_fn=check_requirements,
        requires_env=["CLAWMANAGER_TEAM_SHARED_DIR", "CLAWMANAGER_TEAM_TOKEN", "CLAWMANAGER_TEAM_PREVIEW_ORIGIN"],
        is_async=True,
        description="Open a Team artifact through the managed Browser preview.",
    )
    ctx.register_tool(
        name="team_send",
        toolset="redis_team",
        schema={
            "name": "team_send",
            "description": "Send a task or message to another ClawManager team member via Redis Streams.",
            "parameters": {
                "type": "object",
                "additionalProperties": False,
                "anyOf": [
                    {"required": ["text"]},
                    {"required": ["message"]},
                    {"required": ["prompt"]},
                ],
                "properties": {
                    "to": {"type": "string", "description": "Recipient member ID, or broadcast if supported"},
                    "recipient": {"type": "string", "description": "Compatibility alias for to"},
                    "targetMemberId": {"type": "string", "description": "Compatibility alias for to"},
                    "target_member_id": {"type": "string", "description": "Compatibility alias for to"},
                    "text": {"type": "string", "description": "Task or message text"},
                    "message": {"type": "string", "description": "Compatibility alias for text"},
                    "prompt": {"type": "string", "description": "Compatibility alias for text"},
                    "intent": {"type": "string"},
                    "taskId": {"type": "string"},
                    "rootTaskId": {"type": "string"},
                    "workId": {"type": "string"},
                    "assignmentId": {"type": "string"},
                    "phaseId": {"type": "string"},
                    "revision": {"type": "integer"},
                    "required": {"type": "boolean"},
                    "reviewRequired": {
                        "type": "boolean",
                        "description": "Set true on production-only work with downstream validation; tell the producer to hand off without self-testing",
                    },
                    "validationRequired": {"type": "boolean"},
                    "validationAssignment": {
                        "type": "boolean",
                        "description": "Marks this as test/review/evidence work; any member role may validate and several validators may run in parallel",
                    },
                    "validationTargetAssignmentId": {"type": "string"},
                    "validationTargetRevision": {"type": "integer"},
                    "reviewedAssignmentId": {"type": "string"},
                    "reviewedRevision": {"type": "integer"},
                    "dependsOn": {"type": "array", "items": {"type": "string"}},
                    "title": {"type": "string"},
                    "contextRefs": {"type": "array", "items": {"type": "string"}},
                    "ttlSeconds": {"type": "integer"},
                    "priority": {"type": "string"},
                    "metadata": {"type": "object"},
                },
            },
        },
        handler=_tool_team_send,
        check_fn=check_requirements,
        requires_env=["CLAWMANAGER_TEAM_REDIS_URL", "CLAWMANAGER_TEAM_ID", "CLAWMANAGER_TEAM_MEMBER_ID"],
        is_async=True,
        description="Send work to another Redis Team member.",
    )
    ctx.register_tool(
        name="team_status",
        toolset="redis_team",
        schema={
            "name": "team_status",
            "description": "Read Redis Team member status snapshots from the shared Team directory.",
            "parameters": {
                "type": "object",
                "additionalProperties": False,
                "properties": {"memberId": {"type": "string"}},
            },
        },
        handler=_tool_team_status,
        check_fn=check_requirements,
        requires_env=["CLAWMANAGER_TEAM_SHARED_DIR"],
        is_async=True,
        description="Read Redis Team status snapshots.",
    )
    ctx.register_tool(
        name="team_update_progress",
        toolset="redis_team",
        schema={
            "name": "team_update_progress",
            "description": "Update this Hermes member's Redis Team task progress and publish a progress event.",
            "parameters": {
                "type": "object",
                "additionalProperties": False,
                "required": ["status"],
                "properties": {
                    "taskId": {"type": "string"},
                    "status": {"type": "string"},
                    "summary": {"type": "string"},
                    "progress": {"type": "number"},
                    "artifactRefs": {"type": "array", "items": {"type": "string"}},
                    "eventKind": {"type": "string"},
                },
            },
        },
        handler=_tool_team_update_progress,
        check_fn=check_requirements,
        requires_env=["CLAWMANAGER_TEAM_REDIS_URL", "CLAWMANAGER_TEAM_ID", "CLAWMANAGER_TEAM_MEMBER_ID"],
        is_async=True,
        description="Publish Redis Team task progress.",
    )
    ctx.register_tool(
        name="team_complete_task",
        toolset="redis_team",
        schema={
            "name": "team_complete_task",
            "description": "Mark a Redis Team task succeeded, failed or cancelled, and write durable result files.",
            "parameters": {
                "type": "object",
                "additionalProperties": False,
                "required": ["status", "summary"],
                "properties": {
                    "taskId": {"type": "string"},
                    "status": {"type": "string"},
                    "summary": {"type": "string"},
                    "resultMarkdown": {"type": "string"},
                    "artifactRefs": {"type": "array", "items": {"type": "string"}},
                    "reviewVerdict": {"type": "string", "enum": ["pass", "fail"]},
                    "reviewedAssignmentId": {"type": "string"},
                    "reviewedRevision": {"type": "integer"},
                    "reviewedArtifactRefs": {"type": "array", "items": {"type": "string"}},
                },
            },
        },
        handler=_tool_team_complete_task,
        check_fn=check_requirements,
        requires_env=["CLAWMANAGER_TEAM_REDIS_URL", "CLAWMANAGER_TEAM_ID", "CLAWMANAGER_TEAM_MEMBER_ID"],
        is_async=True,
        description="Complete a Redis Team task.",
    )
    ctx.register_platform(
        name="redis_team",
        label="Redis Team",
        adapter_factory=lambda cfg: RedisTeamAdapter(cfg),
        check_fn=check_requirements,
        validate_config=validate_config,
        is_connected=is_connected,
        required_env=[
            "CLAWMANAGER_TEAM_ENABLED",
            "CLAWMANAGER_TEAM_REDIS_URL",
            "CLAWMANAGER_TEAM_ID",
            "CLAWMANAGER_TEAM_MEMBER_ID",
        ],
        env_enablement_fn=_env_enablement,
        cron_deliver_env_var="CLAWMANAGER_TEAM_MEMBER_ID",
        standalone_sender_fn=_standalone_send,
        emoji="[team]",
        pii_safe=True,
        allow_update_command=False,
        platform_hint=(
            "You are handling a ClawManager Redis Team task. Treat the incoming "
            "message as delegated Worker work. Read the task context and "
            "/team/team.json when available. Prefer team_artifact_write/read/list/mkdir "
            "for shared files and use team_artifact_preview before opening a Team file "
            "in Browser; never use file:// or start a temporary server. Validation is "
            "assignment-specific: production-only assignments produce and hand off artifacts "
            "without tests, while assignments explicitly designated by the Leader as test, "
            "review, or evidence work validate normally regardless of the member role. "
            "These instructions never block delivery and tools remain available for required "
            "implementation or validation work. When work is ready, call team_complete_task "
            "once with the actual result. A normal assistant reply is non-terminal; if the explicit "
            "receipt is missed, end the turn normally and let ClawManager Monitor issue a separate "
            "bounded reminder. Missing optional Team metadata is not a reason to stop. Never write "
            "Team tokens, API keys, or Redis credentials into Team files or logs."
        ),
    )
