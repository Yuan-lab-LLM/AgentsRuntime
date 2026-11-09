#!/usr/bin/env python3
"""Patch pinned Hermes Agent to yield after a durable Team completion tool.

The patch is deliberately narrow and build-failing on upstream drift. It does
not change generic tool-loop semantics: only the redis_team completion tool's
authenticated accepted/submitted/deferred receipts end the current model turn.
Pending receipts do not complete the assignment: the existing late-ack watcher
and ClawManager remain authoritative. The tool result is persisted first.
"""

from __future__ import annotations

import sys
from pathlib import Path


MARKER = "clawmanager-team-completion-stop-v1"
ANCHOR = """                if agent._tool_guardrail_halt_decision is not None:
"""
REPLACEMENT = """                # The pinned Hermes release persists every tool result before
                # control returns here. Keep this ClawManager-specific stop
                # decision after that persistence boundary and before generic
                # tool guardrail handling.
                # clawmanager-team-completion-stop-v1
                # An accepted explicit Team completion is already durable in
                # ClawManager. Do not spend another model iteration asking the
                # model what to do after its explicit tool succeeded. Pending
                # receipts yield to the existing acknowledgement watcher;
                # rejected receipts retain the normal correction loop.
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
                        and str(_team_result_payload.get("decision") or "").strip().lower() in {"accepted", "submitted", "deferred"}
                    )
                    break
                if _team_completion_accepted:
                    _team_decision = str(_team_result_payload.get("decision") or "").strip().lower()
                    _turn_exit_reason = "team_completion_accepted" if _team_decision == "accepted" else "team_completion_pending"
                    final_response = (
                        agent._strip_think_blocks(assistant_message.content or "").strip()
                        or ("Completion accepted by ClawManager." if _team_decision == "accepted" else "Completion submitted; awaiting ClawManager decision.")
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
        raise RuntimeError(f"expected exactly one Hermes post-tool guardrail anchor, found {count}")
    target.write_text(source.replace(ANCHOR, REPLACEMENT), encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
