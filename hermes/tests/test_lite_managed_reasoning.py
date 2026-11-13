import ast
import importlib.util
from pathlib import Path
import tempfile
import unittest

path = Path(__file__).resolve().parents[1] / "patches/hermes-agent/apply_lite_managed_reasoning.py"
spec = importlib.util.spec_from_file_location("managed_reasoning_patch", path)
patcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(patcher)


class ManagedReasoningPatchTests(unittest.TestCase):
    def test_only_unset_default_changes(self):
        source = 'class CustomProfile:\n    def default_reasoning_config(self, model=None):\n' + patcher.BEFORE + '\n    def explicit(self, effort):\n        return effort\n'
        output = patcher.rewrite(source)
        namespace = {}
        exec(compile(ast.parse(output), '<patched-provider>', 'exec'), namespace)
        profile = namespace['CustomProfile']()
        self.assertIsNone(profile.default_reasoning_config('auto'))
        for effort in ['low', 'medium', 'high', 'max', 'none']:
            self.assertEqual(effort, profile.explicit(effort))

    def test_unrecognized_source_is_rejected_without_write(self):
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / 'plugins/model-providers/custom/__init__.py'
            target.parent.mkdir(parents=True)
            target.write_text('changed upstream source', encoding='utf-8')
            with self.assertRaisesRegex(ValueError, 'exact locked'):
                patcher.apply(tmp)
            self.assertEqual('changed upstream source', target.read_text(encoding='utf-8'))

    def test_missing_or_repeated_anchor_is_rejected(self):
        for source in ['', patcher.BEFORE * 2]:
            with self.assertRaises(ValueError):
                patcher.rewrite(source)


if __name__ == '__main__':
    unittest.main()
