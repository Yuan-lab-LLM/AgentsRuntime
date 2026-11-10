"""Exercise the actual injected v0.22.5 behavior and its persistence placement."""
import ast
import importlib.util
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
from types import SimpleNamespace
import unittest

SOURCE = Path(os.environ.get("HERMES_LITE_TEST_PATCHED_ROOT", "/opt/hermes-agent"))
PATCHES = Path(__file__).resolve().parents[1] / "patches/hermes-agent"
sys.path.insert(0, str(PATCHES))
from apply_lite_team_completion_stop import BLOCK
from apply_lite_skill_dedupe import REPLACEMENT

class GitHubAdapters(unittest.TestCase):
    def test_split_round_returns_verdict_only_for_durable_receipts(self):
        source = 'def run():\n' + BLOCK + '        pass\n    return _verdict("continue")\n'
        for decision in ("accepted", "submitted", "deferred", "rejected", "", "unknown"):
            for ok in (True, False):
                env = dict(json=json, messages=[{"role": "tool", "name": "team_complete_task", "content": json.dumps({"ok": ok, "decision": decision})}],
                    agent=SimpleNamespace(_tool_guardrail_halt_decision=None, _strip_think_blocks=lambda s: s), assistant_message=SimpleNamespace(content=""), _verdict=lambda action: action)
                exec(source, env)
                self.assertEqual("break" if ok and decision in {"accepted", "submitted", "deferred"} else "continue", env["run"]())
        # A receipt from a preceding assistant turn cannot terminate this round.
        env["messages"] = [{"role": "tool", "name": "team_complete_task", "content": '{"ok":true,"decision":"accepted"}'}, {"role":"assistant"}, {"role":"tool","name":"other"}]
        self.assertEqual("continue", env["run"]())

    def test_skill_dedupe_preserves_modified_hub_owned_and_symlink_copies(self):
        for content, hub, link, removed in [("bundled",False,False,True),("origin",False,False,True),("modified",False,False,False),("bundled",True,False,False),("bundled",False,True,False)]:
            with self.subTest(content=content, hub=hub, link=link), tempfile.TemporaryDirectory() as tmp:
                root=Path(tmp); real=root/"real"; real.mkdir(); (real/"SKILL.md").write_text(content)
                dest=root/"skill"
                if link: dest.symlink_to(real, target_is_directory=True)
                else: shutil.copytree(real,dest)
                st=SimpleNamespace(manifest={"skill":"origin"}, external_conflicts=[],say=lambda s:None)
                env=dict(st=st, skill_name="skill", dest=dest, bundled_hash="bundled",dir_hash=lambda p:(p/"SKILL.md").read_text(),_skills_dir=lambda:root,_read_hub_install_paths=lambda:{"skill"} if hub else set(),_rmtree_writable=shutil.rmtree)
                exec('def run():\n'+REPLACEMENT,env);env["run"]()
                self.assertEqual(not removed,dest.exists())
                self.assertEqual([] if removed else ["skill"],st.external_conflicts)
                self.assertTrue(real.exists())

    @unittest.skipUnless((SOURCE / "agent/turn_tool_round.py").exists(), "requires patched v0.22.5 source image")
    def test_actual_round_keeps_persistence_failure_ahead_of_completion(self):
        text=(SOURCE / "agent/turn_tool_round.py").read_text()
        self.assertLess(text.index('if getattr(agent, "_incremental_persistence_failed"'),text.index('# clawmanager-team-completion-stop-v1'))
        self.assertLess(text.index('# clawmanager-team-completion-stop-v1'),text.index('if agent._tool_guardrail_halt_decision'))
        compile(text,"turn_tool_round.py","exec")

if __name__ == "__main__": unittest.main()
