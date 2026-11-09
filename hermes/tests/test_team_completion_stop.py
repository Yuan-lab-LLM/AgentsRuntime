import importlib.util
import json
from pathlib import Path
import textwrap
from types import SimpleNamespace
import unittest


class CompletionStop(unittest.TestCase):
    def test_only_durable_explicit_receipts_end_the_current_turn(self):
        path = Path(__file__).resolve().parents[1] / 'patches/hermes-agent/apply_team_completion_stop.py'
        spec = importlib.util.spec_from_file_location('completion_patch', path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        # Execute the actual injected post-persistence block, not a copied predicate.
        block = textwrap.dedent(module.REPLACEMENT) + '    pass\n'
        code = "_turn_exit_reason = 'continued'\nfinal_response = ''\nfor _ in range(1):\n" + textwrap.indent(block, '    ')
        for decision in ('accepted', 'submitted', 'deferred', 'rejected', '', 'unknown'):
            for ok in (True, False):
                with self.subTest(decision=decision, ok=ok):
                    env = {'json': json, 'messages': [{'role': 'tool', 'name': 'team_complete_task',
                           'content': json.dumps({'ok': ok, 'decision': decision})}],
                           'agent': SimpleNamespace(_tool_guardrail_halt_decision=None, _strip_think_blocks=lambda text: text),
                           'assistant_message': SimpleNamespace(content='')}
                    exec(code, env)
                    expected = ('team_completion_accepted' if decision == 'accepted' else 'team_completion_pending') if ok and decision in {'accepted', 'submitted', 'deferred'} else 'continued'
                    self.assertEqual(expected, env['_turn_exit_reason'])
                    if decision in {'submitted', 'deferred'} and ok:
                        self.assertNotIn('accepted', env['final_response'])
