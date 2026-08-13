#!/usr/bin/env python3
"""Patch pinned Hermes Agent to stop after an accepted Team completion tool.

The patch is deliberately narrow and build-failing on upstream drift. It does
not change generic tool-loop semantics: only the redis_team completion tool's
authenticated `decision=accepted` result ends the current model turn.
"""

from __future__ import annotations

import sys
from pathlib import Path


MARKER = "clawmanager-team-completion-stop-v1"
ANCHOR = """                agent._execute_tool_calls(assistant_message, messages, effective_task_id, api_call_count)

                if agent._tool_guardrail_halt_decision is not None:
"""
REPLACEMENT = """                agent._execute_tool_calls(assistant_message, messages, effective_task_id, api_call_count)

                # clawmanager-team-completion-stop-v1
                # An accepted explicit Team completion is already durable in
                # ClawManager. Do not spend another model iteration asking the
                # model what to do after its terminal tool succeeded. Rejected
                # and pending receipts deliberately retain the normal loop.
                _team_completion_accepted = False
                for _team_result_message in reversed(messages):
                    if not isinstance(_team_result_message, dict):
                        continue
                    if _team_result_message.get("role") == "assistant":
                        break
                    if (
                        _team_result_message.get("role") != "tool"
                        or _team_result_message.get("name") != "team_complete_task"
                    ):
                        continue
                    try:
                        _team_result_payload = json.loads(_team_result_message.get("content") or "{}")
                    except (TypeError, ValueError):
                        _team_result_payload = {}
                    _team_completion_accepted = bool(
                        isinstance(_team_result_payload, dict)
                        and _team_result_payload.get("ok") is True
                        and str(_team_result_payload.get("decision") or "").strip().lower() == "accepted"
                    )
                    break
                if _team_completion_accepted:
                    _turn_exit_reason = "team_completion_accepted"
                    final_response = (
                        agent._strip_think_blocks(assistant_message.content or "").strip()
                        or "Completion accepted by ClawManager."
                    )
                    break

                if agent._tool_guardrail_halt_decision is not None:
"""


def main() -> int:
    if len(sys.argv) != 2:
        raise SystemExit("usage: apply_team_completion_stop.py <conversation_loop.py>")
    target = Path(sys.argv[1])
    source = target.read_text(encoding="utf-8")
    if MARKER in source:
        return 0
    count = source.count(ANCHOR)
    if count != 1:
        raise RuntimeError(f"expected exactly one Hermes tool-loop anchor, found {count}")
    target.write_text(source.replace(ANCHOR, REPLACEMENT), encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
