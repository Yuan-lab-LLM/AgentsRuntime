from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

import apply_team_live_sessions as patch


class TeamLiveSessionsPatchTest(unittest.TestCase):
    def make_fixture(self, root: Path) -> None:
        (root / "agent").mkdir(parents=True)
        (root / "tui_gateway").mkdir(parents=True)
        (root / "web" / "src" / "pages").mkdir(parents=True)
        (root / "run_agent.py").write_text(
            patch.RUN_AGENT_METHOD_ANCHOR + patch.RUN_AGENT_FINALIZE_ANCHOR,
            encoding="utf-8",
        )
        (root / "agent" / "conversation_loop.py").write_text(
            "\n".join(
                [
                    patch.USER_MESSAGE_ANCHOR,
                    patch.TOOL_ROUND_ANCHOR,
                    patch.FINAL_MESSAGE_ANCHOR,
                ]
            ),
            encoding="utf-8",
        )
        (root / "web" / "src" / "pages" / "SessionsPage.tsx").write_text(
            "\n".join(
                [
                    patch.MESSAGE_LIST_SIGNATURE_ANCHOR,
                    patch.MESSAGE_LIST_EFFECT_ANCHOR,
                    patch.SESSION_EFFECT_ANCHOR,
                    patch.MESSAGE_LIST_CALL_ANCHOR,
                ]
            ),
            encoding="utf-8",
        )
        (root / "tui_gateway" / "server.py").write_text(
            "\n".join(
                [
                    patch.TUI_FINALIZE_ANCHOR,
                    patch.TUI_RESUME_ANCHOR,
                    patch.TUI_RESUME_INIT_ANCHOR,
                    patch.TUI_PROMPT_ANCHOR,
                ]
            ),
            encoding="utf-8",
        )

    def test_applies_scoped_runtime_and_web_patches_idempotently(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.make_fixture(root)

            patch.apply(root)
            first = {
                path: path.read_text(encoding="utf-8")
                for path in (
                    root / "run_agent.py",
                    root / "agent" / "conversation_loop.py",
                    root / "web" / "src" / "pages" / "SessionsPage.tsx",
                    root / "tui_gateway" / "server.py",
                )
            }
            patch.apply(root)

            self.assertIn(patch.RUNTIME_MARKER, first[root / "run_agent.py"])
            self.assertIn(
                'platform", "") or "").strip().lower() != "redis_team"',
                first[root / "run_agent.py"],
            )
            self.assertEqual(
                first[root / "agent" / "conversation_loop.py"].count(
                    "agent._checkpoint_session(messages, conversation_history)"
                ),
                3,
            )
            self.assertIn("redis_team_turn_exit", first[root / "run_agent.py"])
            web = first[root / "web" / "src" / "pages" / "SessionsPage.tsx"]
            self.assertIn(patch.WEB_MARKER, web)
            self.assertIn('session.source !== "redis_team"', web)
            self.assertIn("clearTimeout(timer)", web)
            self.assertIn("followTailRef", web)
            tui = first[root / "tui_gateway" / "server.py"]
            self.assertIn(patch.TUI_MARKER, tui)
            self.assertIn("external_redis_team", tui)
            self.assertIn("_clawmanager_tui_wrote", tui)
            for path, content in first.items():
                self.assertEqual(path.read_text(encoding="utf-8"), content)

    def test_fails_closed_when_upstream_anchor_drifts(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            self.make_fixture(root)
            (root / "agent" / "conversation_loop.py").write_text(
                "upstream changed",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(RuntimeError, "initial user message anchor"):
                patch.apply(root)


if __name__ == "__main__":
    unittest.main()
