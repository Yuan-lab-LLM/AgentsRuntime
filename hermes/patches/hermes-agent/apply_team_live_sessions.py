#!/usr/bin/env python3
"""Patch pinned Hermes Agent for live Redis Team session visibility.

The patch is deliberately source- and platform-scoped:

* only ``redis_team`` agent turns checkpoint messages while running;
* Hermes' existing ``_last_flushed_db_idx`` remains the deduplication truth;
* the native Sessions page polls messages only for an expanded
  ``redis_team`` row;
* ordinary Hermes platforms retain their existing persistence and fetch flow.

Every replacement is build-failing on upstream drift.
"""

from __future__ import annotations

import sys
from pathlib import Path


RUNTIME_MARKER = "clawmanager-team-live-session-checkpoint-v1"
WEB_MARKER = "clawmanager-team-live-session-poll-v1"

RUN_AGENT_METHOD_ANCHOR = '''    def _persist_session(self, messages: List[Dict], conversation_history: List[Dict] = None):
        """Save session state to both JSON log and SQLite on any exit path.
'''
RUN_AGENT_METHOD_REPLACEMENT = '''    def _checkpoint_session(self, messages: List[Dict], conversation_history: List[Dict] = None):
        """Best-effort live persistence for the managed Redis Team platform.

        # clawmanager-team-live-session-checkpoint-v1
        # Keep ordinary Hermes platforms on their upstream persistence path.
        # Redis Team uses the native SessionDB as the single conversation
        # truth, and the existing flush index makes the final persist
        # idempotent after any number of live checkpoints.
        """
        if str(getattr(self, "platform", "") or "").strip().lower() != "redis_team":
            return
        self._session_messages = messages
        self._flush_messages_to_session_db(messages, conversation_history)

    def _persist_session(self, messages: List[Dict], conversation_history: List[Dict] = None):
        """Save session state to both JSON log and SQLite on any exit path.
'''

USER_MESSAGE_ANCHOR = '''    messages.append(user_msg)
    current_turn_user_idx = len(messages) - 1
    agent._persist_user_message_idx = current_turn_user_idx
'''
USER_MESSAGE_REPLACEMENT = '''    messages.append(user_msg)
    current_turn_user_idx = len(messages) - 1
    agent._persist_user_message_idx = current_turn_user_idx
    # clawmanager-team-live-session-checkpoint-v1: make the assignment
    # visible before the first model request starts.
    agent._checkpoint_session(messages, conversation_history)
'''

TOOL_ROUND_ANCHOR = '''                agent._execute_tool_calls(assistant_message, messages, effective_task_id, api_call_count)

                # clawmanager-team-completion-stop-v1
'''
TOOL_ROUND_REPLACEMENT = '''                agent._execute_tool_calls(assistant_message, messages, effective_task_id, api_call_count)

                # Persist the complete assistant/tool-result boundary. The
                # final session save sees the same flush index and cannot
                # duplicate these rows.
                agent._checkpoint_session(messages, conversation_history)

                # clawmanager-team-completion-stop-v1
'''

FINAL_MESSAGE_ANCHOR = '''                messages.append(final_msg)
                
                _turn_exit_reason = f"text_response(finish_reason={finish_reason})"
'''
FINAL_MESSAGE_REPLACEMENT = '''                messages.append(final_msg)
                agent._checkpoint_session(messages, conversation_history)
                
                _turn_exit_reason = f"text_response(finish_reason={finish_reason})"
'''

SESSION_EFFECT_ANCHOR = '''  useEffect(() => {
    if (isExpanded && messages === null && !loading) {
      setLoading(true);
      api
        .getSessionMessages(session.id)
        .then((resp) => setMessages(resp.messages))
        .catch((err) => setError(String(err)))
        .finally(() => setLoading(false));
    }
  }, [isExpanded, session.id, messages, loading]);
'''
SESSION_EFFECT_REPLACEMENT = '''  useEffect(() => {
    // Preserve the upstream one-shot fetch for every ordinary platform.
    if (
      session.source !== "redis_team" &&
      isExpanded &&
      messages === null &&
      !loading
    ) {
      setLoading(true);
      api
        .getSessionMessages(session.id)
        .then((resp) => setMessages(resp.messages))
        .catch((err) => setError(String(err)))
        .finally(() => setLoading(false));
    }
  }, [isExpanded, session.id, session.source, messages, loading]);

  useEffect(() => {
    if (!isExpanded || session.source !== "redis_team") return;

    // clawmanager-team-live-session-poll-v1
    // Redis Team sessions are created before the agent turn starts. Poll the
    // expanded native session so an initially empty row fills in while the
    // turn is still running. No ordinary Hermes session takes this path.
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;

    const loadLiveMessages = async (initial: boolean) => {
      if (initial) setLoading(true);
      try {
        const resp = await api.getSessionMessages(session.id);
        if (cancelled) return;
        setError(null);
        setMessages((current) =>
          current && JSON.stringify(current) === JSON.stringify(resp.messages)
            ? current
            : resp.messages,
        );
      } catch (err) {
        // A transient refresh failure must not erase messages already shown.
        if (!cancelled && initial) setError(String(err));
      } finally {
        if (!cancelled) {
          if (initial) setLoading(false);
          timer = setTimeout(() => void loadLiveMessages(false), 1500);
        }
      }
    };

    void loadLiveMessages(true);
    return () => {
      cancelled = true;
      if (timer !== null) clearTimeout(timer);
    };
  }, [isExpanded, session.id, session.source]);
'''

MESSAGE_LIST_SIGNATURE_ANCHOR = '''function MessageList({
  messages,
  highlight,
}: {
  messages: SessionMessage[];
  highlight?: string;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
'''
MESSAGE_LIST_SIGNATURE_REPLACEMENT = '''function MessageList({
  messages,
  highlight,
  followLive,
}: {
  messages: SessionMessage[];
  highlight?: string;
  followLive?: boolean;
}) {
  const containerRef = useRef<HTMLDivElement>(null);
  const followTailRef = useRef(true);
  const previousCountRef = useRef(0);
'''

MESSAGE_LIST_EFFECT_ANCHOR = '''  useEffect(() => {
    if (!highlight || !containerRef.current) return;
    // Scroll to first hit after render
    const timer = setTimeout(() => {
      const hit = containerRef.current?.querySelector("[data-search-hit]");
      if (hit) {
        hit.scrollIntoView({ behavior: "smooth", block: "center" });
      }
    }, 50);
    return () => clearTimeout(timer);
  }, [messages, highlight]);

  return (
    <div
      ref={containerRef}
      className="flex flex-col gap-3 max-h-[600px] overflow-y-auto pr-2"
    >
'''
MESSAGE_LIST_EFFECT_REPLACEMENT = '''  useEffect(() => {
    if (!highlight || !containerRef.current) return;
    // Scroll to first hit after render
    const timer = setTimeout(() => {
      const hit = containerRef.current?.querySelector("[data-search-hit]");
      if (hit) {
        hit.scrollIntoView({ behavior: "smooth", block: "center" });
      }
    }, 50);
    return () => clearTimeout(timer);
  }, [messages, highlight]);

  useEffect(() => {
    const previousCount = previousCountRef.current;
    previousCountRef.current = messages.length;
    if (
      !followLive ||
      highlight ||
      messages.length <= previousCount ||
      !followTailRef.current
    ) {
      return;
    }
    const frame = requestAnimationFrame(() => {
      const element = containerRef.current;
      if (element) element.scrollTop = element.scrollHeight;
    });
    return () => cancelAnimationFrame(frame);
  }, [messages, highlight, followLive]);

  return (
    <div
      ref={containerRef}
      className="flex flex-col gap-3 max-h-[600px] overflow-y-auto pr-2"
      onScroll={(event) => {
        const element = event.currentTarget;
        followTailRef.current =
          element.scrollHeight - element.scrollTop - element.clientHeight < 40;
      }}
    >
'''

MESSAGE_LIST_CALL_ANCHOR = '''            <MessageList messages={messages} highlight={searchQuery} />
'''
MESSAGE_LIST_CALL_REPLACEMENT = '''            <MessageList
              messages={messages}
              highlight={searchQuery}
              followLive={session.source === "redis_team" && session.is_active}
            />
'''


def replace_once(source: str, anchor: str, replacement: str, label: str) -> str:
    count = source.count(anchor)
    if count != 1:
        raise RuntimeError(f"expected exactly one {label} anchor, found {count}")
    return source.replace(anchor, replacement)


def patch_file(path: Path, marker: str, replacements: list[tuple[str, str, str]]) -> None:
    source = path.read_text(encoding="utf-8")
    if marker in source:
        return
    for anchor, replacement, label in replacements:
        source = replace_once(source, anchor, replacement, label)
    path.write_text(source, encoding="utf-8")


def apply(root: Path) -> None:
    patch_file(
        root / "run_agent.py",
        RUNTIME_MARKER,
        [(RUN_AGENT_METHOD_ANCHOR, RUN_AGENT_METHOD_REPLACEMENT, "run_agent checkpoint")],
    )
    patch_file(
        root / "agent" / "conversation_loop.py",
        RUNTIME_MARKER,
        [
            (USER_MESSAGE_ANCHOR, USER_MESSAGE_REPLACEMENT, "initial user message"),
            (TOOL_ROUND_ANCHOR, TOOL_ROUND_REPLACEMENT, "complete tool round"),
            (FINAL_MESSAGE_ANCHOR, FINAL_MESSAGE_REPLACEMENT, "final assistant message"),
        ],
    )
    patch_file(
        root / "web" / "src" / "pages" / "SessionsPage.tsx",
        WEB_MARKER,
        [
            (SESSION_EFFECT_ANCHOR, SESSION_EFFECT_REPLACEMENT, "Sessions live poll"),
            (
                MESSAGE_LIST_SIGNATURE_ANCHOR,
                MESSAGE_LIST_SIGNATURE_REPLACEMENT,
                "MessageList live signature",
            ),
            (
                MESSAGE_LIST_EFFECT_ANCHOR,
                MESSAGE_LIST_EFFECT_REPLACEMENT,
                "MessageList tail following",
            ),
            (MESSAGE_LIST_CALL_ANCHOR, MESSAGE_LIST_CALL_REPLACEMENT, "MessageList live call"),
        ],
    )


def main() -> int:
    if len(sys.argv) != 2:
        raise SystemExit("usage: apply_team_live_sessions.py <hermes-agent-root>")
    apply(Path(sys.argv[1]))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
