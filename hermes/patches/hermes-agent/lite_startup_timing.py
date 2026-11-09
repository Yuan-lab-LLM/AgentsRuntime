"""Bounded numeric timings for the managed serve pre-HTTP path."""
from contextlib import contextmanager
from functools import wraps
import os
import sys
import time

PHASES = frozenset({
    'cli_import', 'cli_dispatch', 'prepare_runtime', 'web_deps', 'skills_sync',
    'terminal_config', 'plugins_load', 'web_import', 'server_setup',
    'gateway_warm', 'auth_warm', 'copilot_warm', 'provider_warm', 'skin_warm',
    'inventory_warm', 'model_switch_warm', 'boot_fingerprint', 'hosted_import',
    'lifespan_prepare', 'http_bind', 'skills_optional', 'skills_external',
})
clock = time.monotonic
_enabled = os.environ.get('CLAWMANAGER_HERMES_DESKTOP_WEB_ENABLED', '').lower() == 'true'


def emit(phase, started, action='done'):
    if not _enabled or phase not in PHASES or action not in {'done', 'failed'}:
        return
    elapsed = min(999999999, max(0, round((clock()-started)*1000)))
    try:
        print(f'hermes_lite_startup phase={phase} action={action} elapsed_ms={elapsed}', file=sys.stderr, flush=True)
    except OSError:
        pass  # Diagnostics must not change startup/error behavior.


@contextmanager
def stage(phase):
    started = clock()
    try:
        yield
    except BaseException:
        emit(phase, started, 'failed')
        raise
    else:
        emit(phase, started)


def timed(phase):
    def decorate(function):
        @wraps(function)
        def wrapped(*args, **kwargs):
            with stage(phase):
                return function(*args, **kwargs)
        return wrapped
    return decorate
