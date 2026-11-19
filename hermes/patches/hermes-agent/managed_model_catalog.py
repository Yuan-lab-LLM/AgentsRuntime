"""Resolve managed Hermes session references without rewriting session history.

The Gateway remains the authority for every inference request. This adapter
only repairs native provider lookup; it never grants access or falls back to
another model. Enabled by a versioned control-plane environment contract.
"""
import json
import contextlib
import threading
import logging
import signal
from dataclasses import dataclass
import os
import urllib.parse
import urllib.request
import urllib.error

PROTOCOL = "CLAWMANAGER_MODEL_REFERENCE_PROTOCOL"
MAX_BYTES = 1024 * 1024
_force_stop_pending = threading.Event()
_force_stop_lock = threading.Lock()


def _terminate_runtime():
    # Pro's launcher creates a private session. Verify BOTH session and group;
    # never signal a guessed parent group or the Webtop session.
    raw = os.environ.get("CLAWMANAGER_HERMES_FAIL_STOP_PGID", "")
    if (hasattr(os, "killpg") and hasattr(os, "getsid") and hasattr(os, "getpgrp")
            and raw.isascii() and raw.isdigit() and len(raw) <= 10):
        pgid = int(raw)
        if pgid > 1 and os.getpgrp() == pgid and os.getsid(0) == pgid:
            try:
                os.killpg(pgid, getattr(signal, "SIGKILL", 9))
            except OSError:
                logging.getLogger(__name__).error("ClawManager private group stop failed; exiting Agent without claiming cleanup")
    # Lite's supervisor owns group cleanup after this explicit exit.
    os._exit(78)


def _arm_force_stop():
    if os.environ.get("CLAWMANAGER_HERMES_FORCE_STOP_ON_REVOKE") != "1":
        return
    if os.environ.get("CLAWMANAGER_TEAM_ENABLED", "").strip().lower() in {"1", "true", "yes", "on", "enabled"}:
        return
    with _force_stop_lock:
        if _force_stop_pending.is_set():
            return
        grace = 10
        raw = os.environ.get("CLAWMANAGER_HERMES_CANCEL_GRACE_SECONDS", "")
        if raw.isascii() and raw.isdigit() and len(raw) <= 2:
            grace = max(1, min(60, int(raw)))
        # Latch admission closed before the timer starts. Native return or
        # revoke/regrant must not cancel an unproven-cleanup fallback.
        _force_stop_pending.set()

        def terminate():
            threading.Event().wait(grace)
            _terminate_runtime()

        try:
            threading.Thread(target=terminate, name="clawmanager-revocation-fail-stop", daemon=True).start()
        except Exception:
            # Keep admission closed, but do not prevent cooperative interrupt.
            logging.getLogger(__name__).error("ClawManager fail-stop watchdog failed to start; instance admission remains closed")


@dataclass(frozen=True, repr=False)
class _BoundTask:
    base: str
    key: str
    session: str
    task: str
    generation: str


@contextlib.contextmanager
def monitor_turn(agent, task_id):
    """Monitor the exact native turn; join our watcher, never claim tool cleanup."""
    refresh_managed_context(agent)
    headers = task_headers(agent, task_id)
    if not headers:
        yield
        return
    # Native fallback may mutate agent credentials/model/session while a turn
    # unwinds. Checks and finish must stay bound to the original owner scope.
    scope = _BoundTask(os.environ["CLAWMANAGER_LLM_BASE_URL"].strip().rstrip("/"),
        os.environ["CLAWMANAGER_LLM_API_KEY"].strip(),
        headers["X-ClawManager-Session-ID"], task_id,
        headers["X-ClawManager-Runtime-Generation"])
    check_task(scope, task_id)  # Fail before any turn work on uncertain identity.
    stop = threading.Event()
    uncertain = threading.Event()
    fence = threading.Lock()

    def watch():
        while not stop.wait(1.0):
            try:
                check_task(scope, task_id)
            except Exception as error:
                uncertain.set()
                # Serialize callback admission with exit. Never interrupt a
                # later turn reusing this agent after this context has closed.
                with fence:
                    if not stop.is_set():
                        if isinstance(error, ManagedTaskRevoked):
                            _arm_force_stop()
                        try:
                            agent.interrupt("当前模型任务不可用；已请求停止本轮执行",
                                hard_cancel=True, tool_reason="model access unavailable")
                        except Exception:
                            pass
                        stop.set()
                return

    watcher = threading.Thread(target=watch, name="clawmanager-model-access", daemon=True)
    watcher.start()
    completed = False
    try:
        yield
        completed = True
    finally:
        with fence:
            stop.set()
        # HTTP checks have bounded timeouts. Do not detach a watcher capable
        # of mutating the cached native agent and poisoning a subsequent turn.
        watcher.join()
    if completed and not uncertain.is_set():
        # Ordinary return is NOT a cancellation acknowledgement. Recheck after
        # joining to catch revocation racing with the last successful poll.
        try:
            check_task(scope, task_id)
        except ManagedTaskRevoked:
            _arm_force_stop()
            raise
        finish_task(scope, task_id)


def task_headers(agent, task_id):
    """Opt-in exact native turn identity; never enroll or acknowledge cleanup."""
    if os.environ.get("CLAWMANAGER_HERMES_TASK_IDENTITY_PROTOCOL") != "1":
        return {}
    if os.environ.get("CLAWMANAGER_TEAM_ENABLED", "").strip().lower() in {"1", "true", "yes", "on", "enabled"}:
        return {}
    base = os.environ.get("CLAWMANAGER_LLM_BASE_URL", "").strip().rstrip("/")
    key = os.environ.get("CLAWMANAGER_LLM_API_KEY", "").strip()
    if not base or not key or str(getattr(agent, "base_url", "") or "").strip().rstrip("/") != base or str(getattr(agent, "api_key", "") or "").strip() != key:
        return {}
    if _force_stop_pending.is_set():
        raise ManagedCatalogUnavailable("实例正在因模型撤权停止，请等待重启")
    session = str(getattr(agent, "session_id", "") or "")
    generation = os.environ.get("CLAWMANAGER_GATEWAY_GENERATION", "")
    if not session.strip() or not isinstance(task_id, str) or not task_id.strip() or not generation.isascii() or not generation.isdigit():
        raise ManagedCatalogUnavailable("模型任务身份不可用；未回退到旧协议")
    if len(session) > 256 or len(task_id) > 256 or int(generation) > 2147483647 or any(ord(c) < 32 or ord(c) == 127 for c in session + task_id):
        raise ManagedCatalogUnavailable("模型任务身份不可用；未回退到旧协议")
    return {"X-ClawManager-Session-ID": session, "X-ClawManager-Task-ID": task_id,
            "X-ClawManager-Runtime-Generation": generation}


def apply_task_headers(agent, task_id, kwargs):
    headers = task_headers(agent, task_id)
    if not headers:
        return kwargs
    result = dict(kwargs)
    # Discard case variants supplied by extensions; exact native identity wins.
    result["extra_headers"] = {k: v for k, v in (kwargs.get("extra_headers") or {}).items()
                               if k.lower() not in {h.lower() for h in headers}}
    result["extra_headers"].update(headers)
    return result


def _task_request(agent, task_id, action):
    if isinstance(agent, _BoundTask):
        if task_id != agent.task:
            raise ManagedCatalogUnavailable("模型任务身份不匹配")
        base, key = agent.base, agent.key
        headers = {"X-ClawManager-Session-ID": agent.session,
                   "X-ClawManager-Runtime-Generation": agent.generation}
    else:
        headers = task_headers(agent, task_id)
        if not headers:
            return
        base = os.environ["CLAWMANAGER_LLM_BASE_URL"].strip().rstrip("/")
        key = os.environ["CLAWMANAGER_LLM_API_KEY"].strip()
    parsed = urllib.parse.urlsplit(base)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
        raise ManagedCatalogUnavailable("模型任务状态不可用")
    payload = {"session_id": headers["X-ClawManager-Session-ID"],
               "task_id": task_id, "generation": int(headers["X-ClawManager-Runtime-Generation"])}
    if action == "finish":
        payload["cancelled"] = False  # No cleanup proof: never acknowledge cancellation.
    body = json.dumps(payload).encode()
    request = urllib.request.Request(base + "/runtime/tasks/" + action, data=body,
        headers={"Authorization": "Bearer " + key, "Content-Type": "application/json"}, method="POST")
    try:
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), _NoRedirect())
        with opener.open(request, timeout=2.5) as response:
            if response.status != 200:
                raise ValueError("task refused")
    except urllib.error.HTTPError as error:
        confirmed = action == "check" and error.code == 403 and error.headers is not None and error.headers.get("X-ClawManager-Task-State") == "revoked"
        error.close()
        if confirmed:
            raise ManagedTaskRevoked("当前模型不可用：本轮模型权限已撤销") from None
        raise ManagedCatalogUnavailable("模型任务状态暂不可用") from None
    except Exception:
        raise ManagedCatalogUnavailable("当前模型任务不可用：已撤权或任务状态暂不可用") from None


def check_task(agent, task_id):
    return _task_request(agent, task_id, "check")


def refresh_managed_context(agent):
    """Refresh native compression budgets before the managed turn starts."""
    if os.environ.get(PROTOCOL) != "1":
        return
    if os.environ.get("CLAWMANAGER_TEAM_ENABLED", "").strip().lower() in {"1", "true", "yes", "on", "enabled"}:
        return
    base = os.environ.get("CLAWMANAGER_LLM_BASE_URL", "").strip().rstrip("/")
    key = os.environ.get("CLAWMANAGER_LLM_API_KEY", "").strip()
    if not base or not key or str(getattr(agent, "base_url", "") or "").strip().rstrip("/") != base or str(getattr(agent, "api_key", "") or "").strip() != key:
        return
    model = _name(getattr(agent, "model", None))
    matches = [item for item in load_catalog(base, key) if model and model in _names(item)]
    if len(matches) != 1:
        raise ManagedModelUnavailable("当前模型不可用：无法唯一确认托管模型上下文")
    window = matches[0].get("context_window", 0)
    if type(window) is not int or not 0 <= window <= 2147483647:
        raise ManagedCatalogUnavailable("托管模型上下文配置无效")
    if window == 0:
        return  # Unknown: preserve the native/runtime default.
    engine = getattr(agent, "context_compressor", None)
    if engine is not None:
        # Use the pinned native setter: it invalidates derived threshold,
        # tail and summary budgets without erasing conversation history.
        engine.context_length = window
    agent._config_context_length = window


def finish_task(agent, task_id):
    """Only ordinary task completion; cancellation receipts are unsupported."""
    return _task_request(agent, task_id, "finish")


def managed_tool_block(agent, task_id=None):
    """Fence native tool dispatch; this is NOT an in-flight cancellation receipt."""
    if os.environ.get(PROTOCOL) != "1":
        return None
    # Team execution is outside this single-instance change.
    if os.environ.get("CLAWMANAGER_TEAM_ENABLED", "").strip().lower() in {"1", "true", "yes", "on", "enabled"}:
        return None
    base = os.environ.get("CLAWMANAGER_LLM_BASE_URL", "").strip().rstrip("/")
    key = os.environ.get("CLAWMANAGER_LLM_API_KEY", "").strip()
    if (not base or not key or str(getattr(agent, "base_url", "") or "").strip().rstrip("/") != base
            or str(getattr(agent, "api_key", "") or "").strip() != key):
        return None  # Do not interrupt a user's independent provider.
    try:
        check_task(agent, task_id)
        model = _name(getattr(agent, "model", None))
        matches = [item for item in load_catalog(base, key) if model and model in _names(item)]
        if len(matches) == 1:
            return None
        message = "当前模型不可用：模型已撤权、删除或历史引用不唯一；本轮后续工具已阻止"
    except ManagedCatalogUnavailable:
        message = "模型授权目录暂不可用；为安全起见已阻止本轮后续工具，请稍后重试"
    # Native hard_cancel signals active workers too, but completion/join must
    # be verified separately. Do not claim Auto's resolved model was checked.
    try:
        agent.interrupt(message, hard_cancel=True, tool_reason="model access unavailable")
    except Exception:
        pass
    return message


class ManagedModelUnavailable(ValueError):
    pass


class ManagedCatalogUnavailable(ValueError):
    pass


class ManagedTaskRevoked(ManagedCatalogUnavailable):
    pass


def _name(value):
    return str(value or "").strip().lower()


def _provider(value):
    value = _name(value)
    return value[7:] if value.startswith("custom:") else value


def _names(item):
    current = [] if item.get("legacy_name_unavailable") else [item["id"]]
    return {_name(v) for v in [*current, item.get("stable_id"), *item.get("aliases", [])] if v}


def _provider_matches(requested, original):
    # These are the exact transforms used by Hermes and our config generator.
    original = _name(original)
    variants = {original, "clawmanager-" + original}
    variants |= {v.replace(" ", "-") for v in variants}
    return requested in variants


class _NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def load_catalog(base_url, api_key):
    """No retry/cache/redirect: failures must not resurrect revoked grants."""
    try:
        parsed = urllib.parse.urlsplit(base_url)
        if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username or parsed.password or parsed.query or parsed.fragment:
            raise ValueError("invalid endpoint")
        request = urllib.request.Request(base_url.rstrip("/") + "/runtime/catalog",
                                         headers={"Authorization": "Bearer " + api_key, "Accept": "application/json"})
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), _NoRedirect())
        with opener.open(request, timeout=2.5) as response:
            raw = response.read(MAX_BYTES + 1)
        if len(raw) > MAX_BYTES:
            raise ValueError("oversized catalog")
        payload = json.loads(raw)
        items = payload["models"]
        if not isinstance(items, list):
            raise ValueError("invalid catalog")
        for item in items:
            if not isinstance(item, dict) or not isinstance(item.get("id"), str) or not item["id"].strip():
                raise ValueError("invalid model")
            if item["id"] != "auto" and not isinstance(item.get("stable_id"), str):
                raise ValueError("missing identity protocol")
            if item["id"] != "auto" and not item["stable_id"].strip():
                raise ValueError("empty identity")
            if not isinstance(item.get("legacy_name_unavailable", False), bool):
                raise ValueError("invalid historical name status")
            if not isinstance(item.get("aliases", []), list) or not all(isinstance(v, str) for v in item.get("aliases", [])):
                raise ValueError("invalid aliases")
            if not isinstance(item.get("provider_refs", []), list):
                raise ValueError("invalid provider refs")
            for ref in item.get("provider_refs", []):
                if not isinstance(ref, dict) or not all(isinstance(ref.get(k), str) and ref[k].strip() for k in ("provider_id", "model_id")):
                    raise ValueError("invalid provider ref")
        return items
    except Exception:
        # Never expose the request URL, credentials or upstream response body.
        raise ManagedCatalogUnavailable("模型授权目录暂不可用，请稍后重试；未切换其他模型") from None


def resolve_managed_runtime(*, requested_provider, target_model, explicit_base_url,
                            explicit_api_key, lookup_custom, resolve_builtin):
    if os.environ.get(PROTOCOL) != "1" or not target_model:
        return None
    base = os.environ.get("CLAWMANAGER_LLM_BASE_URL", "").strip().rstrip("/")
    key = os.environ.get("CLAWMANAGER_LLM_API_KEY", "").strip()
    if not base or not key:
        raise ManagedCatalogUnavailable("模型授权目录暂不可用：缺少实例 Gateway 配置")
    # Explicit foreign connections are not managed by this adapter.
    if explicit_base_url and explicit_base_url.strip().rstrip("/") != base:
        return None
    if explicit_api_key and explicit_api_key.strip() != key:
        return None
    provider = _provider(requested_provider)
    custom = lookup_custom(requested_provider)
    owned = bool(custom and str(custom.get("base_url") or "").strip().rstrip("/") == base
                 and str(custom.get("api_key") or "").strip() == key)
    if custom and not owned:
        return None
    if not owned and provider not in {"clawmanager"} and not provider.startswith("clawmanager-"):
        try:
            builtin = resolve_builtin(requested_provider)
        except Exception:
            builtin = None
        if builtin:
            return None
    items = load_catalog(base, key)
    model = _name(target_model)
    candidates = []
    for item in items:
        names = _names(item)
        bare = model in names
        exact = any(_provider_matches(provider, ref["provider_id"]) and model == _name(ref["model_id"])
                    for ref in item.get("provider_refs", []))
        if (bare and (owned or provider == "clawmanager")) or exact:
            candidates.append(item)
    if not candidates and not owned and provider != "clawmanager" and not provider.startswith("clawmanager-"):
        # Unknown non-managed provider: native Hermes reports/configures it.
        return None
    if len(candidates) != 1:
        raise ManagedModelUnavailable("当前模型不可用：模型已撤权、删除或历史引用不唯一")
    selected = candidates[0]
    # Native callers keep target_model rather than runtime['model']. Require
    # that this unchanged wire name resolves uniquely to the SAME deployment.
    # Never strip a provider and guess its leaf, or substitute Auto.
    wire_matches = [item for item in items if model in _names(item)]
    if len(wire_matches) != 1 or wire_matches[0] is not selected:
        raise ManagedModelUnavailable("当前模型不可用：历史模型引用无法安全恢复")
    result = {"provider": "custom", "api_mode": "chat_completions", "base_url": base,
            "api_key": key, "requested_provider": requested_provider,
            "source": "clawmanager-runtime-catalog-v1"}
    if custom:
        # Keep existing provider limits/protocol/features; only stale credential
        # pools and fallback deployment selection are intentionally bypassed.
        for field in ("api_mode", "max_output_tokens", "capabilities", "extra_headers"):
            if field in custom:
                result[field] = custom[field]
        if isinstance(custom.get("extra_body"), dict) and custom["extra_body"]:
            result["request_overrides"] = {"extra_body": dict(custom["extra_body"])}
    return result
