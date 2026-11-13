"""Expose only the already running Team agent to its Redis adapter.

No generic busy-input policy, gateway-control flag or interruption changes.
The adapter checks current business identity before calling native steer().
"""
import re
import sys
from pathlib import Path

MARKER = "clawmanager-team-context-reader-v1"
READER = '''    # clawmanager-team-context-reader-v1
    def _clawmanager_team_active_agent(self, source):
        if getattr(source.platform, "value", source.platform) != "redis_team":
            return None
        key = self._session_key_for_source(source)
        if hasattr(self, "_peek_session_state"):
            state = self._peek_session_state(key)
            return state.turn.agent if state and state.turn else None
        return self._running_agents.get(key)

'''


def register(match):
    indent = match.group(1)
    return match.group(0) + "\n" + indent + 'if hasattr(adapter, "set_clawmanager_team_agent_reader"):\n' + indent + "    adapter.set_clawmanager_team_agent_reader(self._clawmanager_team_active_agent)"


def patch(source: str) -> str:
    if MARKER in source:
        return source
    anchor = "    async def _handle_active_session_busy_message(self, event: MessageEvent, session_key: str) -> bool:"
    if source.count(anchor) != 1:
        raise RuntimeError("Unsupported Hermes active-session boundary")
    source = source.replace(anchor, READER + anchor)
    expression = r"(?m)^( +)adapter\.set_busy_session_handler\(self\._handle_active_session_busy_message\)$"
    source, count = re.subn(expression, register, source)
    if count != 2:
        raise RuntimeError(f"Unsupported Hermes adapter registration boundary: {count}")
    return source


def patch_tree(target: Path) -> None:
    source = target.read_text(encoding="utf-8")
    # Hermes 0.22.5 moved native busy admission and shared adapter wiring into
    # mixins. Preserve those callbacks and add only the Redis Team reader.
    if "class GatewayRunner(" in source and "GatewayAdapterLifecycleMixin" in source:
        adapters = target.with_name("run_adapters.py")
        wiring = adapters.read_text(encoding="utf-8")
        if MARKER in source and "adapter.set_clawmanager_team_agent_reader(" in wiring:
            return
        anchor = "    def _peek_session_state("
        matches = list(re.finditer(r"(?m)^" + re.escape(anchor), source))
        expression = r"(?m)^( +)adapter\.set_busy_session_handler\(busy_session_handler or self\._primary_busy_session_handler\(\)\)$"
        wiring, count = re.subn(expression, register, wiring)
        if len(matches) != 1 or count != 1 or MARKER in source:
            raise RuntimeError("Unsupported Hermes split adapter boundary")
        source = source[:matches[0].start()] + READER + source[matches[0].start():]
        # Validate both files before writing either; a new upstream layout
        # must fail the image build instead of silently dropping delivery.
        compile(source, str(target), "exec")
        compile(wiring, str(adapters), "exec")
        target.write_text(source, encoding="utf-8")
        adapters.write_text(wiring, encoding="utf-8")
        return
    target.write_text(patch(source), encoding="utf-8")


if __name__ == "__main__":
    target = Path(sys.argv[1])
    patch_tree(target)
