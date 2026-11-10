"""Keep Lite process identity and deployment boundaries out of workspace dotenv.

Imported at the beginning of the pinned env loader, before any workspace load.
Only the Lite image installs this module. Unknown user keys keep the upstream
dotenv semantics, including interpolation and override precedence.
"""
from functools import wraps
from contextvars import ContextVar
from concurrent.futures import ThreadPoolExecutor
from itertools import islice
import os
from pathlib import Path
import threading

_PREFIXES = ("CLAWMANAGER_", "HERMES_DASHBOARD_BASIC_AUTH_", "HERMES_MANAGED_",
             "PYTHON", "LD_", "XDG_")
_KEYS = frozenset({
    "HOME", "HERMES_HOME", "HERMES_PROFILE", "HERMES_MANAGED", "HOST", "PORT", "PATH",
    "HERMES_ACCEPT_HOOKS", "NODE_OPTIONS", "NODE_PATH", "BASH_ENV", "ENV",
    "HERMES_RUNTIME_DIR", "HERMES_PYTHON", "HERMES_LITE_INTERNAL_WS_CREDENTIAL",
    "HERMES_INSTALL_ROOT", "HERMES_DISABLE_LAZY_INSTALLS",
    "OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_API_BASE", "CUSTOM_BASE_URL", "OPENAI_MODEL",
})


def protected(key):
    return key in _KEYS or key.startswith(_PREFIXES)


_INITIAL = {key: value for key, value in os.environ.items() if protected(key)}
_LOCK = threading.RLock()
_READ_REQUEST = ContextVar("lite_env_read_request", default=None)
_SKILL_READ_POOL = ThreadPoolExecutor(max_workers=8, thread_name_prefix="lite-skill-read")
_SKILL_TEXT_LIMIT = 256 * 1024
_SKILL_REQUEST_LIMIT = 8 * 1024 * 1024


def env_read_request(fn):
    """One dotenv snapshot per read-only catalog request, never across requests.

    Upstream catalog loops otherwise read the same NFS file for every provider
    and platform. No TTL/mtime cache: the next request reads the actual bytes.
    Mutating configuration routes and model execution do not use this scope.
    """
    @wraps(fn)
    def guarded(*args, **kwargs):
        token = _READ_REQUEST.set({})
        try:
            return fn(*args, **kwargs)
        finally:
            _READ_REQUEST.reset(token)
    return guarded


def catalog_value(name, loader):
    """Reuse a read-only derived value within the same catalog/profile request."""
    cache = _READ_REQUEST.get()
    if cache is None:
        return loader()
    key = ("catalog", name)
    if key not in cache:
        cache[key] = loader()
    return cache[key]


def _skill_text_key(path):
    return ("skill-text", os.path.abspath(os.fspath(path)))


def _read_small_skill(path):
    # Only file bytes are read in workers. Profile, permission and quarantine
    # decisions stay in the caller's existing scoped catalog iterator.
    try:
        with Path(path).open("rb") as stream:
            data = stream.read(_SKILL_TEXT_LIMIT + 1)
        return data if len(data) <= _SKILL_TEXT_LIMIT else None
    except OSError:
        # Let the normal parser/read path retain its existing error handling.
        return None


def prefetch_skill_files(paths):
    """Bound NFS reads to eight workers and one request's small-file snapshot.

    No persistent file cache: a subsequent request sees edits and removals.
    Large skills and reads outside a catalog keep the upstream behavior.
    """
    cache = _READ_REQUEST.get()
    if cache is None:
        yield from paths
        return
    paths = iter(paths)
    while batch := list(islice(paths, 8)):
        missing = [p for p in batch if _skill_text_key(p) not in cache]
        if cache.get("skill-text-bytes", 0) < _SKILL_REQUEST_LIMIT:
            for path, data in zip(missing, _SKILL_READ_POOL.map(_read_small_skill, missing)):
                if data is not None and cache.get("skill-text-bytes", 0) + len(data) <= _SKILL_REQUEST_LIMIT:
                    cache[_skill_text_key(path)] = data
                    cache["skill-text-bytes"] = cache.get("skill-text-bytes", 0) + len(data)
        yield from batch


def read_skill_text(path, *, encoding="utf-8-sig", errors="strict"):
    cache = _READ_REQUEST.get()
    key = _skill_text_key(path)
    if cache is not None and key in cache:
        # Match Path.read_text's universal-newline and decoding semantics.
        return cache[key].decode(encoding, errors).replace("\r\n", "\n").replace("\r", "\n")
    return path.read_text(encoding=encoding, errors=errors)


def managed_value(key, default=None):
    return _INITIAL.get(key, default)


def secret_scope(values, is_global):
    # v0.22.5 resolves profile credentials directly from files as well as env.
    # Keep user integration secrets, but seal process-owned instance credentials
    # before a scope can be used by a provider or copied to a child process.
    result = {key: value for key, value in values.items() if not protected(key)}
    result.update({key: value for key, value in _INITIAL.items() if not is_global(key)})
    return result


def secret_file(fn):
    @wraps(fn)
    def guarded(*args, **kwargs):
        cache = _READ_REQUEST.get()
        path = args[0] if args else kwargs.get("env_path")
        key = (fn, os.path.abspath(os.fspath(path))) if path is not None else None
        if cache is not None and key is not None:
            if key not in cache:
                cache[key] = dict(fn(*args, **kwargs))
            values = cache[key]
        else:
            values = fn(*args, **kwargs)
        return {key: (_INITIAL[key] if protected(key) else value)
                for key, value in values.items() if not protected(key) or key in _INITIAL}
    return guarded


def _restore():
    for key in list(os.environ):
        if protected(key) and key not in _INITIAL:
            os.environ.pop(key, None)
    os.environ.update(_INITIAL)


def preserve_environment(fn):
    @wraps(fn)
    def guarded(*args, **kwargs):
        with _LOCK:
            _restore()
            try:
                return fn(*args, **kwargs)
            finally:
                # Secret-provider/config reload paths run inside this loader.
                _restore()
    return guarded


def load_dotenv(*, dotenv_path=None, stream=None, override=False, encoding="utf-8"):
    # Use the same pinned parser, but filter before assignment: restoring only
    # after load_dotenv would expose transient attacker values to other threads.
    from dotenv.main import DotEnv
    with _LOCK:
        _restore()
        values = DotEnv(dotenv_path=dotenv_path, stream=stream, verbose=False,
                        encoding=encoding, interpolate=True, override=override).dict()
        for key, value in values.items():
            if protected(key) or value is None or (not override and key in os.environ):
                continue
            os.environ[key] = value
        return bool(values)
