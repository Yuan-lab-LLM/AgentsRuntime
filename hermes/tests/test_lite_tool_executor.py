import hashlib
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    "lite_executor_patcher", Path(__file__).resolve().parents[1] / "patches/hermes-agent/apply_lite_tool_executor.py")
patcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(patcher)


class ExecutorPatchIntegrityTests(unittest.TestCase):
    def test_changed_upstream_is_never_written(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            target = root / patcher.TARGET
            target.parent.mkdir()
            original = b"unexpected upstream changes\n"
            target.write_bytes(original)
            with self.assertRaisesRegex(ValueError, "exact locked upstream"):
                patcher.apply(root)
            self.assertEqual(original, target.read_bytes())

    def test_missing_or_ambiguous_anchor_fails_closed(self):
        for body in (b"missing", (patcher.ANCHOR * 2).encode()):
            with self.subTest(body=body), patch.object(patcher, "EXPECTED", hashlib.sha256(body).hexdigest()):
                with self.assertRaisesRegex(ValueError, "anchor changed"):
                    patcher.patched_source(body)


if __name__ == "__main__":
    unittest.main()
