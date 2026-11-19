"""Keep the additive Team hook compatible with both pinned gateway layouts."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location(
    "reader_patch", Path(__file__).parents[1] / "patches/hermes-agent/apply_team_context_reader.py"
)
reader_patch = importlib.util.module_from_spec(spec)
spec.loader.exec_module(reader_patch)


class ReaderPatchTest(unittest.TestCase):
    def test_legacy_registration_and_idempotence(self):
        native = '''class GatewayRunner:
    async def _handle_active_session_busy_message(self, event: MessageEvent, session_key: str) -> bool:
        return False
    def connect(self, adapter):
        adapter.set_busy_session_handler(self._handle_active_session_busy_message)
    def reconnect(self, adapter):
        adapter.set_busy_session_handler(self._handle_active_session_busy_message)
'''
        patched = reader_patch.patch(native)
        self.assertEqual(patched.count("adapter.set_clawmanager_team_agent_reader("), 2)
        self.assertEqual(reader_patch.patch(patched), patched)
        self.assertIn("return False", patched)

    def test_split_layout_keeps_native_busy_callback(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "run.py"
            adapters = target.with_name("run_adapters.py")
            target.write_text('''class GatewayRunner(GatewayAdapterLifecycleMixin):
    def _peek_session_state(self, key):
        return self.sessions.get(key)
''')
            original = '''class GatewayAdapterLifecycleMixin:
    def wire(self, adapter, busy_session_handler=None):
        adapter.set_busy_session_handler(busy_session_handler or self._primary_busy_session_handler())
'''
            adapters.write_text(original)
            reader_patch.patch_tree(target)
            installed = (target.read_text(), adapters.read_text())
            reader_patch.patch_tree(target)
            self.assertEqual(installed, (target.read_text(), adapters.read_text()))
            self.assertIn(original.splitlines()[-1], installed[1])
            self.assertEqual(installed[1].count("adapter.set_clawmanager_team_agent_reader("), 1)

    def test_unknown_split_layout_fails_without_partial_write(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "run.py"
            adapters = target.with_name("run_adapters.py")
            source = "class GatewayRunner(GatewayAdapterLifecycleMixin):\n    pass\n"
            target.write_text(source)
            adapters.write_text("# changed native wiring\n")
            with self.assertRaises(RuntimeError):
                reader_patch.patch_tree(target)
            self.assertEqual(target.read_text(), source)
            self.assertEqual(adapters.read_text(), "# changed native wiring\n")


if __name__ == "__main__":
    unittest.main()
